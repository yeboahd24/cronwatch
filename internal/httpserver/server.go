package httpserver

import (
	"bytes"
	"database/sql"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/yeboahd24/cronwatch/internal/logs"
	"github.com/yeboahd24/cronwatch/internal/model"
	"github.com/yeboahd24/cronwatch/internal/runenv"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

//go:embed templates/*.html static/*
var assets embed.FS

const (
	recentLogLines  = 12  // lines in the dashboard's Recent logs panel
	logSearchRuns   = 50  // runs scanned by the Logs page
	logResultLines  = 20  // matching lines shown per run on the Logs page
	logPreviewLines = 6   // lines shown per run on the Logs page without a query
	runListLimit    = 100 // runs listed on the Runs and job pages
)

type Server struct {
	Router    http.Handler
	Templates *template.Template
	Store     *storage.Store
	now       func() time.Time
}

// Options configures the web server.
type Options struct {
	// AnyHost disables the loopback Host-header check, for --public servers
	// reached through a real hostname.
	AnyHost bool
}

// page is the data every full page template receives.
type page struct {
	Title string
	Tab   string // "jobs", "runs" or "logs"
	Data  any
}

// logExcerpt is a run with some of its log lines, for the dashboard and Logs page.
type logExcerpt struct {
	Run     model.Run
	JobName string
	Lines   []logs.Line
	Hidden  int // matching lines not shown
}

func New(store *storage.Store, opts Options) (*Server, error) {
	s := &Server{Store: store, now: time.Now}
	funcs := template.FuncMap{
		"status": statusLabel,
		"when":   func(t time.Time) string { return shortTime(t, s.now()) },
		"whenPtr": func(t *time.Time) string {
			if t == nil {
				return "—"
			}
			return shortTime(*t, s.now())
		},
		"full": fullTime,
		"iso":  func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
		"duration": func(ms *int64) string {
			if ms == nil {
				return "—"
			}
			return humanDuration(time.Duration(*ms) * time.Millisecond)
		},
		"seconds": func(n int64) string { return humanDuration(time.Duration(n) * time.Second) },
		"shell":   shellCommand,
		"join":    strings.Join,
	}
	t, err := template.New("").Funcs(funcs).ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	s.Templates = t
	static, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, err
	}
	r := chi.NewRouter()
	r.Use(middleware.Recoverer, securityHeaders)
	if !opts.AnyHost {
		r.Use(loopbackHostOnly)
	}
	s.Router = r
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	r.Get("/", s.handleIndex)
	r.Get("/jobs/{id}", s.handleJob)
	r.Get("/runs", s.handleRuns)
	r.Get("/runs/{id}", s.handleRun)
	r.Get("/logs", s.handleLogs)
	r.Get("/partials/dashboard", s.handleDashboardPartial)
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

type dashboard struct {
	Jobs   []model.JobView
	Recent *logExcerpt
}

// loadDashboard returns the job views and the run for the Recent logs panel:
// the newest last run among currently failing jobs, else the newest run.
func (s *Server) loadDashboard(r *http.Request) (dashboard, error) {
	jobs, err := s.Store.ListJobViews(r.Context(), s.now())
	if err != nil {
		return dashboard{}, err
	}
	d := dashboard{Jobs: jobs}
	for _, j := range jobs {
		if j.Status == "failed" && j.LastRun != nil && (d.Recent == nil || j.LastRun.StartedAt.After(d.Recent.Run.StartedAt)) {
			d.Recent = &logExcerpt{Run: *j.LastRun, JobName: j.Name}
		}
	}
	if d.Recent == nil {
		latest, err := s.Store.ListRunsWithJob(r.Context(), 1)
		if err != nil {
			return dashboard{}, err
		}
		if len(latest) == 1 {
			d.Recent = &logExcerpt{Run: latest[0].Run, JobName: latest[0].JobName}
		}
	}
	if d.Recent != nil {
		d.Recent.Lines = logs.Tail(logs.Parse(d.Recent.Run.CombinedLog), recentLogLines)
	}
	return d, nil
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	d, err := s.loadDashboard(r)
	if err != nil {
		queryError(w, err)
		return
	}
	s.render(w, "index.html", page{Title: "Jobs", Tab: "jobs", Data: d})
}

// handleDashboardPartial serves the parts of the dashboard that refresh live.
func (s *Server) handleDashboardPartial(w http.ResponseWriter, r *http.Request) {
	d, err := s.loadDashboard(r)
	if err != nil {
		queryError(w, err)
		return
	}
	s.render(w, "dashboard_live", d)
}

func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.Store.GetJob(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		queryError(w, err)
		return
	}
	view, err := s.Store.JobView(r.Context(), job, s.now())
	if err != nil {
		queryError(w, err)
		return
	}
	runs, err := s.Store.ListRunsForJob(r.Context(), job.ID, runListLimit)
	if err != nil {
		queryError(w, err)
		return
	}
	s.render(w, "job.html", page{Title: job.Name, Tab: "jobs", Data: struct {
		View model.JobView
		Runs []model.Run
	}{view, runs}})
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
	stderr := logs.ParseAs(run.Stderr, logs.Stderr)
	stream := r.URL.Query().Get("stream")
	var lines []logs.Line
	switch stream {
	case "stdout":
		lines = logs.ParseAs(run.Stdout, logs.Stdout)
	case "stderr":
		lines = stderr
	default:
		stream = "all"
		lines = logs.Parse(run.CombinedLog)
	}
	env, err := s.loadRunEnv(r, run)
	if err != nil {
		queryError(w, err)
		return
	}
	s.render(w, "run.html", page{Title: "Run · " + job.Name, Tab: "runs", Data: struct {
		Run       model.Run
		Job       model.Job
		Stream    string
		Lines     []logs.Line
		LastError string
		Env       *runEnv
	}{run, job, stream, lines, logs.LastError(stderr), env}})
}

