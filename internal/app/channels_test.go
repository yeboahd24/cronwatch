package app

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/cronwatch/internal/model"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

// The message notify sends is built from exactly the variables hooks get.
func TestMessageFromHookEnviron(t *testing.T) {
	code := 3
	started := time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC)
	run := model.Run{ID: "r1", Status: "failed", ExitCode: &code, StartedAt: started, Stderr: "dumping\npg_dump: command not found\n"}
	e := hookEvent{Kind: "failed", Job: model.Job{Name: "Database Backup", Slug: "database-backup"}, Run: &run}
	m, err := messageFromEnv(getenvOf(e.environ()))
	if err != nil {
		t.Fatal(err)
	}
	if m.Event != "failed" || m.JobSlug != "database-backup" || m.RunID != "r1" || m.ExitCode == nil || *m.ExitCode != 3 ||
		m.LastError != "pg_dump: command not found" || m.StartedAt == nil || !m.StartedAt.Equal(started) {
		t.Errorf("message = %+v", m)
	}

	expected := time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC)
	missed := hookEvent{Kind: "missed", Job: model.Job{Name: "Database Backup", Slug: "database-backup"}, ExpectedAt: expected}
	m, err = messageFromEnv(getenvOf(missed.environ()))
	if err != nil {
		t.Fatal(err)
	}
	if m.Event != "missed" || m.ExitCode != nil || m.ExpectedAt == nil || !m.ExpectedAt.Equal(expected) {
		t.Errorf("missed message = %+v", m)
	}
}

func getenvOf(environ []string) func(string) string {
	vars := map[string]string{}
	for _, kv := range environ {
		k, v, _ := strings.Cut(kv, "=")
		vars[k] = v
	}
	return func(k string) string { return vars[k] }
}

func TestNotifyCommand(t *testing.T) {
	var bodies []string
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
	}))
	defer ok.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad token", http.StatusUnauthorized)
	}))
	defer broken.Close()

	t.Setenv("CRONWATCH_EVENT", "")
	var stdout bytes.Buffer
	err := Run(context.Background(), []string{"notify", ok.URL}, &stdout, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--test") {
		t.Errorf("without hook variables: %v", err)
	}

	t.Setenv("CRONWATCH_EVENT", "recovered")
	t.Setenv("CRONWATCH_JOB_NAME", "Database Backup")
	err = Run(context.Background(), []string{"notify", "ntfy+" + ok.URL + "/backups", "slack+" + broken.URL + "/hooks/sekret"}, &stdout, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "sekret") {
		t.Errorf("err = %v", err)
	}
	if len(bodies) != 1 || !strings.Contains(stdout.String(), "sent to ntfy (127.0.0.1:") {
		t.Errorf("bodies = %q, stdout = %q", bodies, stdout.String())
	}

	if err := Run(context.Background(), []string{"notify", "nope"}, &stdout, io.Discard); err == nil {
		t.Error("bad URL accepted")
	}
}

// hooksOf returns the stored hooks of the job with slug in dir.
func hooksOf(t *testing.T, dir, slug string) (onFailure, onRecover string) {
	t.Helper()
	ctx := context.Background()
	s, err := storage.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	job, err := s.GetJobBySlug(ctx, slug)
	if err != nil {
		t.Fatal(err)
	}
	return job.OnFailure, job.OnRecover
}

func isNotifyHook(hook string, urls ...string) bool {
	want, _ := notifyHook(urls)
	return hook == want
}

