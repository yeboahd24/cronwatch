package httpserver

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/cronwatch/internal/model"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

var (
	runLink   = regexp.MustCompile(`<td data-label="Started"><a href="/runs/([0-9a-f]+)">`)
	olderLink = regexp.MustCompile(`<a class="pager-older" href="([^"]+)">`)
	newestRef = regexp.MustCompile(`<a href="([^"]+)"><span aria-hidden="true">←</span> Newest</a>`)
)

// historyServer records runs of two jobs, three a day for ten days at 01:00,
// 02:00 and 03:00 local time, the third failed and with output "marker".
// Runs at 03:00 of A and B start at the same instant.
// It returns the IDs of the runs it recorded.
func historyServer(t *testing.T) (*Server, *storage.Store, map[string]model.Job, []string) {
	t.Helper()
	ctx := context.Background()
	s, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	jobs := map[string]model.Job{}
	for _, slug := range []string{"a", "b"} {
		j, err := s.UpsertJob(ctx, storage.JobSpec{Slug: slug, Name: "Job " + strings.ToUpper(slug), Command: `"true"`})
		if err != nil {
			t.Fatal(err)
		}
		jobs[slug] = j
	}
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local)
	var ids []string
	for d := range 10 {
		for _, h := range []int{1, 2, 3} {
			started := day.AddDate(0, 0, d).Add(time.Duration(h) * time.Hour)
			for _, slug := range []string{"a", "b"} {
				if h != 3 && slug == "b" {
					continue
				}
				run, err := s.CreateRun(ctx, jobs[slug].ID, started)
				if err != nil {
					t.Fatal(err)
				}
				status, code, out := "success", 0, fmt.Sprintf("day %d\n", d)
				if h == 3 {
					status, code, out = "failed", 1, "marker\n"
				}
				if err := s.FinishRun(ctx, run.ID, started.Add(time.Second), time.Second, status, &code, out, "", out, false); err != nil {
					t.Fatal(err)
				}
				ids = append(ids, run.ID)
			}
		}
	}
	server, err := New(s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return server, s, jobs, ids
}

func fetch(t *testing.T, server *Server, path string) (int, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = "localhost:8765"
	server.Router.ServeHTTP(recorder, req)
	return recorder.Code, recorder.Body.String()
}

// walk follows Older links from path, returning the run IDs of every page
// and the number of pages.
func walk(t *testing.T, server *Server, path string) ([]string, int) {
	t.Helper()
	var ids []string
	pages := 0
	for path != "" {
		code, body := fetch(t, server, path)
		if code != http.StatusOK {
			t.Fatalf("%s: status %d", path, code)
		}
		pages++
		for _, m := range runLink.FindAllStringSubmatch(body, -1) {
			ids = append(ids, m[1])
		}
		if pages > 1 && !newestRef.MatchString(body) {
			t.Fatalf("page %d of %s has no Newest link", pages, path)
		}
		path = ""
		if m := olderLink.FindStringSubmatch(body); m != nil {
			path = html.UnescapeString(m[1])
		}
	}
	return ids, pages
}