// runEnv is a run's recorded environment for the run page. Changed is set for
// a failed run whose environment differs from the last successful run's.
type runEnv struct {
	runenv.Env
	Changed *envChange
}

type envChange struct {
	Since model.Run   // the last successful run
	Diff  runenv.Diff // A is the last success, B is this run
}

// loadRunEnv returns nil for runs recorded before environments were captured.
func (s *Server) loadRunEnv(r *http.Request, run model.Run) (*runEnv, error) {
	if run.EnvHash == "" {
		return nil, nil
	}
	env, err := s.Store.GetEnvironment(r.Context(), run.EnvHash)
	if err != nil {
		return nil, err
	}
	re := &runEnv{Env: env}
	if run.Status != "failed" {
		return re, nil
	}
	success, err := s.Store.LastSuccessWithEnvBefore(r.Context(), run.JobID, run.StartedAt)
	if errors.Is(err, sql.ErrNoRows) || err == nil && success.EnvHash == run.EnvHash {
		return re, nil
	} else if err != nil {
		return nil, err
	}
	before, err := s.Store.GetEnvironment(r.Context(), success.EnvHash)
	if err != nil {
		return nil, err
	}
	if diff, _ := runenv.Compare(before, env).WithoutSession(); !diff.Empty() {
		re.Changed = &envChange{Since: success, Diff: diff}
	}
	return re, nil
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListRunsWithJob(r.Context(), runListLimit)
	if err != nil {
		queryError(w, err)
		return
	}
	s.render(w, "runs.html", page{Title: "Runs", Tab: "runs", Data: items})
}

// handleLogs searches recent run output. Without a query it previews the end
// of each recent run; "errors" limits matches to stderr lines.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	onlyErrors := r.URL.Query().Get("errors") == "1"
	runs, err := s.Store.SearchRunLogs(r.Context(), query, logSearchRuns)
	if err != nil {
		queryError(w, err)
		return
	}
	results := make([]logExcerpt, 0, len(runs))
	for _, item := range runs {
		lines := logs.Filter(logs.Parse(item.Run.CombinedLog), onlyErrors, query)
		if len(lines) == 0 {
			continue
		}
		limit := logResultLines
		if query == "" && !onlyErrors {
			limit = logPreviewLines
		}
		shown := logs.Tail(lines, limit)
		results = append(results, logExcerpt{Run: item.Run, JobName: item.JobName, Lines: shown, Hidden: len(lines) - len(shown)})
	}
	s.render(w, "logs.html", page{Title: "Logs", Tab: "logs", Data: struct {
		Query      string
		OnlyErrors bool
		Results    []logExcerpt
	}{query, onlyErrors, results}})
}
