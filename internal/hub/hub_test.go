package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/cronwatch/internal/storage"
)

func openStore(t *testing.T) *storage.Store {
	t.Helper()
	s, err := storage.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func quietLogger() *log.Logger { return log.New(io.Discard, "", 0) }

func TestNewClientRefusesPlainHTTP(t *testing.T) {
	for _, tc := range []struct {
		url string
		ok  bool
	}{
		{"https://hub.example:8766", true},
		{"http://127.0.0.1:8766", true},
		{"http://localhost:8766/", true},
		{"http://hub.example:8766", false},
		{"ftp://hub.example", false},
		{"hub.example:8766", false},
	} {
		if _, err := NewClient(tc.url, "cwh_x", ""); (err == nil) != tc.ok {
			t.Errorf("NewClient(%q) error = %v, want ok %v", tc.url, err, tc.ok)
		}
	}
	if _, err := NewClient("https://hub.example", " ", ""); err == nil {
		t.Error("an empty token was accepted")
	}
}

func TestReportsOverTLS(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	token, err := s.CreateHost(ctx, "web-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	received := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	server := httptest.NewTLSServer(Handler(s, func() time.Time { return received }, quietLogger()))
	defer server.Close()

	// The hub's certificate is self-signed: trusted only when given.
	caFile := filepath.Join(t.TempDir(), "hub.pem")
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caFile, cert, 0o600); err != nil {
		t.Fatal(err)
	}
	report := Report{Version: ReportVersion, Cronwatch: "v1", Hostname: "web-1.internal", SentAt: received,
		Jobs: []Job{{Slug: "backup", Name: "Backup", Status: "failed", LastRun: &Run{Status: "failed", LastError: "disk full"}}}}
	untrusted, err := NewClient(server.URL, token, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := untrusted.Send(ctx, report); err == nil {
		t.Fatal("sent to a hub whose certificate is not trusted")
	}
	client, err := NewClient(server.URL+"/", token, caFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Send(ctx, report); err != nil {
		t.Fatal(err)
	}
	host, err := s.HostByName(ctx, "web-1")
	if err != nil {
		t.Fatal(err)
	}
	var stored Report
	if err := json.Unmarshal(host.Report, &stored); err != nil {
		t.Fatal(err)
	}
	if host.ReportedAt == nil || !host.ReportedAt.Equal(received) || stored.Hostname != "web-1.internal" || stored.Jobs[0].LastRun.LastError != "disk full" {
		t.Fatalf("stored %+v at %v", stored, host.ReportedAt)
	}

	// A new token replaces the old one at once.
	fresh, err := s.NewHostToken(ctx, "web-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Send(ctx, report); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("the replaced token still works: %v", err)
	}
	if client, _ = NewClient(server.URL, fresh, caFile); client.Send(ctx, report) != nil {
		t.Fatal("the new token does not work")
	}
}

func TestHandlerRejects(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	token, err := s.CreateHost(ctx, "web-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler(s, time.Now, quietLogger())
	valid, _ := json.Marshal(Report{Version: ReportVersion, Jobs: []Job{}})
	post := func(path, auth string, body []byte) int {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	tooMany := Report{Version: ReportVersion, Jobs: make([]Job, MaxJobs+1)}
	for i := range tooMany.Jobs {
		tooMany.Jobs[i] = Job{Slug: "j", Status: "success"}
	}
	tooManyBody, _ := json.Marshal(tooMany)
	for _, tc := range []struct {
		name, path, auth string
		body             []byte
		want             int
	}{
		{"valid", ReportPath, "Bearer " + token, valid, http.StatusNoContent},
		{"no token", ReportPath, "", valid, http.StatusUnauthorized},
		{"unknown token", ReportPath, "Bearer cwh_nope", valid, http.StatusUnauthorized},
		{"not bearer", ReportPath, token, valid, http.StatusUnauthorized},
		{"other path", "/", "Bearer " + token, valid, http.StatusNotFound},
		{"not JSON", ReportPath, "Bearer " + token, []byte("hello"), http.StatusBadRequest},
		{"newer version", ReportPath, "Bearer " + token, []byte(`{"version":2,"jobs":[]}`), http.StatusBadRequest},
		{"job without a slug", ReportPath, "Bearer " + token, []byte(`{"version":1,"jobs":[{"status":"failed"}]}`), http.StatusBadRequest},
		{"too many jobs", ReportPath, "Bearer " + token, tooManyBody, http.StatusBadRequest},
		{"over 1 MiB", ReportPath, "Bearer " + token, append([]byte(`{"version":1,"hostname":"`), bytes.Repeat([]byte("x"), MaxReportBytes)...), http.StatusBadRequest},
	} {
		if got := post(tc.path, tc.auth, tc.body); got != tc.want {
			t.Errorf("%s: status %d, want %d", tc.name, got, tc.want)
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ReportPath, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status %d", rec.Code)
	}
}

func TestCheckTrimsText(t *testing.T) {
	long := strings.Repeat("é", 400) // 800 bytes
	r := Report{Version: ReportVersion, Hostname: long, Jobs: []Job{{Slug: "a", Status: "failed", LastRun: &Run{LastError: long}}}}
	if err := r.Check(); err != nil {
		t.Fatal(err)
	}
	if len(r.Hostname) > maxName+len("…") || len(r.Jobs[0].LastRun.LastError) > maxText+len("…") || !strings.HasSuffix(r.Hostname, "é…") {
		t.Fatalf("not trimmed: %d and %d bytes", len(r.Hostname), len(r.Jobs[0].LastRun.LastError))
	}
}

func TestAssess(t *testing.T) {
	now := time.Now()
	at := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	failing, _ := json.Marshal(Report{Version: ReportVersion, Jobs: []Job{{Slug: "a", Status: "failed"}, {Slug: "b", Status: "missed"}, {Slug: "c", Status: "success"}}})
	healthy, _ := json.Marshal(Report{Version: ReportVersion, Jobs: []Job{{Slug: "c", Status: "success"}, {Slug: "d", Status: "paused"}}})
	for _, tc := range []struct {
		name     string
		host     storage.Host
		status   string
		problems int
	}{
		{"never reported", storage.Host{}, "never", 0},
		{"fresh and healthy", storage.Host{ReportedAt: at(time.Minute), Report: healthy}, "ok", 0},
		{"fresh with problems", storage.Host{ReportedAt: at(time.Minute), Report: failing}, "problems", 2},
		{"stale beats problems", storage.Host{ReportedAt: at(StaleAfter + time.Second), Report: failing}, "stale", 2},
	} {
		if st := Assess(tc.host, now); st.Status != tc.status || st.Problems != tc.problems {
			t.Errorf("%s: %s with %d problems, want %s with %d", tc.name, st.Status, st.Problems, tc.status, tc.problems)
		}
	}
}
