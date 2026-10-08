package httpserver

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/yeboahd24/cronwatch/internal/apijson"
	"github.com/yeboahd24/cronwatch/internal/model"
)

// The read-only JSON API, at /api/v1 on the dashboard. Jobs and runs have
// the fields of "cronwatch jobs --json" and "cronwatch runs --json".

// Limits of a page of runs from the API.
const (
	apiRunsDefault = 100
	apiRunsMax     = 500
)

// handleAPIJobs serves GET /api/v1/jobs: every unarchived job, or with
// ?all=1 every job, narrowed by ?tag=, which may repeat.
func (s *Server) handleAPIJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tags, err := model.Tags(q["tag"])
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	views, err := s.Store.ListJobViews(r.Context(), s.now())
	if err != nil {
		apiQueryError(w, err)
		return
	}
	if q.Get("all") != "1" {
		views = model.Unarchived(views)
	}
	views = model.WithTags(views, tags)
	out := make([]apijson.Job, 0, len(views))
	for _, v := range views {
		out = append(out, apijson.NewJob(v))
	}
	writeAPI(w, map[string]any{"jobs": out})
}

// handleAPIJob serves GET /api/v1/jobs/{slug}.
func (s *Server) handleAPIJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.Store.GetJobBySlug(r.Context(), chi.URLParam(r, "slug"))
	if err != nil {
		apiQueryError(w, err)
		return
	}
	view, err := s.Store.JobView(r.Context(), job, s.now())
	if err != nil {
		apiQueryError(w, err)
		return
	}
	writeAPI(w, map[string]any{"job": apijson.NewJob(view)})
}

// handleAPIRuns serves GET /api/v1/runs, newest first, filtered like the
// Runs page by ?job=SLUG, ?status=, and ?from= and ?to= days. ?limit= sets
// the page size; "next" is the URL of the next older page, or null.
func (s *Server) handleAPIRuns(w http.ResponseWriter, r *http.Request) {
	f, err := s.parseHistoryFilter(r)
	if err != nil {
		if msg, ok := errors.AsType[errBadRequest](err); ok {
			apiError(w, http.StatusBadRequest, string(msg))
			return
		}
		apiQueryError(w, err)
		return
	}
	limit := apiRunsDefault
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > apiRunsMax {
			apiError(w, http.StatusBadRequest, "limit must be a number from 1 to "+strconv.Itoa(apiRunsMax))
			return
		}
		limit = n
	}
	runs, err := s.Store.ListRunSummaries(r.Context(), f.filter, f.cursor, limit+1)
	if err != nil {
		apiQueryError(w, err)
		return
	}
	runs, p := f.page("/api/v1/runs", runs, limit)
	byID := map[string]model.Job{}
	for _, j := range f.Jobs {
		byID[j.ID] = j
	}
	out := make([]apijson.Run, 0, len(runs))
	for _, item := range runs {
		out = append(out, apijson.NewRun(item.Run, byID[item.Run.JobID]))
	}
	var next *string
	if p.Older != "" {
		// The page links carry the filter but not the limit.
		older := p.Older
		if limit != apiRunsDefault {
			older += "&limit=" + strconv.Itoa(limit)
		}
		next = &older
	}
	writeAPI(w, map[string]any{"runs": out, "next": next})
}

// handleAPIRun serves GET /api/v1/runs/{id}. Its output is at /runs/{id}/log.
func (s *Server) handleAPIRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.Store.GetRun(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		apiQueryError(w, err)
		return
	}
	job, err := s.Store.GetJob(r.Context(), run.JobID)
	if err != nil {
		apiQueryError(w, err)
		return
	}
	writeAPI(w, map[string]any{"run": apijson.NewRun(run, job), "log_url": "/runs/" + run.ID + "/log"})
}

// apiNotFound answers paths under /api/ that do not exist in JSON.
func apiNotFound(w http.ResponseWriter, r *http.Request) {
	apiError(w, http.StatusNotFound, "no such API endpoint")
}

func writeAPI(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// apiError answers with {"error": msg}.
func apiError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func apiQueryError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		apiError(w, http.StatusNotFound, "not found")
		return
	}
	apiError(w, http.StatusInternalServerError, "internal error")
}
