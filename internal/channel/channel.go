// Package channel sends alert messages to chat and push services over HTTP:
// ntfy, Slack (and Slack-compatible services such as Mattermost), Discord,
// Telegram, and any other URL as a JSON webhook.
//
// A destination is the URL the service gives you. Its host picks the
// service; for a self-hosted one, prefix the scheme with the service's name,
// as in "ntfy+https://ntfy.example.com/backups". URLs hold tokens, so errors
// name the service and host but never the URL's path or query.
package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Message is one alert about a job.
type Message struct {
	Event      string     // "failed", "timeout", "missed", "recovered" or "test"
	JobName    string     // empty for "test"
	JobSlug    string     // empty for "test"
	Host       string     // machine the job ran on
	Status     string     // run status, or "missed"
	ExitCode   *int       // nil when there is none
	Reason     string     // why a rule marked the run failed
	LastError  string     // last line the command wrote to stderr
	RunID      string     // empty for missed runs
	StartedAt  *time.Time // when the run started; nil for missed runs
	ExpectedAt *time.Time // when a missed run was due
}

// Failing reports whether m is about a job starting to fail.
func (m Message) Failing() bool {
	switch m.Event {
	case "failed", "timeout", "missed":
		return true
	}
	return false
}

// Title is a one-line summary, such as "Database Backup failed".
func (m Message) Title() string {
	switch m.Event {
	case "failed":
		return m.JobName + " failed"
	case "timeout":
		return m.JobName + " timed out"
	case "missed":
		return m.JobName + " did not start"
	case "recovered":
		return m.JobName + " recovered"
	case "test":
		return "CronWatch test notification"
	}
	return m.JobName + ": " + m.Event
}

// Body is the details under the title, one per line.
func (m Message) Body() string {
	var lines []string
	switch {
	case m.Event == "test":
		lines = append(lines, "Notifications from cronwatch reach this channel.")
	case m.Event == "missed" && m.ExpectedAt != nil:
		lines = append(lines, "It was due at "+m.ExpectedAt.Local().Format("2006-01-02 15:04")+".")
	case m.Reason != "":
		lines = append(lines, m.Reason)
	case m.ExitCode != nil && m.Event != "recovered":
		lines = append(lines, fmt.Sprintf("Exit code %d.", *m.ExitCode))
	}
	if m.LastError != "" && m.Failing() {
		lines = append(lines, "Last error: "+m.LastError)
	}
	if m.Host != "" {
		lines = append(lines, "Host: "+m.Host)
	}
	return strings.Join(lines, "\n")
}

// text is the title and body as one message, for chat services.
func (m Message) text() string {
	mark := "🟢"
	if m.Failing() {
		mark = "🔴"
	}
	return mark + " " + m.Title() + "\n" + m.Body()
}

// Kinds of destination.
const (
	Ntfy     = "ntfy"
	Slack    = "slack"
	Discord  = "discord"
	Telegram = "telegram"
	Webhook  = "webhook"
)

// Destination is a parsed destination URL.
type Destination struct {
	Kind string
	URL  *url.URL // with the "kind+" prefix removed from its scheme
}

// String names the destination without its secrets, e.g. "slack (hooks.slack.com)".
func (d Destination) String() string {
	return d.Kind + " (" + d.URL.Host + ")"
}

// Parse reads a destination URL.
func Parse(raw string) (Destination, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		// url.Parse's error repeats the URL, which may hold a token.
		return Destination{}, errors.New("not a URL: expected one such as https://ntfy.sh/TOPIC")
	}
	kind, scheme, explicit := strings.Cut(u.Scheme, "+")
	if !explicit {
		kind, scheme = "", u.Scheme
	}
	if scheme != "http" && scheme != "https" {
		return Destination{}, fmt.Errorf("unsupported scheme %q: use https://, or KIND+https:// for a self-hosted service", u.Scheme)
	}
	u.Scheme = scheme
	switch kind {
	case Ntfy, Slack, Discord, Telegram, Webhook:
	case "":
		kind = detect(u)
	default:
		return Destination{}, fmt.Errorf("unknown service %q: use ntfy, slack, discord, telegram or webhook", kind)
	}
	if kind == Telegram && u.Query().Get("chat_id") == "" {
		return Destination{}, errors.New("telegram URL needs ?chat_id=CHAT")
	}
	return Destination{Kind: kind, URL: u}, nil
}

