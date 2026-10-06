package httpserver

import (
	"bytes"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/yeboahd24/cronwatch/internal/durations"
	"github.com/yeboahd24/cronwatch/internal/logs"
	"github.com/yeboahd24/cronwatch/internal/model"
	"github.com/yeboahd24/cronwatch/internal/runenv"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

//go:embed templates/*.html static/*
var assets embed.FS

const (
	recentLogLines  = 12  // lines in the dashboard's Recent logs panel
	logSearchRuns   = 50  // runs searched per page of the Logs page
	logResultLines  = 20  // matching lines shown per run on the Logs page
	logPreviewLines = 6   // lines shown per run on the Logs page without a query
	runListLimit    = 100 // runs per page of the Runs page, and on job pages
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
	// Metrics serves Prometheus metrics at /metrics.
	Metrics bool
}

// page is the data every full page template receives.
type page struct {
	Title string
	Tab   string // "jobs", "runs", "logs" or "timeline"
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
		"kb":      humanKB,
		"add":     func(a, b int) int { return a + b },
		"failure": failureSummary,
		"dur":     humanDuration,
		"ms":      func(ms int64) string { return humanDuration(time.Duration(ms) * time.Millisecond) },
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
	r.Get("/runs/{id}/log", s.handleRunLog)
	r.Get("/logs", s.handleLogs)
	r.Get("/timeline", s.handleTimeline)
	if opts.Metrics {
		r.Get("/metrics", s.handleMetrics)
	}
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
	Jobs     []jobRow
	Archived []model.JobView
	Recent   *logExcerpt
}

// jobRow is a job on the jobs list with its recent durations.
type jobRow struct {
	model.JobView
	Trend             *jobTrend
	UndeliveredAlerts int // alerts that failed and are not delivered
}

// loadDashboard returns the job views and the run for the Recent logs panel:
// the newest last run among currently failing jobs, else the newest run.
func (s *Server) loadDashboard(r *http.Request) (dashboard, error) {
	jobs, err := s.Store.ListJobViews(r.Context(), s.now())
	if err != nil {
		return dashboard{}, err
	}
	undelivered, err := s.Store.UndeliveredAlerts(r.Context())
	if err != nil {
		return dashboard{}, err
	}
	d := dashboard{}
	for _, j := range jobs {
		if j.ArchivedAt != nil {
			d.Archived = append(d.Archived, j)
			continue
		}
		trend, err := s.Store.JobTrend(r.Context(), j.ID, listSparkRuns, s.now())
		if err != nil {
			return dashboard{}, err
		}
		d.Jobs = append(d.Jobs, jobRow{JobView: j, Trend: newJobTrend(trend, j.Name), UndeliveredAlerts: undelivered[j.ID]})
		if model.Failing(j.Status) && j.LastRun != nil && (d.Recent == nil || j.LastRun.StartedAt.After(d.Recent.Run.StartedAt)) {
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
	groups, err := s.Store.FailureGroups(r.Context(), job.ID, failureGroupLimit)
	if err != nil {
		queryError(w, err)
		return
	}
	trend, err := s.Store.JobTrend(r.Context(), job.ID, jobSparkRuns, s.now())
	if err != nil {
		queryError(w, err)
		return
	}
	history, err := s.Store.JobCrontabChanges(r.Context(), job.Slug, crontabHistoryLimit)
	if err != nil {
		queryError(w, err)
		return
	}
	alerts, err := s.Store.JobAlerts(r.Context(), job.ID, alertListLimit)
	if err != nil {
		queryError(w, err)
		return
	}
	s.render(w, "job.html", page{Title: job.Name, Tab: "jobs", Data: struct {
		View     model.JobView
		Runs     []model.Run
		Failures []storage.FailureGroup
		Trend    *jobTrend
		Crontab  []storage.CrontabChange
		Alerts   []storage.Alert
	}{view, runs, groups, newJobTrend(trend, job.Name), history, alerts}})
}

// failureGroupLimit caps the Failure types table on a job page,
// crontabHistoryLimit its Crontab history and alertListLimit its Alerts.
const (
	failureGroupLimit   = 10
	crontabHistoryLimit = 10
	alertListLimit      = 10
)

// failureSummary is the line that best describes how a run failed: its last
// error, else why a rule failed it, else its last output, else its exit code.
func failureSummary(r model.Run) string {
	if line := logs.LastError(logs.ParseAs(r.Stderr, logs.Stderr)); line != "" {
		return line
	}
	if r.Reason != "" {
		return r.Reason
	}
	if lines := logs.Parse(r.Stdout); len(lines) > 0 {
		return lines[len(lines)-1].Text
	}
	if r.ExitCode != nil {
		return fmt.Sprintf("Exited %d with no output", *r.ExitCode)
	}
	return statusLabel(r.Status)
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
	// A failed run can be compared with the job's last successful run.
	var success *model.Run
	if model.Failing(run.Status) {
		prev, err := s.Store.LastSuccessBefore(r.Context(), run.JobID, run.StartedAt)
		switch {
		case err == nil:
			success = &prev
		case !errors.Is(err, sql.ErrNoRows):
			queryError(w, err)
			return
		}
	}
	stream := r.URL.Query().Get("stream")
	var lines []logs.Line
	var compare *logCompare
	switch {
	case stream == "stdout":
		lines = logs.ParseAs(run.Stdout, logs.Stdout)
	case stream == "stderr":
		lines = stderr
	case stream == "compare" && success != nil:
		compare = compareRuns(*success, run)
	default:
		stream = "all"
		lines = logs.Parse(run.CombinedLog)
	}
	env, err := s.loadRunEnv(r, run)
	if err != nil {
		queryError(w, err)
		return
	}
	// A successful run is compared with the successes before it.
	var slow *durations.Slowness
	if run.Status == "success" && run.DurationMS != nil {
		earlier, err := s.Store.SuccessDurationsBefore(r.Context(), run.JobID, run.StartedAt, durations.Baseline)
		if err != nil {
			queryError(w, err)
			return
		}
		if sl, ok := durations.Slow(time.Duration(*run.DurationMS)*time.Millisecond, earlier); ok {
			slow = &sl
		}
	}
	var history *storage.FailureHistory
	if run.FailureSignature != "" {
		h, err := s.Store.FailureHistoryBefore(r.Context(), run)
		if err != nil {
			queryError(w, err)
			return
		}
		history = &h
	}
	alerts, err := s.Store.RunAlerts(r.Context(), run.ID)
	if err != nil {
		queryError(w, err)
		return
	}
	s.render(w, "run.html", page{Title: "Run · " + job.Name, Tab: "runs", Data: struct {
		Run         model.Run
		Job         model.Job
		Stream      string
		Lines       []logs.Line
		LastError   string
		Env         *runEnv
		LastSuccess *model.Run
		Compare     *logCompare
		History     *storage.FailureHistory
		Slow        *durations.Slowness
		Alerts      []storage.Alert
	}{run, job, stream, lines, logs.LastError(stderr), env, success, compare, history, slow, alerts}})
}

// handleRunLog serves a run's output as a plain-text download: the combined
// log without stream marks, or one stream with ?stream=stdout|stderr.
func (s *Server) handleRunLog(w http.ResponseWriter, r *http.Request) {
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
	text, suffix := logs.Plain(run.CombinedLog), ""
	switch stream := r.URL.Query().Get("stream"); stream {
	case "stdout":
		text, suffix = run.Stdout, "-stdout"
	case "stderr":
		text, suffix = run.Stderr, "-stderr"
	}
	name := fmt.Sprintf("%s-%s%s.log", job.Slug, run.StartedAt.Local().Format("20060102-150405"), suffix)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = w.Write([]byte(text))
}

// compareLines caps each list on the comparison view; logs can be long.
const compareLines = 200

// logCompare is a failed run's output compared with the last successful run's.
type logCompare struct {
	Success                    model.Run
	Added, Removed             []logs.Line // new in this run; missing from it
	AddedHidden, RemovedHidden int
	Truncated                  bool   // either log lost its middle to the capture limit
	DurationChange, ExitChange string // "" when unchanged or unknown
}

func compareRuns(success, run model.Run) *logCompare {
	added, removed := logs.Compare(logs.Parse(success.CombinedLog), logs.Parse(run.CombinedLog))
	c := &logCompare{Success: success, Truncated: success.Truncated || run.Truncated}
	c.Added, c.AddedHidden = capLines(added)
	c.Removed, c.RemovedHidden = capLines(removed)
	if run.DurationMS != nil && success.DurationMS != nil {
		now, before := time.Duration(*run.DurationMS)*time.Millisecond, time.Duration(*success.DurationMS)*time.Millisecond
		c.DurationChange = fmt.Sprintf("%s, the last success took %s", humanDuration(now), humanDuration(before))
	}
	if run.ExitCode != nil && success.ExitCode != nil && *run.ExitCode != *success.ExitCode {
		c.ExitChange = fmt.Sprintf("%d, the last success exited %d", *run.ExitCode, *success.ExitCode)
	}
	return c
}

func capLines(lines []logs.Line) ([]logs.Line, int) {
	if len(lines) <= compareLines {
		return lines, 0
	}
	return lines[:compareLines], len(lines) - compareLines
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
	if !model.Failing(run.Status) {
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
	f, err := s.parseHistoryFilter(r)
	if err != nil {
		filterError(w, err)
		return
	}
	runs, err := s.Store.ListRunsPage(r.Context(), f.filter, f.cursor, runListLimit+1)
	if err != nil {
		queryError(w, err)
		return
	}
	runs, p := f.page("/runs", runs, runListLimit)
	s.render(w, "runs.html", page{Title: "Runs", Tab: "runs", Data: struct {
		Filter historyFilter
		Runs   []storage.RunWithJob
		Pager  pager
	}{f, runs, p}})
}

// handleLogs searches run output, a page of runs at a time. Without a query
// it previews the end of each run; "errors" limits matches to stderr lines.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	f, err := s.parseHistoryFilter(r)
	if err != nil {
		filterError(w, err)
		return
	}
	runs, err := s.Store.SearchRunLogs(r.Context(), f.Query, f.filter, f.cursor, logSearchRuns+1)
	if err != nil {
		queryError(w, err)
		return
	}
	runs, p := f.page("/logs", runs, logSearchRuns)
	results := make([]logExcerpt, 0, len(runs))
	for _, item := range runs {
		lines := logs.Filter(logs.Parse(item.Run.CombinedLog), f.OnlyErrors, f.Query)
		if len(lines) == 0 {
			continue
		}
		limit := logResultLines
		if f.Query == "" && !f.OnlyErrors {
			limit = logPreviewLines
		}
		shown := logs.Tail(lines, limit)
		results = append(results, logExcerpt{Run: item.Run, JobName: item.JobName, Lines: shown, Hidden: len(lines) - len(shown)})
	}
	s.render(w, "logs.html", page{Title: "Logs", Tab: "logs", Data: struct {
		Filter  historyFilter
		Scanned int // runs searched on this page
		Results []logExcerpt
		Pager   pager
	}{f, len(runs), results, p}})
}
