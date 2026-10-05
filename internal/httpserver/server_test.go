package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/cronwatch/internal/runenv"
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
	for _, path := range []string{"/healthz", "/", "/jobs/" + job.ID, "/runs/" + run.ID, "/runs", "/logs", "/logs?q=script&errors=1", "/partials/dashboard", "/static/app.css", "/static/favicon.svg"} {
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

// get serves path from a server over a fresh store and returns the body.
func get(t *testing.T, server *Server, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = "localhost:8765"
	recorder := httptest.NewRecorder()
	server.Router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("%s: status %d", path, recorder.Code)
	}
	return recorder.Body.String()
}

func TestDashboardAndLogPages(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	expr, grace := "", time.Duration(0)
	failing, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "reports", Name: "Generate Reports", Command: `"sh" "-c" "exit 2"`, Schedule: &expr, Grace: &grace})
	if err != nil {
		t.Fatal(err)
	}
	healthy, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "backup", Name: "Database Backup", Command: `"backup.sh"`, Schedule: &expr, Grace: &grace})
	if err != nil {
		t.Fatal(err)
	}
	two, zero := 2, 0
	failRun, err := s.CreateRun(ctx, failing.ID, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	combined := "loading config\n\x02report job failed: connection refused\n"
	if err := s.FinishRun(ctx, failRun.ID, time.Now(), time.Second, "failed", &two, "loading config\n", "report job failed: connection refused\n", combined, false); err != nil {
		t.Fatal(err)
	}
	okRun, err := s.CreateRun(ctx, healthy.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, okRun.ID, time.Now(), time.Second, "success", &zero, "done\n", "", "done\n", false); err != nil {
		t.Fatal(err)
	}
	server, err := New(s, Options{})
	if err != nil {
		t.Fatal(err)
	}

	// The failing job's run is featured even though a newer run succeeded.
	index := get(t, server, "/")
	for _, want := range []string{">Failed<", ">Success<", `id="recent-logs"`, "Generate Reports · ", `class="log-line is-stderr"`, "report job failed: connection refused"} {
		if !strings.Contains(index, want) {
			t.Fatalf("dashboard missing %q", want)
		}
	}

	// The live partial must keep its tbody inside a table, or DOMParser drops it.
	partial := get(t, server, "/partials/dashboard")
	if !strings.HasPrefix(partial, `<table><tbody id="jobs-body">`) || !strings.Contains(partial, `id="recent-logs"`) {
		t.Fatalf("partial = %.120s", partial)
	}

	run := get(t, server, "/runs/"+failRun.ID)
	if !strings.Contains(run, "Last error") || !strings.Contains(run, "report job failed") {
		t.Fatal("run page missing last error")
	}
	if stdout := get(t, server, "/runs/"+failRun.ID+"?stream=stdout"); strings.Contains(stdout, "is-stderr") || !strings.Contains(stdout, "loading config") {
		t.Fatal("stdout stream shows stderr lines")
	}
	if job := get(t, server, "/jobs/"+failing.ID); !strings.Contains(job, "sh -c &#39;exit 2&#39;") {
		t.Fatal("job page command not shell-quoted")
	}

	logsPage := get(t, server, "/logs?q=REFUSED")
	if !strings.Contains(logsPage, "connection refused") || strings.Contains(logsPage, "Database Backup") {
		t.Fatal("log search returned wrong runs")
	}
	if errorsOnly := get(t, server, "/logs?errors=1"); strings.Contains(errorsOnly, "loading config") || !strings.Contains(errorsOnly, "connection refused") {
		t.Fatal("errors-only filter kept stdout lines")
	}
}

func TestRunPageShowsEnvironmentAndChangeSinceSuccess(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	job, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "env", Name: "Env", Command: `"true"`})
	if err != nil {
		t.Fatal(err)
	}
	shell := runenv.Env{Dir: "/home/a/project", User: "a", Vars: map[string]string{"PATH": "/opt/tool/bin:/usr/bin"}, Names: []string{"DISPLAY", "NVM_DIR", "PATH"}}
	cron := runenv.Env{Dir: "/home/a", User: "a", Vars: map[string]string{"PATH": "/usr/bin"}, Names: []string{"PATH", "SECRET_TOKEN"}}
	var ids []string
	for i, tc := range []struct {
		env    runenv.Env
		status string
	}{{shell, "success"}, {cron, "failed"}} {
		run, err := s.CreateRun(ctx, job.ID, time.Now().Add(time.Duration(i-2)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetRunEnv(ctx, run.ID, tc.env); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishRun(ctx, run.ID, time.Now(), time.Second, tc.status, nil, "", "", "", false); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, run.ID)
	}
	server, err := New(s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) string {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "localhost:8765"
		server.Router.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, recorder.Code)
		}
		return recorder.Body.String()
	}
	failed := get("/runs/" + ids[1])
	for _, want := range []string{"Environment changed since the", `href="/runs/` + ids[0] + `"`,
		"missing <code>/opt/tool/bin</code>", "<code>/home/a/project</code> → <code>/home/a</code>",
		"Not set in this run</span> <code>NVM_DIR</code>", "Set only in this run</span> <code>SECRET_TOKEN</code>",
		"<h2>Environment</h2>", "1 set; values not recorded"} {
		if !strings.Contains(failed, want) {
			t.Fatalf("failed run page lacks %q:\n%s", want, failed)
		}
	}
	if strings.Contains(failed, "DISPLAY") {
		t.Fatal("session variable listed in the change notice")
	}
	success := get("/runs/" + ids[0])
	if strings.Contains(success, "Environment changed") || !strings.Contains(success, "<code>/opt/tool/bin:/usr/bin</code>") {
		t.Fatalf("success run page:\n%s", success)
	}
}