func TestNotifyFlagSetsBothHooks(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	run := func(env map[string]string, flags ...string) error {
		for k, v := range env {
			t.Setenv(k, v)
		}
		args := append([]string{"run", "--name", "job", "--data-dir", dir}, flags...)
		return Run(ctx, append(args, "--", "true"), io.Discard, io.Discard)
	}
	t.Setenv(envOnFailure, "")
	t.Setenv(envOnRecover, "")
	t.Setenv(envNotify, "")

	if err := run(nil, "--notify", "https://ntfy.sh/a", "--notify", "https://example.com/hook"); err != nil {
		t.Fatal(err)
	}
	f, r := hooksOf(t, dir, "job")
	if !isNotifyHook(f, "https://ntfy.sh/a", "https://example.com/hook") || f != r {
		t.Errorf("hooks = %q, %q", f, r)
	}

	// --on-recover wins for its own event.
	if err := run(nil, "--notify", "https://ntfy.sh/a", "--on-recover", "echo back"); err != nil {
		t.Fatal(err)
	}
	if f, r := hooksOf(t, dir, "job"); !isNotifyHook(f, "https://ntfy.sh/a") || r != "echo back" {
		t.Errorf("hooks = %q, %q", f, r)
	}

	// A line's --notify beats the crontab's CRONWATCH_ON_FAILURE.
	if err := run(map[string]string{envOnFailure: "echo env"}, "--notify", "https://ntfy.sh/a"); err != nil {
		t.Fatal(err)
	}
	if f, _ := hooksOf(t, dir, "job"); !isNotifyHook(f, "https://ntfy.sh/a") {
		t.Errorf("on-failure = %q", f)
	}

	// CRONWATCH_NOTIFY fills what CRONWATCH_ON_FAILURE/ON_RECOVER leave empty.
	if err := run(map[string]string{envOnFailure: "echo env", envNotify: "https://ntfy.sh/b"}); err != nil {
		t.Fatal(err)
	}
	if f, r := hooksOf(t, dir, "job"); f != "echo env" || !isNotifyHook(r, "https://ntfy.sh/b") {
		t.Errorf("hooks = %q, %q", f, r)
	}

	// A bad --notify is an error; a bad CRONWATCH_NOTIFY only a warning.
	if err := run(nil, "--notify", "ntfy.sh/a"); err == nil || !strings.Contains(err.Error(), "--notify URL 1") {
		t.Errorf("bad --notify: %v", err)
	}
	if err := run(map[string]string{envOnFailure: "", envNotify: "nope"}); err != nil {
		t.Errorf("bad %s stopped the run: %v", envNotify, err)
	}
	if f, _ := hooksOf(t, dir, "job"); f != "echo env" {
		t.Errorf("bad %s changed the hook to %q", envNotify, f)
	}
}

func TestNotifyEnvForPingAndSync(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv(envOnFailure, "")
	t.Setenv(envOnRecover, "")
	t.Setenv(envNotify, "https://ntfy.sh/p")
	if err := Run(ctx, []string{"ping", "--data-dir", dir, "etl"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f, r := hooksOf(t, dir, "etl"); !isNotifyHook(f, "https://ntfy.sh/p") || f != r {
		t.Errorf("ping hooks = %q, %q", f, r)
	}

	s, err := storage.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tab := "CRONWATCH_NOTIFY=https://ntfy.sh/s\n" +
		"0 2 * * * cronwatch run --name backup -- ./backup.sh\n" +
		"0 3 * * * cronwatch run --name report --notify https://example.com/r -- ./report.sh\n"
	result, err := syncCrontab(ctx, s, tab)
	if err != nil || len(result.Problems) != 0 {
		t.Fatalf("sync: %v %v", err, result.Problems)
	}
	if f, r := hooksOf(t, dir, "backup"); !isNotifyHook(f, "https://ntfy.sh/s") || f != r {
		t.Errorf("backup hooks = %q, %q", f, r)
	}
	if f, _ := hooksOf(t, dir, "report"); !isNotifyHook(f, "https://example.com/r") {
		t.Errorf("report hook = %q", f)
	}

	result, err = syncCrontab(ctx, s, "CRONWATCH_NOTIFY=nope\n0 2 * * * cronwatch run --name backup -- ./backup.sh\n")
	if err != nil || len(result.Problems) != 1 || !strings.Contains(result.Problems[0], envNotify) {
		t.Errorf("bad CRONWATCH_NOTIFY in sync: %v %v", err, result.Problems)
	}
}
