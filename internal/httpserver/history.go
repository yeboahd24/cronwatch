package httpserver

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/yeboahd24/cronwatch/internal/model"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

// historyFilter is the filter of the Runs and Logs pages, from the URL.
type historyFilter struct {
	// As in the URL, to fill in the form and build links.
	Job, Status, From, To string
	Query                 string // Logs page only
	OnlyErrors            bool   // Logs page only
	before                string

	Jobs     []model.Job // for the job menu
	Statuses []string
	filter   storage.RunFilter
	cursor   *storage.RunCursor
}

// Active reports whether anything narrows the list.
func (f historyFilter) Active() bool {
	return f.Job != "" || f.Status != "" || f.From != "" || f.To != "" || f.Query != "" || f.OnlyErrors
}

// Paged reports whether this is a page after the first.
func (f historyFilter) Paged() bool { return f.cursor != nil }

// parseHistoryFilter reads the filter from the request. Dates are days in
// local time, both inclusive.
func (s *Server) parseHistoryFilter(r *http.Request) (historyFilter, error) {
	q := r.URL.Query()
	f := historyFilter{Job: q.Get("job"), Status: q.Get("status"), From: q.Get("from"), To: q.Get("to"),
		Query: strings.TrimSpace(q.Get("q")), OnlyErrors: q.Get("errors") == "1", before: q.Get("before"), Statuses: model.RunStatuses}
	jobs, err := s.Store.ListJobs(r.Context())
	if err != nil {
		return f, err
	}
	f.Jobs = jobs
	if f.Job != "" {
		i := slices.IndexFunc(jobs, func(j model.Job) bool { return j.Slug == f.Job })
		if i < 0 {
			return f, badRequest(fmt.Sprintf("no job with slug %q", f.Job))
		}
		f.filter.JobID = jobs[i].ID
	}
	if f.Status != "" {
		if !slices.Contains(model.RunStatuses, f.Status) {
			return f, badRequest(fmt.Sprintf("unknown status %q", f.Status))
		}
		f.filter.Status = f.Status
	}
	for _, d := range []struct {
		value string
		dst   *time.Time
		next  bool // the day after, for an inclusive end
	}{{f.From, &f.filter.Since, false}, {f.To, &f.filter.Until, true}} {
		if d.value == "" {
			continue
		}
		day, err := time.ParseInLocation("2006-01-02", d.value, time.Local)
		if err != nil {
			return f, badRequest(fmt.Sprintf("%q is not a date like 2026-10-06", d.value))
		}
		if d.next {
			day = day.AddDate(0, 0, 1)
		}
		*d.dst = day
	}
	if f.before != "" {
		at, id, ok := strings.Cut(f.before, "_")
		t, err := time.Parse(time.RFC3339Nano, at)
		if !ok || err != nil || id == "" {
			return f, badRequest("the page position in the link is not valid")
		}
		f.cursor = &storage.RunCursor{StartedAt: t, ID: id}
	}
	return f, nil
}

// link returns path with the filter's query, starting after cursor if it is
// not nil.
func (f historyFilter) link(path string, cursor *storage.RunCursor) string {
	v := url.Values{}
	for _, p := range [][2]string{{"q", f.Query}, {"job", f.Job}, {"status", f.Status}, {"from", f.From}, {"to", f.To}} {
		if p[1] != "" {
			v.Set(p[0], p[1])
		}
	}
	if f.OnlyErrors {
		v.Set("errors", "1")
	}
	if cursor != nil {
		v.Set("before", cursor.StartedAt.UTC().Format(time.RFC3339Nano)+"_"+cursor.ID)
	}
	if len(v) == 0 {
		return path
	}
	return path + "?" + v.Encode()
}

// pager links to the next older page and back to the newest one; "" means
// there is no such page.
type pager struct {
	Older, Newest string
}

// page trims runs, fetched with one extra, to limit and builds the links.
func (f historyFilter) page(path string, runs []storage.RunWithJob, limit int) ([]storage.RunWithJob, pager) {
	var p pager
	if len(runs) > limit {
		runs = runs[:limit]
		p.Older = f.link(path, storage.After(runs[len(runs)-1]))
	}
	if f.cursor != nil {
		p.Newest = f.link(path, nil)
	}
	return runs, p
}

// errBadRequest is a request error shown to the user as is.
type errBadRequest string

func (e errBadRequest) Error() string { return string(e) }

func badRequest(msg string) error { return errBadRequest(msg) }

// filterError reports err from parseHistoryFilter.
func filterError(w http.ResponseWriter, err error) {
	if msg, ok := errors.AsType[errBadRequest](err); ok {
		http.Error(w, string(msg), http.StatusBadRequest)
		return
	}
	queryError(w, err)
}
