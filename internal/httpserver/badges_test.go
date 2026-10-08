package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yeboahd24/cronwatch/internal/storage"
)

func TestBadges(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tags := []string{"backup"}
	job, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "db-backup", Name: "DB Backup", Command: `"true"`, Tags: &tags})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateRun(ctx, job.ID, job.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	code := 1
	if err := s.CompleteRun(ctx, run.ID, storage.Completion{Ended: job.CreatedAt, Status: "failed", ExitCode: &code}); err != nil {
		t.Fatal(err)
	}
	server, err := New(s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	fetch := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "localhost:8765"
		rec := httptest.NewRecorder()
		server.Router.ServeHTTP(rec, req)
		return rec
	}

	rec := fetch("/badge/db-backup.svg")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "image/svg+xml") ||
		rec.Header().Get("Cache-Control") != "no-cache, max-age=0" {
		t.Fatalf("job badge: %d %v", rec.Code, rec.Header())
	}
	if body := rec.Body.String(); !strings.Contains(body, ">DB Backup</text>") || !strings.Contains(body, ">failed</text>") {
		t.Errorf("job badge:\n%s", body)
	}
	if body := fetch("/badge/db-backup.svg?label=Backups").Body.String(); !strings.Contains(body, ">Backups</text>") {
		t.Errorf("?label= ignored:\n%s", body)
	}
	if rec := fetch("/badge/db-backup.json"); rec.Header().Get("Content-Type") != "application/json" || !strings.Contains(rec.Body.String(), `"color":"red"`) {
		t.Errorf("shields JSON: %v %s", rec.Header(), rec.Body.String())
	}
	if body := fetch("/badge/tag/backup.svg").Body.String(); !strings.Contains(body, ">1 of 1 failing</text>") {
		t.Errorf("tag badge:\n%s", body)
	}
	if body := fetch("/badge/tag/nothing.svg").Body.String(); !strings.Contains(body, ">no jobs</text>") {
		t.Errorf("empty tag badge:\n%s", body)
	}
	for _, path := range []string{"/badge/nope.svg", "/badge/db-backup.png", "/badge/db-backup", "/badge/tag/Not%20A%20Tag.svg"} {
		if rec := fetch(path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
	// The job page offers the badge's Markdown.
	page := get(t, server, "/jobs/"+job.ID)
	if !strings.Contains(page, "![DB Backup](http://localhost:8765/badge/db-backup.svg)") {
		t.Error("the job page lacks the badge Markdown")
	}
}
