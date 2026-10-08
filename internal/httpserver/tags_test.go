package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yeboahd24/cronwatch/internal/storage"
)

func TestDashboardFiltersByTag(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, j := range []struct {
		slug, name string
		tags       []string
	}{{"backup", "Database Backup", []string{"backup", "db"}}, {"web", "Web Cache Warm", []string{"web"}}, {"plain", "Plain Job", nil}} {
		tags := j.tags
		if _, err := s.UpsertJob(ctx, storage.JobSpec{Slug: j.slug, Name: j.name, Command: `"true"`, Tags: &tags}); err != nil {
			t.Fatal(err)
		}
	}
	server, err := New(s, Options{})
	if err != nil {
		t.Fatal(err)
	}

	all := get(t, server, "/")
	for _, want := range []string{"Database Backup", "Web Cache Warm", "Plain Job", `href="/?tag=db"`, `aria-label="Filter jobs by tag"`} {
		if !strings.Contains(all, want) {
			t.Errorf("unfiltered page lacks %q", want)
		}
	}
	db := get(t, server, "/?tag=db")
	if !strings.Contains(db, "Database Backup") || strings.Contains(db, "Web Cache Warm") || strings.Contains(db, "Plain Job") {
		t.Error("/?tag=db shows the wrong jobs")
	}
	if !strings.Contains(db, `<a class="tag" href="/?tag=db" aria-current="page">db</a>`) {
		t.Error("/?tag=db does not mark the db tag as current")
	}
	// The live refresh keeps the filter.
	if partial := get(t, server, "/partials/dashboard?tag=web"); !strings.Contains(partial, "Web Cache Warm") || strings.Contains(partial, "Database Backup") {
		t.Error("the partial ignores the tag filter")
	}
	if none := get(t, server, "/?tag=db&tag=web"); !strings.Contains(none, "No jobs have all of these tags") {
		t.Error("an empty filtered list does not say why")
	}

	req := httptest.NewRequest(http.MethodGet, "/?tag=%3Cscript%3E", nil)
	req.Host = "localhost:8765"
	rec := httptest.NewRecorder()
	server.Router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "<script>") && !strings.Contains(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("bad tag: %d %q", rec.Code, rec.Body.String())
	}
}
