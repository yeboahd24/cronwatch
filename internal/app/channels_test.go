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
