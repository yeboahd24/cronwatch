package app

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/cronwatch/internal/hub"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

// pingHub serves a hub that accepts pings, returning a function that sends
// one with a valid token and returns the response's status and body.
func pingHub(t *testing.T, s *storage.Store) func(path, body string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	token, err := s.CreateHost(ctx, "nas", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(hub.Handler(s, time.Now, log.New(io.Discard, "", 0), hubPings(ctx, s, io.Discard)))
	t.Cleanup(server.Close)
	return func(path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(b))
	}
}

func TestHubPings(t *testing.T) {
	ctx := context.Background()
	t.Setenv(envOnFailure, "")
	t.Setenv(envOnRecover, "")
	t.Setenv(envNotify, "")
	s := openStore(t, t.TempDir())
	ping := pingHub(t, s)

	if code, body := ping("/api/v1/ping/etl/start?name=Nightly+ETL&schedule=0+2+*+*+*&grace=15m&max_duration=2h", ""); code != http.StatusNoContent {
		t.Fatalf("start: %d %s", code, body)
	}
	job, err := s.GetJobBySlug(ctx, "etl")
	if err != nil {
		t.Fatal(err)
	}
	if job.Name != "Nightly ETL" || job.Schedule == nil || *job.Schedule != "0 2 * * *" || job.GraceSeconds != 900 {
		t.Errorf("job = %+v", job)
	}
	if code, body := ping("/api/v1/ping/etl/fail", "loaded 0 rows\nupstream 503"); code != http.StatusNoContent {
		t.Fatalf("fail: %d %s", code, body)
	}
	if code, body := ping("/api/v1/ping/etl/3", ""); code != http.StatusNoContent {
		t.Fatalf("exit code: %d %s", code, body)
	}
	if code, body := ping("/api/v1/ping/etl", "loaded 9 rows"); code != http.StatusNoContent {
		t.Fatalf("success: %d %s", code, body)
	}

	runs, err := s.ListRunsForJob(ctx, job.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range runs {
		code := -1
		if r.ExitCode != nil {
			code = *r.ExitCode
		}
		got = append(got, strings.TrimSpace(r.Status+" "+strconv.Itoa(code)+" "+strings.TrimSpace(r.Stdout+r.Stderr)))
	}
	want := []string{"success 0 loaded 9 rows", "failed 3", "failed 1 loaded 0 rows\nupstream 503"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("runs, newest first:\n%q\nwant\n%q", got, want)
	}
	// The hub's own environment is not the remote job's.
	if run, err := s.LatestRunWithEnv(ctx, job.ID); err == nil {
		t.Errorf("a remote ping recorded an environment, for run %s", run.ID)
	}
}

func TestHubPingErrors(t *testing.T) {
	s := openStore(t, t.TempDir())
	ping := pingHub(t, s)
	for path, want := range map[string]int{
		"/api/v1/ping/Bad_Slug":                http.StatusBadRequest,
		"/api/v1/ping/etl/restart":             http.StatusBadRequest,
		"/api/v1/ping/etl/256":                 http.StatusBadRequest,
		"/api/v1/ping/etl?schedule=0+0+30+2+*": http.StatusBadRequest,
		"/api/v1/ping/etl?grace=soon":          http.StatusBadRequest,
		"/api/v1/ping/etl?on_failure=rm+-rf+~": http.StatusBadRequest,
		"/api/v1/ping/":                        http.StatusBadRequest,
	} {
		if code, body := ping(path, ""); code != want {
			t.Errorf("%s: %d %s, want %d", path, code, body, want)
		}
	}
	if code, body := ping("/api/v1/ping/etl/start", "a message"); code != http.StatusBadRequest || !strings.Contains(body, "end ping") {
		t.Errorf("start with a body: %d %s", code, body)
	}
	if code, _ := ping("/api/v1/ping/etl", strings.Repeat("x", hub.MaxPingBytes+1)); code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: %d", code)
	}
	if _, err := s.GetJobBySlug(context.Background(), "etl"); err == nil {
		t.Error("a rejected ping created the job")
	}
}

// Pings need a host's token, and a hub serves them only with --accept-pings.
func TestHubPingAuth(t *testing.T) {
	s := openStore(t, t.TempDir())
	off := httptest.NewServer(hub.Handler(s, time.Now, log.New(io.Discard, "", 0), nil))
	defer off.Close()
	on := httptest.NewServer(hub.Handler(s, time.Now, log.New(io.Discard, "", 0), hubPings(context.Background(), s, io.Discard)))
	defer on.Close()
	post := func(url, token string) int {
		req, _ := http.NewRequest(http.MethodPost, url+"/api/v1/ping/etl", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post(on.URL, ""); code != http.StatusUnauthorized {
		t.Errorf("no token: %d", code)
	}
	if code := post(on.URL, "nope"); code != http.StatusUnauthorized {
		t.Errorf("bad token: %d", code)
	}
	token, err := s.CreateHost(context.Background(), "nas", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if code := post(off.URL, token); code != http.StatusNotFound {
		t.Errorf("pings not accepted: %d", code)
	}
	resp, err := http.Get(on.URL + "/api/v1/ping/etl")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", resp.StatusCode)
	}
}

// A ping's alert is delivered after the response, by the hook in serve's
// environment.
func TestHubPingAlerts(t *testing.T) {
	hook, events := hookLog(t)
	t.Setenv(envOnFailure, hook)
	t.Setenv(envOnRecover, "")
	t.Setenv(envNotify, "")
	s := openStore(t, t.TempDir())
	ping := pingHub(t, s)
	if code, body := ping("/api/v1/ping/etl/fail", "upstream 503"); code != http.StatusNoContent {
		t.Fatalf("%d %s", code, body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for events()[0] == "" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := events(); len(got) != 1 || got[0] != "failed etl failed 1 upstream 503" {
		t.Errorf("events = %q", got)
	}
}
