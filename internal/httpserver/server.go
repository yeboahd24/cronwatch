package httpserver

import (
	"bytes"
	"database/sql"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/yeboahd24/cronwatch/internal/model"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

//go:embed templates/*.html static/*
var assets embed.FS

type Server struct {
	Router    http.Handler
	Templates *template.Template
	Store     *storage.Store
}

func formatTime(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05 MST") }
func maybeTime(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return formatTime(*t)
}
func duration(ms *int64) string {
	if ms == nil {
		return "—"
	}
	return (time.Duration(*ms) * time.Millisecond).String()
}

// Options configures the web server.
type Options struct {
	// AnyHost disables the loopback Host-header check, for --public servers
	// reached through a real hostname.
	AnyHost bool
}

func New(store *storage.Store, opts Options) (*Server, error) {
	t, err := template.New("").Funcs(template.FuncMap{"time": formatTime, "maybeTime": maybeTime, "duration": duration}).ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	static, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, err
	}
	r := chi.NewRouter()
	r.Use(middleware.Recoverer, securityHeaders)
	if !opts.AnyHost {
		r.Use(loopbackHostOnly)
	}
	s := &Server{Router: r, Templates: t, Store: store}
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	r.Get("/", s.handleIndex)
	r.Get("/jobs/{id}", s.handleJob)
	r.Get("/runs", s.handleRuns)
	r.Get("/runs/{id}", s.handleRun)
	r.Get("/partials/jobs", s.handleJobsPartial)
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	return s, nil
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	var body bytes.Buffer
	if err := s.Templates.ExecuteTemplate(&body, name, data); err != nil {
		http.Error(w, "render page: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body.Bytes())
}
func queryError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.Store.ListJobViews(r.Context(), time.Now())
	if err != nil {
		queryError(w, err)
		return
	}
	s.render(w, "index.html", struct{ Jobs []model.JobView }{jobs})
}
func (s *Server) handleJobsPartial(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.Store.ListJobViews(r.Context(), time.Now())
	if err != nil {
		queryError(w, err)
		return
	}
	s.render(w, "jobs_rows", struct{ Jobs []model.JobView }{jobs})
}
func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.Store.GetJob(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		queryError(w, err)
		return
	}
	view, err := s.Store.JobView(r.Context(), job, time.Now())
	if err != nil {
		queryError(w, err)
		return
	}
	runs, err := s.Store.ListRunsForJob(r.Context(), job.ID, 100)
	if err != nil {
		queryError(w, err)
		return
	}
	s.render(w, "job.html", struct {
		View model.JobView
		Runs []model.Run
	}{view, runs})
}
func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.Store.GetRun(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		queryError(w, err)
		return
	}
	job, err := s.Store.GetJob(r.Context(), run.JobID)
	if err != nil {
		queryError(w, err)
		return
	}
	s.render(w, "run.html", struct {
		Run model.Run
		Job model.Job
	}{run, job})
}
func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListRunsWithJob(r.Context(), 100)
	if err != nil {
		queryError(w, err)
		return
	}
	s.render(w, "runs.html", struct{ Items []storage.RunWithJob }{items})
}
