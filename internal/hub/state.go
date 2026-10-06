package hub

import (
	"encoding/json"
	"time"

	"github.com/yeboahd24/cronwatch/internal/storage"
)

// A server reports every ReportEvery; its data is stale after StaleAfter
// without a report, three missed reports.
const (
	ReportEvery = time.Minute
	StaleAfter  = 3 * ReportEvery
)

// HostState is what the hub knows about a host.
type HostState struct {
	Host   storage.Host
	Report *Report // the latest report; nil if none, or if it cannot be read
	// Status is "never" (no report yet), "stale" (no report for StaleAfter),
	// "problems" (a job needs attention) or "ok".
	Status   string
	Age      time.Duration // since the latest report
	Problems int           // jobs needing attention in the latest report
}

// Assess returns the state of a host at now.
func Assess(h storage.Host, now time.Time) HostState {
	st := HostState{Host: h, Status: "never"}
	if h.ReportedAt == nil {
		return st
	}
	st.Age = now.Sub(*h.ReportedAt)
	var r Report
	if json.Unmarshal(h.Report, &r) == nil {
		st.Report = &r
		for _, j := range r.Jobs {
			if j.Problem() {
				st.Problems++
			}
		}
	}
	switch {
	case st.Age > StaleAfter:
		st.Status = "stale"
	case st.Problems > 0:
		st.Status = "problems"
	default:
		st.Status = "ok"
	}
	return st
}
