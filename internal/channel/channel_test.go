package channel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestParseDetectsServices(t *testing.T) {
	for raw, want := range map[string]string{
		"https://ntfy.sh/backups":                                 Ntfy,
		"https://hooks.slack.com/services/T0/B0/xyz":              Slack,
		"https://discord.com/api/webhooks/1/abc":                  Discord,
		"https://discordapp.com/api/webhooks/1/abc":               Discord,
		"https://api.telegram.org/bot1:abc/sendMessage?chat_id=5": Telegram,
		"https://example.com/hook":                                Webhook,
		"https://discord.com/channels/1":                          Webhook,
		"ntfy+http://10.0.0.2:8080/backups":                       Ntfy,
		"slack+https://chat.example.com/hooks/abc":                Slack,
	} {
		d, err := Parse(raw)
		if err != nil {
			t.Errorf("Parse(%q): %v", raw, err)
			continue
		}
		if d.Kind != want {
			t.Errorf("Parse(%q).Kind = %q, want %q", raw, d.Kind, want)
		}
		if strings.Contains(d.URL.Scheme, "+") {
			t.Errorf("Parse(%q) kept the prefix in scheme %q", raw, d.URL.Scheme)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, raw := range []string{
		"ntfy.sh/backups",
		"ftp://example.com/x",
		"matrix+https://example.com/x",
		"https://api.telegram.org/bot1:abc/sendMessage",
	} {
		if _, err := Parse(raw); err == nil {
			t.Errorf("Parse(%q) = nil error", raw)
		}
	}
}

func TestErrorsHideSecrets(t *testing.T) {
	_, err := Parse("https://hooks.slack.com/services/T0/B0/sekret\x7f")
	if err == nil || strings.Contains(err.Error(), "sekret") {
		t.Errorf("Parse error = %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such hook", http.StatusNotFound)
	}))
	defer srv.Close()
	d := mustParse(t, "slack+"+srv.URL+"/hooks/sekret")
	err = Send(context.Background(), srv.Client(), d, failedMessage())
	if err == nil || strings.Contains(err.Error(), "sekret") || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "no such hook") {
		t.Errorf("Send error = %v", err)
	}

	srv.Close()
	err = Send(context.Background(), http.DefaultClient, d, failedMessage())
	if err == nil || strings.Contains(err.Error(), "sekret") {
		t.Errorf("Send error after close = %v", err)
	}
}

// received is one request a test server saw.
type received struct {
	header http.Header
	path   string
	query  url.Values
	body   string
	user   string
	pass   string
}

func capture(t *testing.T) (*httptest.Server, *received) {
	t.Helper()
	got := &received{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.header, got.path, got.query, got.body = r.Header, r.URL.Path, r.URL.Query(), string(b)
		got.user, got.pass, _ = r.BasicAuth()
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func mustParse(t *testing.T, raw string) Destination {
	t.Helper()
	d, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func failedMessage() Message {
	code := 2
	started := time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC)
	return Message{Event: "failed", JobName: "Database Backup", JobSlug: "database-backup", Host: "db1",
		Status: "failed", ExitCode: &code, LastError: "pg_dump: command not found", RunID: "r1", StartedAt: &started}
}

func send(t *testing.T, srv *httptest.Server, raw string, m Message) {
	t.Helper()
	if err := Send(context.Background(), srv.Client(), mustParse(t, raw), m); err != nil {
		t.Fatal(err)
	}
}

func TestNtfy(t *testing.T) {
	srv, got := capture(t)
	u := strings.Replace(srv.URL, "http://", "ntfy+http://:tk_secret@", 1) + "/backups"
	send(t, srv, u, failedMessage())
	if got.path != "/backups" || got.header.Get("Title") != "Database Backup failed" || got.header.Get("Priority") != "high" {
		t.Errorf("request = %+v", got)
	}
	if got.user != "" || got.pass != "tk_secret" {
		t.Errorf("basic auth = %q:%q", got.user, got.pass)
	}
	if want := "Exit code 2.\nLast error: pg_dump: command not found\nHost: db1"; got.body != want {
		t.Errorf("body = %q, want %q", got.body, want)
	}

	send(t, srv, "ntfy+"+srv.URL+"/backups", Message{Event: "recovered", JobName: "Database Backup", Host: "db1"})
	if got.header.Get("Priority") != "" || got.header.Get("Tags") != "white_check_mark" || got.body != "Host: db1" {
		t.Errorf("recovered request = %+v", got)
	}
}

func TestSlackAndDiscord(t *testing.T) {
	srv, got := capture(t)
	send(t, srv, "slack+"+srv.URL+"/hooks/x", failedMessage())
	var slack map[string]string
	if err := json.Unmarshal([]byte(got.body), &slack); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(slack["text"], "🔴 Database Backup failed\nExit code 2.") {
		t.Errorf("slack text = %q", slack["text"])
	}

	m := failedMessage()
	m.LastError = strings.Repeat("x", 3000)
	send(t, srv, "discord+"+srv.URL+"/api/webhooks/1/x", m)
	var discord map[string]string
	if err := json.Unmarshal([]byte(got.body), &discord); err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(discord["content"])); n != discordLimit || !strings.HasSuffix(discord["content"], "…") {
		t.Errorf("discord content is %d characters", n)
	}
}

func TestTelegram(t *testing.T) {
	srv, got := capture(t)
	send(t, srv, "telegram+"+srv.URL+"/bot1:abc/sendMessage?chat_id=-100&message_thread_id=7", failedMessage())
	form, err := url.ParseQuery(got.body)
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/bot1:abc/sendMessage" || len(got.query) != 0 {
		t.Errorf("path = %q, query = %v", got.path, got.query)
	}
	if form.Get("chat_id") != "-100" || form.Get("message_thread_id") != "7" || !strings.HasPrefix(form.Get("text"), "🔴 Database Backup failed") {
		t.Errorf("form = %v", form)
	}
}

func TestWebhook(t *testing.T) {
	srv, got := capture(t)
	expected := time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC)
	send(t, srv, srv.URL+"/hook", Message{Event: "missed", JobName: "Database Backup", JobSlug: "database-backup",
		Host: "db1", Status: "missed", ExpectedAt: &expected})
	var p map[string]any
	if err := json.Unmarshal([]byte(got.body), &p); err != nil {
		t.Fatal(err)
	}
	if p["event"] != "missed" || p["job_slug"] != "database-backup" || p["expected_at"] != "2026-10-08T02:00:00Z" || p["title"] != "Database Backup did not start" {
		t.Errorf("payload = %v", p)
	}
	for _, absent := range []string{"exit_code", "run_id", "started_at"} {
		if _, ok := p[absent]; ok {
			t.Errorf("payload has %s: %v", absent, p)
		}
	}
	if got.header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", got.header.Get("Content-Type"))
	}
}

func TestReasonTakesPrecedenceOverExitCode(t *testing.T) {
	m := failedMessage()
	m.Reason = `output matched --fail-if-match "ERROR"`
	if !strings.HasPrefix(m.Body(), m.Reason+"\n") {
		t.Errorf("body = %q", m.Body())
	}
}
