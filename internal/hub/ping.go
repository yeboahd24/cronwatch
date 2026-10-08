package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// PingPath is where a hub accepts pings, followed by the job's slug and
// optionally "/start", "/fail" or "/EXIT-CODE".
const PingPath = "/api/v1/ping/"

// MaxPingBytes bounds a ping's body, which is recorded as the run's output.
const MaxPingBytes = 1 << 20

// Ping is a ping that arrived over HTTP: a remote "cronwatch ping". Nil
// fields were not given.
type Ping struct {
	From     string // name of the host whose token sent it
	Slug     string
	Start    bool
	Fail     bool
	ExitCode int // -1 if not given
	Message  string

	Name        *string
	Schedule    *string
	Every       *time.Duration
	Grace       *time.Duration
	MaxDuration *time.Duration
	Tags        []string // nil if not given
}

// PingFunc records a ping. An error that wraps ErrBadPing is the sender's
// mistake, answered with 400; any other is answered with 503.
type PingFunc func(ctx context.Context, p Ping) error

// ErrBadPing marks a ping that cannot be recorded as sent.
var ErrBadPing = errors.New("bad ping")

// parsePing reads the ping in r, whose path starts with PingPath, but not
// its body.
func parsePing(r *http.Request) (Ping, error) {
	p := Ping{ExitCode: -1}
	rest := strings.TrimPrefix(r.URL.Path, PingPath)
	slug, action, _ := strings.Cut(rest, "/")
	p.Slug = slug
	switch {
	case action == "":
	case action == "start":
		p.Start = true
	case action == "fail":
		p.Fail = true
	default:
		code, err := strconv.Atoi(action)
		if err != nil || code < 0 || code > 255 || strings.Contains(action, "+") {
			return p, fmt.Errorf("unknown ping %q: use /start, /fail or an exit code from 0 to 255", action)
		}
		p.ExitCode = code
		p.Fail = code != 0
	}
	for key, values := range r.URL.Query() {
		v := values[len(values)-1]
		switch key {
		case "name":
			p.Name = &v
		case "schedule":
			p.Schedule = &v
		case "tag":
			p.Tags = append([]string{}, values...)
		case "every", "grace", "max_duration":
			d, err := time.ParseDuration(v)
			if err != nil {
				return p, fmt.Errorf("%s=%q is not a duration such as 10m", key, v)
			}
			switch key {
			case "every":
				p.Every = &d
			case "grace":
				p.Grace = &d
			default:
				p.MaxDuration = &d
			}
		default:
			return p, fmt.Errorf("unknown parameter %q: use name, schedule, every, grace, max_duration or tag", key)
		}
	}
	return p, nil
}

// servePing handles a ping from host, already authenticated.
func servePing(w http.ResponseWriter, r *http.Request, from string, ping PingFunc) {
	p, err := parsePing(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxPingBytes))
	if err != nil {
		http.Error(w, "the body is over 1 MiB", http.StatusRequestEntityTooLarge)
		return
	}
	p.From = from
	p.Message = strings.ToValidUTF8(string(body), "�")
	if err := ping(r.Context(), p); err != nil {
		if errors.Is(err, ErrBadPing) {
			http.Error(w, strings.TrimPrefix(err.Error(), ErrBadPing.Error()+": "), http.StatusBadRequest)
			return
		}
		http.Error(w, "try again later", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
