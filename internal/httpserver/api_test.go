package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/cronwatch/internal/apijson"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

func TestAPI(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tags := []string{"db"}
	backup, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "backup", Name: "Backup", Command: `"true"`, Tags: &tags})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "web", Name: "Web", Command: `"true"`}); err != nil {
		t.Fatal(err)
	}
	// Five runs of backup, a minute apart; the newest failed.
	var ids []string
	for i := range 5 {
		started := backup.CreatedAt.Add(time.Duration(i) * time.Minute)
		run, err := s.CreateRun(ctx, backup.ID, started)
		if err != nil {
			t.Fatal(err)
		}
		status, code := "success", 0
		if i == 4 {
			status, code = "failed", 2
		}
		if err := s.CompleteRun(ctx, run.ID, storage.Completion{Ended: started.Add(time.Second), Duration: time.Second, Status: status, ExitCode: &code}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, run.ID)
	}
	server, err := New(s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	call := func(path string, v any) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "localhost:8765"
		rec := httptest.NewRecorder()
		server.Router.ServeHTTP(rec, req)
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s: Content-Type %q", path, ct)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
			t.Fatalf("%s: %v\n%s", path, err, rec.Body.String())
		}
		return rec.Code
	}

	var jobs struct{ Jobs []apijson.Job }
	if code := call("/api/v1/jobs", &jobs); code != http.StatusOK || len(jobs.Jobs) != 2 {
		t.Fatalf("jobs: %d %+v", code, jobs)
	}
	if call("/api/v1/jobs?tag=db", &jobs); len(jobs.Jobs) != 1 || jobs.Jobs[0].Slug != "backup" || jobs.Jobs[0].Status != "failed" ||
		jobs.Jobs[0].LastRun == nil || *jobs.Jobs[0].LastRun.ExitCode != 2 {
		t.Errorf("jobs?tag=db: %+v", jobs)
	}
	var one struct{ Job apijson.Job }
	if code := call("/api/v1/jobs/web", &one); code != http.StatusOK || one.Job.Name != "Web" || one.Job.Status != "never_run" {
		t.Errorf("job: %d %+v", code, one)
	}

	// Pages of two, newest first, following "next".
	var got []string
	next := "/api/v1/runs?job=backup&limit=2"
	for pages := 0; next != ""; pages++ {
		if pages == 5 {
			t.Fatal("next never ran out")
		}
		var page struct {
			Runs []apijson.Run
			Next *string
		}
		if code := call(next, &page); code != http.StatusOK {
			t.Fatalf("%s: %d", next, code)
		}
		for _, r := range page.Runs {
			if r.JobSlug != "backup" || r.Job != "Backup" {
				t.Errorf("run %+v", r)
			}
			got = append(got, r.ID)
		}
		next = ""
		if page.Next != nil {
			next = *page.Next
			if !strings.Contains(next, "limit=2") || !strings.Contains(next, "job=backup") {
				t.Errorf("next %q dropped the filter or limit", next)
			}
		}
	}
	if len(got) != 5 || got[0] != ids[4] || got[4] != ids[0] {
		t.Errorf("runs over pages = %q, want newest first %q", got, ids)
	}
	var failed struct{ Runs []apijson.Run }
	if call("/api/v1/runs?status=failed", &failed); len(failed.Runs) != 1 || failed.Runs[0].ID != ids[4] {
		t.Errorf("runs?status=failed: %+v", failed)
	}
	var run struct {
		Run    apijson.Run
		LogURL string `json:"log_url"`
	}
	if code := call("/api/v1/runs/"+ids[4], &run); code != http.StatusOK || run.Run.Status != "failed" || run.LogURL != "/runs/"+ids[4]+"/log" {
		t.Errorf("run: %d %+v", code, run)
	}

	for path, want := range map[string]int{
		"/api/v1/jobs/nope":            http.StatusNotFound,
		"/api/v1/runs/nope":            http.StatusNotFound,
		"/api/v1/nothing":              http.StatusNotFound,
		"/api/v1/runs?job=nope":        http.StatusBadRequest,
		"/api/v1/runs?status=weird":    http.StatusBadRequest,
		"/api/v1/runs?limit=0":         http.StatusBadRequest,
		"/api/v1/runs?limit=501":       http.StatusBadRequest,
		"/api/v1/jobs?tag=Not%20A+Tag": http.StatusBadRequest,
	} {
		var e struct{ Error string }
		if code := call(path, &e); code != want || e.Error == "" {
			t.Errorf("%s: %d %+v, want %d with an error", path, code, e, want)
		}
	}

	// The API is the dashboard's: read-only, and loopback-only by Host.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", nil)
	req.Host = "localhost:8765"
	rec := httptest.NewRecorder()
	server.Router.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	req.Host = "evil.example"
	rec = httptest.NewRecorder()
	server.Router.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Error("a non-loopback Host was served")
	}
}
