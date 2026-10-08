package app

import (
	"encoding/json"
	"io"
)

// The --json output of sync. Jobs and runs are in package apijson. Field
// names are stable; times are RFC 3339 in UTC, and absent values are null.

type jsonSync struct {
	Jobs        int               `json:"jobs"`
	Added       []string          `json:"added"`
	Updated     []string          `json:"updated"`
	Unscheduled []string          `json:"unscheduled"`
	Problems    []string          `json:"problems"`
	Unmonitored []jsonCrontabLine `json:"unmonitored"`
	Changes     []jsonChange      `json:"crontab_changes"`
}

type jsonChange struct {
	JobSlug string `json:"job_slug,omitempty"`
	Kind    string `json:"kind"`
	Before  string `json:"before,omitempty"`
	After   string `json:"after,omitempty"`
}

type jsonCrontabLine struct {
	Line     int    `json:"line"`
	Schedule string `json:"schedule"`
	Command  string `json:"command"`
}

func newJSONSync(r syncResult) jsonSync {
	out := jsonSync{Jobs: r.Jobs, Added: nonNil(r.Added), Updated: nonNil(r.Updated), Unscheduled: nonNil(r.Unscheduled), Problems: nonNil(r.Problems),
		Unmonitored: []jsonCrontabLine{}, Changes: []jsonChange{}}
	for _, c := range r.Changes {
		out.Changes = append(out.Changes, jsonChange{JobSlug: c.JobSlug, Kind: c.Kind, Before: c.Before, After: c.After})
	}
	for _, e := range r.Unmonitored {
		out.Unmonitored = append(out.Unmonitored, jsonCrontabLine{Line: e.Line, Schedule: e.Raw, Command: e.Command})
	}
	return out
}

// nonNil makes an empty list print as [] rather than null.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
