package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/cronwatch/internal/hub"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

func TestServersPages(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	local, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "local-backup", Name: "Local backup", Command: `"true"`})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	report := func(host string, at time.Time, jobs ...hub.Job) {
		t.Helper()
		if _, err := s.CreateHost(ctx, host, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		if jobs == nil {
			return
		}
		h, err := s.HostByName(ctx, host)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(hub.Report{Version: hub.ReportVersion, Cronwatch: "v0.10.0", Hostname: host + ".internal", Jobs: jobs})
		if err := s.SaveHostReport(ctx, h.ID, at, body); err != nil {
			t.Fatal(err)
		}
	}
	report("web-1", now.Add(-30*time.Second),
		hub.Job{Slug: "ok-job", Name: "OK job", Status: "success"},
		hub.Job{Slug: "db", Name: "DB dump", Status: "failed", LastRun: &hub.Run{Status: "failed", LastError: "pg_dump: connection refused"}, UndeliveredAlerts: 2})
	report("web-2", now.Add(-20*time.Minute), hub.Job{Slug: "old", Name: "Old news", Status: "success"})
	report("web-3", time.Time{})
	server, err := New(s, Options{Version: "v0.10.0"})
	if err != nil {
		t.Fatal(err)
	}

	code, page := fetch(t, server, "/servers")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	for _, want := range []string{
		`<a href="/servers/this%20server">this server</a>`,
		`<span class="status status-failed">1 problem</span>`,
		`<tr class="is-stale">`, `<span class="status status-missed">Stale</span><small>no report for 20m</small>`,
		`<span class="status status-never_run">Never reported</span>`,
		"<code>pg_dump: connection refused</code>", "2 alerts not delivered",
		`<small class="text-warning">as of 20m ago</small>`,
		`<a href="/jobs/` + local.ID + `">Local backup</a>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("servers page lacks %q", want)
		}
	}
	// Jobs needing attention come first.
	if strings.Index(page, "DB dump") > strings.Index(page, "Local backup") {
		t.Error("a failing job is not listed first")
	}
	if _, page := fetch(t, server, "/servers/web-2"); !strings.Contains(page, `<span class="error-label">Stale</span><span>No report for 20m`) || !strings.Contains(page, "Old news") || strings.Contains(page, "DB dump") {
		t.Errorf("stale host page:\n%s", page)
	}
	if _, page := fetch(t, server, "/servers/web-3"); !strings.Contains(page, "Never reported") {
		t.Error("never-reported host page")
	}
	if _, page := fetch(t, server, "/servers/this%20server"); !strings.Contains(page, "Live: this is the server you are viewing") || !strings.Contains(page, "Local backup") {
		t.Error("this server's page")
	}
	if code, _ := fetch(t, server, "/servers/nope"); code != http.StatusNotFound {
		t.Errorf("unknown server: %d", code)
	}
}
