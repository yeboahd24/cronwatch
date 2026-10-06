package httpserver

import (
	"cmp"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/yeboahd24/cronwatch/internal/hub"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

// localHost is how the Servers page names the server it runs on.
const localHost = "this server"

// serverJob is a job on the Servers page.
type serverJob struct {
	Host    string
	Job     hub.Job
	LocalID string // the job's ID on this server, for a link; "" for others
	Stale   bool   // the host's data is out of date
	Age     time.Duration
}

// serverStates returns this server, then every reporting host, and all of
// their jobs, those needing attention first.
func (s *Server) serverStates(r *http.Request) ([]hub.HostState, []serverJob, error) {
	ctx := r.Context()
	now := s.now()
	local, err := hub.BuildReport(ctx, s.Store, s.version, now)
	if err != nil {
		return nil, nil, err
	}
	reported := now
	states := []hub.HostState{{Host: storage.Host{Name: localHost, ReportedAt: &reported}, Report: &local, Status: "ok"}}
	for _, j := range local.Jobs {
		if j.Problem() {
			states[0].Problems++
		}
	}
	if states[0].Problems > 0 {
		states[0].Status = "problems"
	}
	hosts, err := s.Store.ListHosts(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, h := range hosts {
		states = append(states, hub.Assess(h, now))
	}
	localIDs := map[string]string{}
	if jobs, err := s.Store.ListJobs(ctx); err == nil {
		for _, j := range jobs {
			localIDs[j.Slug] = j.ID
		}
	}
	var jobs []serverJob
	for i, st := range states {
		if st.Report == nil {
			continue
		}
		for _, j := range st.Report.Jobs {
			sj := serverJob{Host: st.Host.Name, Job: j, Stale: st.Status == "stale", Age: st.Age}
			if i == 0 {
				sj.LocalID = localIDs[j.Slug]
			}
			jobs = append(jobs, sj)
		}
	}
	slices.SortStableFunc(jobs, func(a, b serverJob) int {
		if a.Job.Problem() != b.Job.Problem() {
			if a.Job.Problem() {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(a.Host, b.Host), cmp.Compare(a.Job.Name, b.Job.Name))
	})
	return states, jobs, nil
}

func (s *Server) handleServers(w http.ResponseWriter, r *http.Request) {
	states, jobs, err := s.serverStates(r)
	if err != nil {
		queryError(w, err)
		return
	}
	s.render(w, "servers.html", page{Title: "Servers", Tab: "servers", Data: struct {
		Hosts      []hub.HostState
		Jobs       []serverJob
		StaleAfter time.Duration
	}{states, jobs, hub.StaleAfter}})
}

func (s *Server) handleServer(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	states, jobs, err := s.serverStates(r)
	if err != nil {
		queryError(w, err)
		return
	}
	i := slices.IndexFunc(states, func(st hub.HostState) bool { return st.Host.Name == name })
	if i < 0 {
		http.Error(w, "no such server", http.StatusNotFound)
		return
	}
	jobs = slices.DeleteFunc(jobs, func(j serverJob) bool { return j.Host != name })
	s.render(w, "server.html", page{Title: name, Tab: "servers", Data: struct {
		State      hub.HostState
		Jobs       []serverJob
		Local      bool
		StaleAfter time.Duration
	}{states[i], jobs, i == 0, hub.StaleAfter}})
}
