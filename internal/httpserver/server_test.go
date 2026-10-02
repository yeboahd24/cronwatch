package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/cronwatch/internal/storage"
)

func TestPagesEscapeLogsAndSetSecurityHeaders(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	expr, grace := "", time.Duration(0)
	job, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "xss", Name: "Example", Command: "true", Schedule: &expr, Grace: &grace})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateRun(ctx, job.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	zero := 0
	if err := s.FinishRun(ctx, run.ID, time.Now(), time.Millisecond, "success", &zero, "<script>alert(1)</script>", "", "<script>alert(1)</script>", false); err != nil {
		t.Fatal(err)
	}
	server, err := New(s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/healthz", "/", "/jobs/" + job.ID, "/runs/" + run.ID, "/runs", "/static/app.css"} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "localhost:8765"
		server.Router.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, recorder.Code)
		}
		if recorder.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("%s: missing CSP", path)
		}
		if path == "/runs/"+run.ID {
			body := recorder.Body.String()
			if strings.Contains(body, "<script>alert(1)</script>") || !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
				t.Fatalf("log not escaped: %s", body)
			}
		}
	}
}

func TestRejectsNonLoopbackHost(t *testing.T) {
	s, err := storage.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, tc := range []struct {
		host    string
		anyHost bool
		want    int
	}{
		{"localhost:8765", false, http.StatusOK},
		{"127.0.0.1:9000", false, http.StatusOK},
		{"[::1]:8765", false, http.StatusOK},
		{"evil.example:8765", false, http.StatusForbidden},
		{"evil.example:8765", true, http.StatusOK},
	} {
		server, err := New(s, Options{AnyHost: tc.anyHost})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Host = tc.host
		recorder := httptest.NewRecorder()
		server.Router.ServeHTTP(recorder, req)
		if recorder.Code != tc.want {
			t.Fatalf("%s (anyHost=%v): status %d", tc.host, tc.anyHost, recorder.Code)
		}
	}
}
