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
	job, err := s.UpsertJob(ctx, "xss", "Example", "true", "", 0)
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
	server, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/healthz", "/", "/jobs/" + job.ID, "/runs/" + run.ID, "/runs", "/static/app.css"} {
		recorder := httptest.NewRecorder()
		server.Router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
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
