package app

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yeboahd24/cronwatch/internal/hub"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

var tokenPattern = regexp.MustCompile(`cwh_[A-Za-z0-9_-]+`)

func TestHostsCommand(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	hosts := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := Run(ctx, append([]string{"hosts"}, args...), &out, &bytes.Buffer{})
		return out.String(), err
	}
	// Flags may come before or after the subcommand's arguments.
	out, err := hosts("add", "web-1", "--data-dir", dir)
	token := tokenPattern.FindString(out)
	if err != nil || token == "" || !strings.Contains(out, "shown only now") {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if _, err := hosts("--data-dir", dir, "add", "web-1"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("a second add: %v", err)
	}
	for _, bad := range [][]string{{"add", "Web 1"}, {"add"}, {"frobnicate"}, {"remove", "nope"}, {"token", "nope"}} {
		if _, err := hosts(append(bad, "--data-dir", dir)...); err == nil {
			t.Errorf("hosts %v accepted", bad)
		}
	}
	if out, _ := hosts("list", "--data-dir", dir); !strings.Contains(out, "web-1  never") {
		t.Fatalf("list:\n%s", out)
	}
	out, err = hosts("token", "web-1", "--data-dir", dir)
	fresh := tokenPattern.FindString(out)
	if err != nil || fresh == "" || fresh == token {
		t.Fatalf("token: %v\n%s", err, out)
	}
	s := openStore(t, dir)
	if _, err := s.HostByToken(ctx, token); err == nil {
		t.Fatal("the old token still works")
	}
	if _, err := s.HostByToken(ctx, fresh); err != nil {
		t.Fatal("the new token does not work")
	}
	if _, err := hosts("remove", "web-1", "--data-dir", dir); err != nil {
		t.Fatal(err)
	}
	if out, _ := hosts("--data-dir", dir); !strings.Contains(out, "No hosts report here yet") {
		t.Fatalf("list after remove:\n%s", out)
	}
}

