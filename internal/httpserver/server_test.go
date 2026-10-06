package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/cronwatch/internal/model"
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

func TestRunPageShowsOutcomeDetails(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	job, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "outcome", Name: "Outcome", Command: `"true"`})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.CreateRun(ctx, job.ID, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateRun(ctx, job.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	zero := 0
	if err := s.CompleteRun(ctx, second.ID, storage.Completion{Ended: time.Now(), Status: "failed", ExitCode: &zero,
		Stdout: "ERROR: lost\n", Reason: `output matched --fail-if-match "ERROR": ERROR: lost`,
		Usage: &model.Usage{MaxRSSKB: 43008, UserCPUMS: 1500, SysCPUMS: 20}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunOverlap(ctx, second.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	server, err := New(s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for path, wants := range map[string][]string{
		"/runs/" + second.ID: {`<span class="error-label">Failed</span><span>output matched --fail-if-match &#34;ERROR&#34;: ERROR: lost</span>`,
			"<dd>42 MB</dd>", "1.5s user, 20ms system", `<a href="/runs/` + first.ID + `">an earlier run</a> was still running`},
		"/jobs/" + job.ID: {`<td data-label="Peak memory">42 MB</td>`},
	} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "localhost:8765"
		server.Router.ServeHTTP(recorder, req)
		for _, want := range wants {
			if !strings.Contains(recorder.Body.String(), want) {
				t.Fatalf("%s lacks %q:\n%s", path, want, recorder.Body.String())
			}
		}
	}
}

func TestTimeline(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	expr, grace := "0 * * * *", time.Duration(0)
	job, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "hourly", Name: "Hourly", Command: `"true"`, Schedule: &expr, Grace: &grace})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	one := 1
	for i, status := range []string{"success", "failed", "skipped"} {
		run, err := s.CreateRun(ctx, job.ID, now.Add(time.Duration(i-3)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteRun(ctx, run.ID, storage.Completion{Ended: now.Add(time.Duration(i-3)*time.Hour + time.Minute), Duration: time.Minute, Status: status, ExitCode: &one}); err != nil {
			t.Fatal(err)
		}
	}
	// A run from before the 24-hour window is only on the 7-day view.
	old, err := s.CreateRun(ctx, job.ID, now.Add(-48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, old.ID, now.Add(-48*time.Hour), 0, "success", nil, "", "", "", false); err != nil {
		t.Fatal(err)
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
	day := get("/timeline")
	for _, want := range []string{`class="tl-ok"`, `class="tl-fail" x=`, `class="tl-minor"`, "<title>Failed · ", " · 1m · exit 1</title>",
		`aria-label="Hourly: 1 success, 1 failed, 1 skipped"`, `class="tl-expected"`, `aria-current="page">24 hours`} {
		if !strings.Contains(day, want) {
			t.Fatalf("24h timeline lacks %q:\n%s", want, day)
		}
	}
	if week := get("/timeline?range=7d"); !strings.Contains(week, `aria-label="Hourly: 2 success, 1 failed, 1 skipped"`) {
		t.Fatalf("7d timeline:\n%s", week)
	}
	if bad := get("/timeline?range=nonsense"); !strings.Contains(bad, `aria-current="page">24 hours`) {
		t.Fatal("unknown range did not fall back to 24 hours")
	}
}

func TestRunPageComparesWithLastSuccess(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	job, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "backup", Name: "Backup", Command: `"true"`})
	if err != nil {
		t.Fatal(err)
	}
	finish := func(started time.Time, status string, code int, combined string, d time.Duration) model.Run {
		run, err := s.CreateRun(ctx, job.ID, started)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteRun(ctx, run.ID, storage.Completion{Ended: started.Add(d), Duration: d, Status: status, ExitCode: &code, Combined: combined}); err != nil {
			t.Fatal(err)
		}
		return run
	}
	now := time.Now()
	success := finish(now.Add(-48*time.Hour), "success", 0, "[Sat 02:00] Starting backup\nDump complete. Size: 1.7G\nUploaded to api\n", 42*time.Second)
	failed := finish(now.Add(-24*time.Hour), "failed", 255, "[Sun 02:00] Starting backup\nDump complete. Size: 1.8G\n\x02ssh: connect to host api port 22: Connection timed out\n", 9*time.Minute)
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
	if body := get("/runs/" + failed.ID); !strings.Contains(body, `href="?stream=compare"`) {
		t.Fatal("failed run page has no comparison tab")
	}
	if body := get("/runs/" + success.ID); strings.Contains(body, "stream=compare") {
		t.Fatal("successful run page offers a comparison")
	}
	body := get("/runs/" + failed.ID + "?stream=compare")
	for _, want := range []string{`<a href="/runs/` + success.ID + `">last successful run</a>`,
		"<dd>255, the last success exited 0</dd>", "<dd>9m, the last success took 42s</dd>",
		"New in this run <span class=\"muted\">(1)", "is-stderr", "ssh: connect to host api port 22: Connection timed out",
		"Missing from this run <span class=\"muted\">(1)", "Uploaded to api"} {
		if !strings.Contains(body, want) {
			t.Fatalf("comparison lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Starting backup") || strings.Contains(body, "Dump complete") {
		t.Fatal("lines that differ only in dates or numbers were reported")
	}
}

func TestFailureHistoryOnRunAndJobPages(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	job, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "backup", Name: "Backup", Command: `"true"`})
	if err != nil {
		t.Fatal(err)
	}
	one := 1
	var ids []string
	for i, stderr := range []string{"[Fri] ssh: connect to host api port 22: Connection timed out\n", "[Sat] ssh: connect to host api port 22: Connection timed out\n", "pg_dump: command not found\n", ""} {
		run, err := s.CreateRun(ctx, job.ID, time.Now().Add(time.Duration(i-4)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteRun(ctx, run.ID, storage.Completion{Ended: run.StartedAt, Status: "failed", ExitCode: &one, Stderr: stderr}); err != nil {
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
	for path, want := range map[string]string{
		"/runs/" + ids[0]: `<span class="badge badge-new">New error</span> The first time this job failed this way.`,
		"/runs/" + ids[1]: `<span class="badge">Seen before</span> Same error as 1 earlier run, first seen`,
		"/runs/" + ids[2]: `New error`,
	} {
		if body := get(path); !strings.Contains(body, want) || !strings.Contains(body, `href="/jobs/`+job.ID+`#failures"`) {
			t.Fatalf("%s lacks %q", path, want)
		}
	}
	page := get("/jobs/" + job.ID)
	for _, want := range []string{`<h2 id="failures">Failure types</h2>`,
		`<code>Exited 1 with no output</code>`, `<code>pg_dump: command not found</code>`,
		`<code>[Sat] ssh: connect to host api port 22: Connection timed out</code></td>
      <td data-label="Runs">2</td>`} {
		if !strings.Contains(page, want) {
			t.Fatalf("job page lacks %q:\n%s", want, page)
		}
	}
}

func TestSlowRunsAndDrift(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	job, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "export", Name: "Export", Command: `"true"`})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	day := 24 * time.Hour
	zero := 0
	add := func(started time.Time, d time.Duration) model.Run {
		run, err := s.CreateRun(ctx, job.ID, started)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteRun(ctx, run.ID, storage.Completion{Ended: started.Add(d), Duration: d, Status: "success", ExitCode: &zero}); err != nil {
			t.Fatal(err)
		}
		return run
	}
	for i := 37; i >= 8; i-- {
		add(now.Add(-time.Duration(i)*day), 2*time.Minute)
	}
	for i := 7; i >= 1; i-- {
		add(now.Add(-time.Duration(i)*day), 3*time.Minute)
	}
	slow := add(now.Add(-time.Hour), 12*time.Minute)
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
	slowBadge := `<span class="badge badge-slow" title="6.0× the usual 2m">slow</span>`
	for path, wants := range map[string][]string{
		"/":                {slowBadge, `<svg class="spark" role="img" aria-label="Export: durations of the last 30 runs, longest 12m, usually 2m, 1 unusually slow">`},
		"/jobs/" + job.ID:  {slowBadge, "<h2>Duration</h2>", "Getting slower: 3m over the last 7 days, up 50% from 2m over the 30 days before.", `class="spark-slow"`},
		"/runs/" + slow.ID: {`<span class="text-warning">· 6.0× the usual 2m</span>`},
	} {
		body := get(path)
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Fatalf("%s lacks %q:\n%s", path, want, body)
			}
		}
	}
}

func TestCrontabHistoryOnJobPageAndTimeline(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	expr := "0 3 * * *"
	job, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "backup", Name: "Backup", Command: `"true"`, Schedule: &expr})
	if err != nil {
		t.Fatal(err)
	}
	changed := time.Now().Add(-2 * time.Hour)
	if err := s.RecordCrontabSnapshot(ctx, storage.CrontabSnapshot{TakenAt: changed, Hash: "h2", Content: "x"}, []storage.CrontabChange{
		{JobSlug: "backup", Kind: "schedule", Before: "0 2 * * *", After: "0 3 * * *"},
		{Kind: "line_added", After: "MAILTO=me@example.com"},
	}); err != nil {
		t.Fatal(err)
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
	page := get("/jobs/" + job.ID)
	if !strings.Contains(page, `<h2 id="crontab-history">Crontab history</h2>`) ||
		!strings.Contains(page, "Schedule changed from <code>0 2 * * *</code> to <code>0 3 * * *</code>") ||
		strings.Contains(page, "MAILTO") {
		t.Fatalf("job page:\n%s", page)
	}
	timeline := get("/timeline")
	if !strings.Contains(timeline, `<a href="/jobs/`+job.ID+`#crontab-history"><title>Crontab changed · `) ||
		!strings.Contains(timeline, " · schedule 0 2 * * * → 0 3 * * *</title>") {
		t.Fatalf("timeline lacks the change marker:\n%s", timeline)
	}
}

func TestRunLogDownload(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	job, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "backup", Name: "Backup", Command: `"true"`})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateRun(ctx, job.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, run.ID, time.Now(), time.Second, "failed", nil, "out\n", "boom\n", "out\n\x02boom\n", false); err != nil {
		t.Fatal(err)
	}
	server, err := New(s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for query, want := range map[string]string{"": "out\nboom\n", "?stream=stdout": "out\n", "?stream=stderr": "boom\n"} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/log"+query, nil)
		req.Host = "localhost:8765"
		server.Router.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK || recorder.Body.String() != want {
			t.Fatalf("%q: %d %q", query, recorder.Code, recorder.Body.String())
		}
		disposition := recorder.Header().Get("Content-Disposition")
		if !strings.HasPrefix(disposition, `attachment; filename="backup-`) || !strings.HasSuffix(disposition, `.log"`) {
			t.Fatalf("Content-Disposition = %q", disposition)
		}
		if recorder.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Fatalf("Content-Type = %q", recorder.Header().Get("Content-Type"))
		}
	}
}

