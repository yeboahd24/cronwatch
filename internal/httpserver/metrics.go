package httpserver

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yeboahd24/cronwatch/internal/model"
)

// metric is one Prometheus gauge family.
type metric struct {
	name, help string
	samples    []string
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// handleMetrics serves job state in the Prometheus text format. Values that
// are unknown, such as the last run of a job that never ran, are omitted.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	views, err := s.Store.ListJobViews(r.Context(), now)
	if err != nil {
		queryError(w, err)
		return
	}
	views = model.Unarchived(views)
	families := []*metric{
		{name: "cronwatch_job_info", help: "Always 1; labels carry the job's name, schedule and status."},
		{name: "cronwatch_job_failing", help: "1 if the job's last run failed or timed out."},
		{name: "cronwatch_job_missed", help: "1 if a scheduled run of the job did not start."},
		{name: "cronwatch_job_last_run_timestamp_seconds", help: "When the job's last run started, in Unix seconds."},
		{name: "cronwatch_job_last_run_duration_seconds", help: "How long the job's last finished run took."},
		{name: "cronwatch_job_last_run_exit_code", help: "Exit code of the job's last finished run."},
		{name: "cronwatch_job_last_success_timestamp_seconds", help: "When the job's last successful run started, in Unix seconds."},
		{name: "cronwatch_job_next_expected_timestamp_seconds", help: "When the job's next scheduled run is expected, in Unix seconds."},
	}
	add := func(f *metric, labels string, v float64) {
		f.samples = append(f.samples, fmt.Sprintf("%s{%s} %s", f.name, labels, strconv.FormatFloat(v, 'f', -1, 64)))
	}
	bool01 := func(b bool) float64 {
		if b {
			return 1
		}
		return 0
	}
	for _, v := range views {
		job := `job="` + labelEscaper.Replace(v.Slug) + `"`
		schedule := ""
		if v.Schedule != nil {
			schedule = *v.Schedule
		}
		add(families[0], fmt.Sprintf(`%s,name="%s",schedule="%s",status="%s"`, job, labelEscaper.Replace(v.Name), labelEscaper.Replace(schedule), v.Status), 1)
		add(families[1], job, bool01(model.Failing(v.Status)))
		add(families[2], job, bool01(v.Status == "missed"))
		if last := v.LastRun; last != nil {
			add(families[3], job, float64(last.StartedAt.Unix()))
			if last.DurationMS != nil {
				add(families[4], job, float64(*last.DurationMS)/1000)
			}
			if last.ExitCode != nil {
				add(families[5], job, float64(*last.ExitCode))
			}
		}
		success, err := s.Store.LastSuccessBefore(r.Context(), v.ID, now.Add(time.Second))
		switch {
		case err == nil:
			add(families[6], job, float64(success.StartedAt.Unix()))
		case !errors.Is(err, sql.ErrNoRows):
			queryError(w, err)
			return
		}
		if v.NextExpectedAt != nil {
			add(families[7], job, float64(v.NextExpectedAt.Unix()))
		}
	}
	var b strings.Builder
	for _, f := range families {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n", f.name, f.help, f.name)
		for _, sample := range f.samples {
			b.WriteString(sample + "\n")
		}
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}
