package httpserver

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/yeboahd24/cronwatch/internal/badge"
	"github.com/yeboahd24/cronwatch/internal/model"
)

// handleJobBadge serves /badge/SLUG.svg, or .json for shields.io.
func (s *Server) handleJobBadge(w http.ResponseWriter, r *http.Request) {
	slug, ext, ok := badgeFile(chi.URLParam(r, "file"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	job, err := s.Store.GetJobBySlug(r.Context(), slug)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "no job with this slug", http.StatusNotFound)
		return
	}
	if err != nil {
		queryError(w, err)
		return
	}
	view, err := s.Store.JobView(r.Context(), job, s.now())
	if err != nil {
		queryError(w, err)
		return
	}
	writeBadge(w, r, badge.ForJob(view), ext)
}

// handleTagBadge serves /badge/tag/TAG.svg, or .json: how many of the
// tag's jobs need attention.
func (s *Server) handleTagBadge(w http.ResponseWriter, r *http.Request) {
	name, ext, ok := badgeFile(chi.URLParam(r, "file"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	tags, err := model.Tags([]string{name})
	if err != nil || len(tags) != 1 || tags[0] != name {
		http.Error(w, "not a tag", http.StatusNotFound)
		return
	}
	views, err := s.Store.ListJobViews(r.Context(), s.now())
	if err != nil {
		queryError(w, err)
		return
	}
	writeBadge(w, r, badge.ForJobs(name, model.WithTags(views, tags)), ext)
}

// badgeFile splits "name.svg" or "name.json".
func badgeFile(file string) (name, ext string, ok bool) {
	for _, ext := range []string{".svg", ".json"} {
		if name, ok := strings.CutSuffix(file, ext); ok && name != "" {
			return name, ext, true
		}
	}
	return "", "", false
}

// writeBadge writes b, with the label from ?label= if given. Badges are
// not cached, so a README shows the current status.
func writeBadge(w http.ResponseWriter, r *http.Request, b badge.Badge, ext string) {
	b = b.WithLabel(r.URL.Query().Get("label"))
	w.Header().Set("Cache-Control", "no-cache, max-age=0")
	if ext == ".json" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b.ShieldsJSON())
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	_, _ = w.Write(b.SVG())
}
