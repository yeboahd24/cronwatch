package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/yeboahd24/cronwatch/internal/apijson"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestTags(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv(envOnFailure, "")
	t.Setenv(envOnRecover, "")
	t.Setenv(envNotify, "")
	cli := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := Run(ctx, append(args[:1:1], append([]string{"--data-dir", dir}, args[1:]...)...), &out, io.Discard)
		return out.String(), err
	}
	run := func(name string, flags ...string) {
		t.Helper()
		args := append([]string{"run", "--name", name}, flags...)
		if _, err := cli(append(args, "--", "true")...); err != nil {
			t.Fatal(err)
		}
	}
	tagsOf := func(slug string) []string {
		t.Helper()
		s := openStore(t, dir)
		defer s.Close()
		job, err := s.GetJobBySlug(ctx, slug)
		if err != nil {
			t.Fatal(err)
		}
		return job.Tags
	}

	run("Backup", "--tag", "Backup,db", "--tag", "nightly")
	if got := tagsOf("backup"); !slices.Equal(got, []string{"backup", "db", "nightly"}) {
		t.Errorf("tags = %q", got)
	}
	run("Backup") // without --tag, tags are kept
	if got := tagsOf("backup"); len(got) != 3 {
		t.Errorf("tags after a run without --tag = %q", got)
	}
	run("Web", "--tag", "web")
	run("Untagged")
	if _, err := cli("ping", "--tag", "db", "etl"); err != nil {
		t.Fatal(err)
	}
	if got := tagsOf("etl"); !slices.Equal(got, []string{"db"}) {
		t.Errorf("ping tags = %q", got)
	}

	out, err := cli("jobs", "--tag", "db")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "TAGS") || !strings.Contains(out, "backup,db,nightly") || !strings.Contains(out, "etl") || strings.Contains(out, "Web") {
		t.Errorf("jobs --tag db:\n%s", out)
	}
	out, err = cli("jobs", "--json", "--tag", "db", "--tag", "nightly")
	if err != nil {
		t.Fatal(err)
	}
	var jobs []apijson.Job
	if err := json.Unmarshal([]byte(out), &jobs); err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Slug != "backup" || !slices.Equal(jobs[0].Tags, []string{"backup", "db", "nightly"}) {
		t.Errorf("jobs --json with two tags = %+v", jobs)
	}
	if out, _ := cli("jobs", "--json"); !strings.Contains(out, `"tags": []`) {
		t.Errorf("an untagged job's tags are not []:\n%s", out)
	}

	// check counts only the tagged jobs.
	if _, err := cli("run", "--name", "Web", "--", "false"); err == nil {
		t.Fatal("false succeeded")
	}
	if out, err := cli("check", "--tag", "db"); err != nil || !strings.HasPrefix(out, "CRONWATCH OK") {
		t.Errorf("check --tag db: %v\n%s", err, out)
	}
	if out, err := cli("check", "--tag", "web"); !strings.HasPrefix(out, "CRONWATCH CRITICAL") {
		t.Errorf("check --tag web: %v\n%s", err, out)
	} else if exit, ok := errors.AsType[*ExitError](err); !ok || exit.Code != checkCritical {
		t.Errorf("check --tag web exit: %v", err)
	}
	if out, err := cli("digest", "--tag", "db"); err != nil || strings.Contains(out, "Web") || !strings.Contains(out, "Backup") {
		t.Errorf("digest --tag db: %v\n%s", err, out)
	}

	run("Backup", "--tag", "") // removes them
	if got := tagsOf("backup"); len(got) != 0 {
		t.Errorf("tags after --tag \"\" = %q", got)
	}
	if _, err := cli("run", "--name", "Backup", "--tag", "two words", "--", "true"); err == nil || !strings.Contains(err.Error(), "--tag") {
		t.Errorf("bad tag: %v", err)
	}
}

func TestHubPingTags(t *testing.T) {
	t.Setenv(envOnFailure, "")
	t.Setenv(envOnRecover, "")
	t.Setenv(envNotify, "")
	s := openStore(t, t.TempDir())
	ping := pingHub(t, s)
	if code, body := ping("/api/v1/ping/etl?tag=db&tag=nightly", ""); code != http.StatusNoContent {
		t.Fatalf("%d %s", code, body)
	}
	job, err := s.GetJobBySlug(context.Background(), "etl")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(job.Tags, []string{"db", "nightly"}) {
		t.Errorf("tags = %q", job.Tags)
	}
	if code, _ := ping("/api/v1/ping/etl?tag=no+spaces", ""); code != http.StatusBadRequest {
		t.Errorf("bad tag: %d", code)
	}
}

func TestSyncSetsTags(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, t.TempDir())
	t.Setenv(envNotify, "")
	sync := func(tab string) syncResult {
		t.Helper()
		result, err := syncCrontab(ctx, s, tab)
		if err != nil || len(result.Problems) != 0 {
			t.Fatalf("sync: %v %v", err, result.Problems)
		}
		return result
	}
	tagsOf := func() []string {
		t.Helper()
		job, err := s.GetJobBySlug(ctx, "backup")
		if err != nil {
			t.Fatal(err)
		}
		return job.Tags
	}
	sync("0 2 * * * cronwatch run --name backup -- ./backup.sh\n")
	if got := tagsOf(); len(got) != 0 {
		t.Fatalf("tags = %q", got)
	}
	// Adding --tag to an existing job's line updates the job.
	if r := sync("0 2 * * * cronwatch run --name backup --tag db -- ./backup.sh\n"); !slices.Equal(r.Updated, []string{"backup"}) {
		t.Errorf("updated = %q", r.Updated)
	}
	if got := tagsOf(); !slices.Equal(got, []string{"db"}) {
		t.Errorf("tags = %q", got)
	}
	// The same tags again are no change; a line without --tag keeps them.
	if r := sync("0 2 * * * cronwatch run --name backup --tag db -- ./backup.sh\n"); len(r.Updated) != 0 {
		t.Errorf("updated = %q", r.Updated)
	}
	sync("0 2 * * * cronwatch run --name backup -- ./backup.sh\n")
	if got := tagsOf(); !slices.Equal(got, []string{"db"}) {
		t.Errorf("tags after a line without --tag = %q", got)
	}
}
