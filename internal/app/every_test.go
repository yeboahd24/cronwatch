package app

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestEveryFlag(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv(envOnFailure, "")
	t.Setenv(envOnRecover, "")
	t.Setenv(envNotify, "")
	cli := func(args ...string) error {
		return Run(ctx, append(args[:1:1], append([]string{"--data-dir", dir}, args[1:]...)...), io.Discard, io.Discard)
	}
	scheduleOf := func(slug string) string {
		t.Helper()
		s := openStore(t, dir)
		defer s.Close()
		job, err := s.GetJobBySlug(ctx, slug)
		if err != nil {
			t.Fatal(err)
		}
		if job.Schedule == nil {
			return ""
		}
		return *job.Schedule
	}

	if err := cli("run", "--name", "sync", "--every", "90m", "--", "true"); err != nil {
		t.Fatal(err)
	}
	if got := scheduleOf("sync"); got != "@every 1h30m" {
		t.Errorf("schedule = %q", got)
	}
	// A cron schedule replaces it, and the other way round.
	if err := cli("run", "--name", "sync", "--schedule", "0 2 * * *", "--", "true"); err != nil {
		t.Fatal(err)
	}
	if got := scheduleOf("sync"); got != "0 2 * * *" {
		t.Errorf("schedule = %q", got)
	}
	if err := cli("ping", "--every", "2h", "sync"); err != nil {
		t.Fatal(err)
	}
	if got := scheduleOf("sync"); got != "@every 2h" {
		t.Errorf("schedule after ping --every = %q", got)
	}

	for _, args := range [][]string{
		{"run", "--name", "sync", "--every", "1h", "--schedule", "0 2 * * *", "--", "true"},
		{"run", "--name", "sync", "--every", "30s", "--", "true"},
		{"ping", "--every", "1h", "--schedule", "0 2 * * *", "sync"},
		{"ping", "--every", "0", "sync"},
	} {
		if err := cli(args...); err == nil {
			t.Errorf("%q accepted", args)
		}
	}
	if got := scheduleOf("sync"); got != "@every 2h" {
		t.Errorf("a rejected flag changed the schedule to %q", got)
	}
}

func TestHubPingEvery(t *testing.T) {
	t.Setenv(envOnFailure, "")
	t.Setenv(envOnRecover, "")
	t.Setenv(envNotify, "")
	s := openStore(t, t.TempDir())
	ping := pingHub(t, s)
	if code, body := ping("/api/v1/ping/nas-backup?every=24h&grace=1h", ""); code != http.StatusNoContent {
		t.Fatalf("%d %s", code, body)
	}
	job, err := s.GetJobBySlug(context.Background(), "nas-backup")
	if err != nil {
		t.Fatal(err)
	}
	if job.Schedule == nil || *job.Schedule != "@every 24h" || job.GraceSeconds != 3600 {
		t.Errorf("job = %+v", job)
	}
	if code, body := ping("/api/v1/ping/nas-backup?every=1h&schedule=0+2+*+*+*", ""); code != http.StatusBadRequest || !strings.Contains(body, "cannot go together") {
		t.Errorf("every with schedule: %d %s", code, body)
	}
}