func TestMetrics(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	expr := "0 2 * * *"
	job, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "backup", Name: `DB "main" backup`, Command: `"true"`, Schedule: &expr})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "never", Name: "Never", Command: `"true"`}); err != nil {
		t.Fatal(err)
	}
	started := time.Unix(1_760_000_000, 0)
	two := 2
	run, err := s.CreateRun(ctx, job.ID, started)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRun(ctx, run.ID, storage.Completion{Ended: started.Add(1500 * time.Millisecond), Duration: 1500 * time.Millisecond, Status: "failed", ExitCode: &two}); err != nil {
		t.Fatal(err)
	}
	get := func(opts Options) *httptest.ResponseRecorder {
		server, err := New(s, opts)
		if err != nil {
			t.Fatal(err)
		}
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.Host = "localhost:8765"
		server.Router.ServeHTTP(recorder, req)
		return recorder
	}
	if rec := get(Options{}); rec.Code != http.StatusNotFound {
		t.Fatalf("/metrics without --metrics: %d", rec.Code)
	}
	rec := get(Options{Metrics: true})
	body := rec.Body.String()
	for _, want := range []string{
		"# TYPE cronwatch_job_failing gauge\n",
		`cronwatch_job_info{job="backup",name="DB \"main\" backup",schedule="0 2 * * *",status="failed"} 1`,
		`cronwatch_job_failing{job="backup"} 1`, `cronwatch_job_failing{job="never"} 0`,
		`cronwatch_job_last_run_timestamp_seconds{job="backup"} 1760000000`,
		`cronwatch_job_last_run_duration_seconds{job="backup"} 1.5`,
		`cronwatch_job_last_run_exit_code{job="backup"} 2`,
		`cronwatch_job_next_expected_timestamp_seconds{job="backup"} `,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics lack %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `cronwatch_job_last_success_timestamp_seconds{`) || strings.Contains(body, `last_run_timestamp_seconds{job="never"}`) {
		t.Fatalf("unknown values were reported:\n%s", body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Fatalf("Content-Type = %q", ct)
	}
}