func TestRunsPagesAndFilters(t *testing.T) {
	server, s, jobs, created := historyServer(t)
	ctx := context.Background()
	// The order every page must follow: newest first, ties by ID.
	unpaged, err := s.ListRunsPage(ctx, storage.RunFilter{}, nil, -1)
	if err != nil {
		t.Fatal(err)
	}
	var all []string
	for _, r := range unpaged {
		all = append(all, r.Run.ID)
	}
	if !slices.Equal(slices.Sorted(slices.Values(all)), slices.Sorted(slices.Values(created))) {
		t.Fatal("the unpaged list is not the runs recorded")
	}
	ids, pages := walk(t, server, "/runs")
	if !slices.Equal(ids, all) || pages != 1 {
		t.Fatalf("runs = %d on %d pages, want %d on 1", len(ids), pages, len(all))
	}

	// Pages continue after the last run shown, ties included, so a run
	// recorded meanwhile neither shifts a page nor repeats a run.
	var paged []string
	var cursor *storage.RunCursor
	for {
		page, err := s.ListRunsPage(ctx, storage.RunFilter{}, cursor, 7)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, r := range page {
			paged = append(paged, r.Run.ID)
		}
		cursor = storage.After(page[len(page)-1])
		if len(paged) == 7 {
			if _, err := s.CreateRun(ctx, jobs["a"].ID, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !slices.Equal(paged, all) {
		t.Fatalf("paged %d runs, want the %d recorded first, each once", len(paged), len(all))
	}

	for query, want := range map[string]int{
		"job=b":                               10,
		"status=failed":                       20,
		"job=a&status=success":                20,
		"from=2026-09-03&to=2026-09-04":       8,
		"job=b&from=2026-09-10&to=2026-09-10": 1,
		"from=2026-10-01":                     1, // the run recorded above
	} {
		ids, _ := walk(t, server, "/runs?"+query)
		if len(ids) != want {
			t.Errorf("/runs?%s: %d runs, want %d", query, len(ids), want)
		}
	}
	_, body := fetch(t, server, "/runs?job=b&status=failed")
	for _, want := range []string{`<option value="b" selected>Job B</option>`, `<option value="failed" selected>Failed</option>`, `<a class="clear" href="/runs">Clear</a>`} {
		if !strings.Contains(body, want) {
			t.Errorf("filtered page lacks %q", want)
		}
	}
	for _, bad := range []string{"status=bogus", "job=nope", "from=yesterday", "before=garbage", "before=2026-09-01T00:00:00Z"} {
		if code, _ := fetch(t, server, "/runs?"+bad); code != http.StatusBadRequest {
			t.Errorf("/runs?%s: status %d, want 400", bad, code)
		}
	}
	if _, body := fetch(t, server, "/jobs/"+jobs["a"].ID); !strings.Contains(body, `href="/runs?job=a">Filter and browse all runs`) {
		t.Error("job page does not link to its runs")
	}
}

func TestRunsPagePagesBeyondTheLimit(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	job, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "busy", Name: "Busy", Command: `"true"`})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour)
	var all []string
	for i := range 250 {
		started := base.Add(time.Duration(i/2) * time.Second) // pairs share a start
		run, err := s.CreateRun(ctx, job.ID, started)
		if err != nil {
			t.Fatal(err)
		}
		out := fmt.Sprintf("line %d\n", i)
		if err := s.FinishRun(ctx, run.ID, started, 0, "success", new(0), out, "", out, false); err != nil {
			t.Fatal(err)
		}
		all = append(all, run.ID)
	}
	server, err := New(s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ids, pages := walk(t, server, "/runs?job=busy")
	if pages != 3 || len(ids) != 250 {
		t.Fatalf("%d runs on %d pages, want 250 on 3", len(ids), pages)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	if len(seen) != 250 {
		t.Fatalf("%d distinct runs, want 250", len(seen))
	}
	if _, body := fetch(t, server, "/runs?job=busy"); !strings.Contains(body, "job=busy") || newestRef.MatchString(body) {
		t.Fatal("the first page has a Newest link, or the Older link drops the filter")
	}

	// The Logs page searches 50 runs a page, keeping the search in its links.
	results, logPages := 0, 0
	for path := "/logs?q=LINE"; path != ""; logPages++ {
		_, body := fetch(t, server, path)
		results += strings.Count(body, `class="panel log-result"`)
		path = ""
		if m := olderLink.FindStringSubmatch(body); m != nil {
			path = html.UnescapeString(m[1])
			if !strings.Contains(path, "q=LINE") {
				t.Fatalf("older link %q drops the search", path)
			}
		}
	}
	if results != 250 || logPages != 5 {
		t.Fatalf("%d log results on %d pages, want 250 on 5", results, logPages)
	}
}

func TestLogsPagesAndFilters(t *testing.T) {
	server, _, _, _ := historyServer(t)
	code, body := fetch(t, server, "/logs?q=marker&job=a")
	if code != http.StatusOK || strings.Count(body, `class="panel log-result"`) != 10 {
		t.Fatalf("filtered search: status %d\n%s", code, body)
	}
	// Each page searches 50 runs; the rest are on later pages.
	_, body = fetch(t, server, "/logs")
	m := olderLink.FindStringSubmatch(body)
	if strings.Count(body, `class="panel log-result"`) != 40 || m != nil {
		t.Fatalf("unfiltered logs: %d results, older link %v", strings.Count(body, `class="panel log-result"`), m)
	}
	if code, _ := fetch(t, server, "/logs?status=nope"); code != http.StatusBadRequest {
		t.Fatalf("bad status: %d", code)
	}
}