// detect picks a service from a URL's host.
func detect(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "ntfy.sh":
		return Ntfy
	case host == "hooks.slack.com":
		return Slack
	case (host == "discord.com" || host == "discordapp.com") && strings.HasPrefix(u.Path, "/api/webhooks/"):
		return Discord
	case host == "api.telegram.org":
		return Telegram
	}
	return Webhook
}

// Size limits of the services' messages.
const (
	discordLimit  = 2000
	telegramLimit = 4096
)

// Send delivers m to d. Any response other than 2xx is an error.
func Send(ctx context.Context, client *http.Client, d Destination, m Message) error {
	req, err := request(ctx, d, m)
	if err != nil {
		return fmt.Errorf("%s: %w", d, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		// *url.Error repeats the URL, which may hold a token.
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return fmt.Errorf("%s: %w", d, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode/100 != 2 {
		msg := strings.Join(strings.Fields(string(body)), " ")
		if msg != "" {
			msg = ": " + msg
		}
		return fmt.Errorf("%s: %s%s", d, resp.Status, msg)
	}
	return nil
}

func request(ctx context.Context, d Destination, m Message) (*http.Request, error) {
	u := *d.URL
	switch d.Kind {
	case Ntfy:
		// ntfy takes the message as the body and the rest as headers. User
		// info is basic auth; an access token goes in as the password.
		user := u.User
		u.User = nil
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(m.Body()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Title", m.Title())
		if m.Failing() {
			req.Header.Set("Priority", "high")
			req.Header.Set("Tags", "rotating_light")
		} else {
			req.Header.Set("Tags", "white_check_mark")
		}
		if user != nil {
			pass, _ := user.Password()
			req.SetBasicAuth(user.Username(), pass)
		}
		return req, nil
	case Slack:
		return postJSON(ctx, u.String(), map[string]string{"text": m.text()})
	case Discord:
		return postJSON(ctx, u.String(), map[string]string{"content": truncate(m.text(), discordLimit)})
	case Telegram:
		// The URL's query (chat_id, and message_thread_id for a topic)
		// becomes the form, so the API sees every parameter the same way.
		form := u.Query()
		form.Set("text", truncate(m.text(), telegramLimit))
		u.RawQuery = ""
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return req, nil
	default:
		return postJSON(ctx, u.String(), webhookPayload(m))
	}
}

// webhookJSON is the JSON a generic webhook receives. Field names are
// stable; fields that do not apply are omitted.
type webhookJSON struct {
	Event      string `json:"event"`
	Title      string `json:"title"`
	Message    string `json:"message"`
	JobName    string `json:"job_name,omitempty"`
	JobSlug    string `json:"job_slug,omitempty"`
	Host       string `json:"host,omitempty"`
	Status     string `json:"status,omitempty"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	Reason     string `json:"reason,omitempty"`
	LastError  string `json:"last_error,omitempty"`
	RunID      string `json:"run_id,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	ExpectedAt string `json:"expected_at,omitempty"`
}

func webhookPayload(m Message) webhookJSON {
	p := webhookJSON{
		Event: m.Event, Title: m.Title(), Message: m.Body(),
		JobName: m.JobName, JobSlug: m.JobSlug, Host: m.Host, Status: m.Status, ExitCode: m.ExitCode,
		Reason: m.Reason, LastError: m.LastError, RunID: m.RunID,
	}
	if m.StartedAt != nil {
		p.StartedAt = m.StartedAt.UTC().Format(time.RFC3339)
	}
	if m.ExpectedAt != nil {
		p.ExpectedAt = m.ExpectedAt.UTC().Format(time.RFC3339)
	}
	return p
}

func postJSON(ctx context.Context, target string, v any) (*http.Request, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// truncate shortens s to at most limit characters, ending with "…" if cut.
func truncate(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit-1]) + "…"
}