func TestAlertDeliveryIsShown(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	hook := "mail-me"
	job, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "backup", Name: "Backup", Command: `"true"`, OnFailure: &hook})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateRun(ctx, job.ID, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	code := 1
	if err := s.CompleteRun(ctx, run.ID, storage.Completion{Ended: time.Now(), Status: "failed", ExitCode: &code}); err != nil {
		t.Fatal(err)
	}
	if err := s.QueueAlert(ctx, storage.NewAlert{JobID: job.ID, RunID: run.ID, Event: "failed", Hook: "on_failure", Created: time.Now()}); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingAlerts(ctx, job.ID)
	if err != nil || len(pending) != 1 || pending[0].Command != hook {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	now := time.Now()
	if ok, err := s.ClaimAlert(ctx, pending[0].ID, now, now.Add(time.Minute)); !ok || err != nil {
		t.Fatalf("claim = %v, %v", ok, err)
	}
	retry := now.Add(time.Minute)
	if err := s.FinishAlertAttempt(ctx, pending[0].ID, storage.AlertPending, &retry, "exit status 1", "smtp: <refused>"); err != nil {
		t.Fatal(err)
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
	for path, want := range map[string][]string{
		"/":                   {`href="/jobs/` + job.ID + `#alerts">1 alert not delivered</a>`},
		"/jobs/" + job.ID:     {`<h2 id="alerts">Alerts</h2>`, `<span class="text-warning">Retrying</span> after 1 failed attempt, next`, `<code>exit status 1</code>`, `<pre>smtp: &lt;refused&gt;</pre>`},
		"/runs/" + run.ID:     {`<dt>Failure alert</dt><dd><span class="text-warning">Retrying</span>`},
		"/partials/dashboard": {"1 alert not delivered"},
	} {
		page := get(path)
		for _, w := range want {
			if !strings.Contains(page, w) {
				t.Fatalf("%s lacks %q:\n%s", path, w, page)
			}
		}
	}
}

func TestPausedAndArchivedJobs(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	expr := "0 3 * * *"
	paused, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "paused", Name: "Paused job", Command: `"true"`, Schedule: &expr})
	if err != nil {
		t.Fatal(err)
	}
	archived, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "old", Name: "Old job", Command: `"true"`})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	until := now.Add(2 * time.Hour)
	if err := s.PauseJob(ctx, paused, now, &until); err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveJob(ctx, archived, now); err != nil {
		t.Fatal(err)
	}
	server, err := New(s, Options{Metrics: true})
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
	index := get("/")
	if !strings.Contains(index, `<span class="status status-paused">Paused</span><small>until <time`) ||
		!strings.Contains(index, `<p class="archived-jobs">Archived: <a href="/jobs/`+archived.ID+`">Old job</a></p>`) ||
		strings.Contains(index, `<td data-label="Job" class="cell-job"><a href="/jobs/`+archived.ID) {
		t.Fatalf("index:\n%s", index)
	}
	if page := get("/jobs/" + paused.ID); !strings.Contains(page, `<span class="error-label">Paused</span>`) || !strings.Contains(page, "<code>cronwatch resume paused</code>") {
		t.Fatalf("paused job page:\n%s", page)
	}
	if page := get("/jobs/" + archived.ID); !strings.Contains(page, `<span class="error-label">Archived</span>`) {
		t.Fatalf("archived job page:\n%s", page)
	}
	if metrics := get("/metrics"); strings.Contains(metrics, `job="old"`) || !strings.Contains(metrics, `status="paused"`) {
		t.Fatalf("metrics:\n%s", metrics)
	}
}