// tlsHub starts a hub on TLS and returns its URL, a file with its
// certificate, and a token for host web-1.
func tlsHub(t *testing.T) (string, string, string, *storage.Store) {
	t.Helper()
	s := openStore(t, t.TempDir())
	token, err := s.CreateHost(context.Background(), "web-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(hub.Handler(s, time.Now, log.New(io.Discard, "", 0)))
	t.Cleanup(server.Close)
	ca := filepath.Join(t.TempDir(), "hub.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return server.URL, ca, token, s
}

func TestReportCommand(t *testing.T) {
	ctx := context.Background()
	url, ca, token, hubStore := tlsHub(t)
	dir := t.TempDir()
	if err := Run(ctx, []string{"run", "--data-dir", dir, "--name", "Backup", "--", "sh", "-c", "echo disk full >&2; exit 1"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("the failing job succeeded")
	}
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := Run(ctx, []string{"report", "--data-dir", dir, "--to", url, "--token-file", tokenFile, "--ca", ca}, &out, &errOut); err != nil {
		t.Fatalf("report: %v\n%s", err, errOut.String())
	}
	// A token file others can read is pointed out.
	if !strings.Contains(out.String(), "Reported 1 job to") || !strings.Contains(errOut.String(), "can be read by other users") {
		t.Fatalf("stdout %q, stderr %q", out.String(), errOut.String())
	}
	h, err := hubStore.HostByName(ctx, "web-1")
	if err != nil {
		t.Fatal(err)
	}
	var r hub.Report
	if err := json.Unmarshal(h.Report, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Jobs) != 1 || r.Jobs[0].Status != "failed" || r.Jobs[0].LastRun.LastError != "disk full" {
		t.Fatalf("the hub got %+v", r)
	}
	// The token can come from the environment instead.
	t.Setenv(envReportToken, token)
	if err := Run(ctx, []string{"report", "--data-dir", dir, "--to", url, "--ca", ca}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("report with the token in the environment: %v", err)
	}
	t.Setenv(envReportToken, "cwh_wrong")
	if err := Run(ctx, []string{"report", "--data-dir", dir, "--to", url, "--ca", ca}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("a wrong token: %v", err)
	}
}

func TestReporterLogsOnlyChanges(t *testing.T) {
	ctx := context.Background()
	url, ca, token, _ := tlsHub(t)
	s := openStore(t, t.TempDir())
	var logged bytes.Buffer
	bad, err := hub.NewClient(url, "cwh_wrong", ca)
	if err != nil {
		t.Fatal(err)
	}
	r := &reporter{client: bad, stderr: &logged}
	r.send(ctx, s)
	r.send(ctx, s)
	if n := strings.Count(logged.String(), "hub report: "); n != 1 {
		t.Fatalf("%d log lines for the same failure:\n%s", n, logged.String())
	}
	r.client, _ = hub.NewClient(url, token, ca)
	r.send(ctx, s)
	r.send(ctx, s)
	if !strings.HasSuffix(logged.String(), "hub report: delivered again\n") || strings.Count(logged.String(), "\n") != 2 {
		t.Fatalf("log:\n%s", logged.String())
	}
}

func TestServeChecksHubFlags(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--hub-addr", "0.0.0.0:0"}, "needs --hub-cert and --hub-key"},
		{[]string{"--hub-cert", "c.pem"}, "go together"},
		{[]string{"--hub-addr", "127.0.0.1:0", "--hub-cert", "missing.pem", "--hub-key", "missing.key"}, "--hub-cert and --hub-key:"},
		{[]string{"--report-to", "http://hub.example:8766"}, "must use https"},
		{[]string{"--report-to", "https://hub.example:8766"}, "no hub token"},
	} {
		err := Run(context.Background(), append([]string{"serve", "--data-dir", dir, "--addr", "127.0.0.1:0", "--sync-crontab=false"}, tc.args...), &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("serve %v: %v, want %q", tc.args, err, tc.want)
		}
	}
}

// syncBuffer is a bytes.Buffer safe for concurrent writes and reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestServeWaitsForTheHubAddress(t *testing.T) {
	saved := hubRetryEvery
	hubRetryEvery = 20 * time.Millisecond
	t.Cleanup(func() { hubRetryEvery = saved })
	dir := t.TempDir()
	token, err := openStore(t, dir).CreateHost(context.Background(), "web-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Something else holds the address at first, as when a VPN is not up yet.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := busy.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	var stdout, stderr syncBuffer
	served := make(chan error, 1)
	go func() {
		served <- Run(ctx, []string{"serve", "--data-dir", dir, "--addr", "127.0.0.1:0", "--sync-crontab=false", "--hub-addr", addr}, &stdout, &stderr)
	}()
	waitUntil := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("%s; stdout %q, stderr %q", what, stdout.String(), stderr.String())
			}
		}
	}
	// The dashboard serves meanwhile, and the wait is logged once.
	waitUntil("the dashboard did not start", func() bool { return strings.Contains(stdout.String(), "CronWatch UI:") })
	waitUntil("the wait was not logged", func() bool { return strings.Contains(stderr.String(), "cannot accept reports yet") })
	time.Sleep(100 * time.Millisecond)
	if n := strings.Count(stderr.String(), "cannot accept reports yet"); n != 1 {
		t.Fatalf("logged the same wait %d times", n)
	}
	busy.Close()
	waitUntil("the hub did not start", func() bool { return strings.Contains(stdout.String(), "accepting reports at http://"+addr) })
	client, err := hub.NewClient("http://"+addr, token, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Send(ctx, hub.Report{Version: hub.ReportVersion, Jobs: []hub.Job{}}); err != nil {
		t.Fatalf("report after the wait: %v", err)
	}
	cancel()
	if err := <-served; err != nil {
		t.Fatal(err)
	}
}
