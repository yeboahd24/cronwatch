package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/yeboahd24/cronwatch/internal/config"
	"github.com/yeboahd24/cronwatch/internal/httpserver"
	"github.com/yeboahd24/cronwatch/internal/hub"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

func loopbackAddress(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func serveCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("serve", "cronwatch serve [flags]",
		"Serve the read-only dashboard, register jobs from your crontab, and check for missed runs.")
	addr := fs.String("addr", "127.0.0.1:8765", "HTTP listen address")
	public := fs.Bool("public", false, "allow non-loopback bind")
	metrics := fs.Bool("metrics", false, "serve Prometheus metrics at /metrics")
	dataDir := fs.String("data-dir", "", "data directory")
	syncTab := fs.Bool("sync-crontab", true, "register jobs found in the user's crontab")
	hubAddr := fs.String("hub-addr", "", "act as a hub: accept reports from other servers on this `address`, such as :8766")
	hubCert := fs.String("hub-cert", "", "TLS certificate `file` (PEM) for --hub-addr; required unless it is a loopback address")
	hubKey := fs.String("hub-key", "", "TLS key `file` (PEM) for --hub-addr")
	acceptPings := fs.Bool("accept-pings", false, "with --hub-addr, also record pings that hosts send to "+hub.PingPath+"SLUG as this machine's jobs")
	var rc reportConfig
	fs.StringVar(&rc.to, "report-to", "", "report this server's jobs every minute to the hub at this `URL`, such as https://hub.example:8766")
	fs.StringVar(&rc.tokenFile, "report-token-file", "", "read the hub token from this `file` (default $"+envReportToken+")")
	fs.StringVar(&rc.ca, "report-ca", "", "trust the hub certificate in this PEM `file` instead of the system's")
	if err := parseFlags(fs, args, stdout); err != nil {
		return err
	}
	if (*hubCert == "") != (*hubKey == "") {
		return errors.New("--hub-cert and --hub-key go together")
	}
	if *hubAddr != "" && *hubCert == "" && !loopbackAddress(*hubAddr) {
		return errors.New("--hub-addr on a non-loopback address needs --hub-cert and --hub-key, so tokens are not sent in the clear; behind a reverse proxy that terminates TLS, use a loopback address")
	}
	if *acceptPings && *hubAddr == "" {
		return errors.New("--accept-pings needs --hub-addr")
	}
	var report *reporter
	if rc.to != "" {
		client, err := rc.client(stderr)
		if err != nil {
			return err
		}
		report = &reporter{client: client, stderr: stderr}
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected serve arguments")
	}
	if !loopbackAddress(*addr) && !*public {
		return errors.New("non-loopback address requires --public")
	}
	if !loopbackAddress(*addr) {
		fmt.Fprintln(stderr, "WARNING: CronWatch is listening on a non-loopback interface. The web UI has no authentication. Protect this port with a firewall or reverse proxy authentication.")
	}
	if *dataDir == "" {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		*dataDir = cfg.DataDir
	}
	s, err := storage.Open(ctx, *dataDir)
	if err != nil {
		return err
	}
	defer s.Close()
	web, err := httpserver.New(s, httpserver.Options{AnyHost: *public, Metrics: *metrics, Version: Version})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: web.Router, ReadHeaderTimeout: 5 * time.Second}
	fmt.Fprintf(stdout, "CronWatch UI: http://%s\n", listener.Addr())
	done := make(chan struct{})
	var hubServer *http.Server
	if *hubAddr != "" {
		var pings hub.PingFunc
		if *acceptPings {
			pings = hubPings(ctx, s, stderr)
		}
		hubServer = &http.Server{Handler: hub.Handler(s, time.Now, log.New(stderr, "", 0), pings), ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
		// A bad certificate is a mistake to fix now; an address that is not
		// up yet, such as a VPN's at boot, is waited for.
		if *hubCert != "" {
			cert, err := tls.LoadX509KeyPair(*hubCert, *hubKey)
			if err != nil {
				return fmt.Errorf("--hub-cert and --hub-key: %w", err)
			}
			hubServer.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		}
		go serveHub(ctx, done, hubServer, *hubAddr, *acceptPings, stdout, stderr)
	}
	go maintenanceLoop(ctx, done, s, *syncTab, report, stderr)
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = server.Shutdown(shutdownCtx)
			if hubServer != nil {
				_ = hubServer.Shutdown(shutdownCtx)
			}
			cancel()
		case <-done:
		}
	}()
	err = server.Serve(listener)
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// maintenanceLoop periodically registers crontab jobs, closes out runs whose
// cronwatch process died and records missed occurrences, so the dashboard is
// current even when nobody is looking at it.
// hubRetryEvery is how often serve retries a hub address it cannot listen
// on yet. Tests shorten it.
var hubRetryEvery = 10 * time.Second

// serveHub accepts reports on addr until done, retrying until it can listen
// there, so the rest of serve runs while, for example, a VPN address that
// --hub-addr names is still coming up at boot.
func serveHub(ctx context.Context, done <-chan struct{}, server *http.Server, addr string, pings bool, stdout, stderr io.Writer) {
	failed := ""
	for {
		listener, err := net.Listen("tcp", addr)
		if err == nil {
			scheme := "http"
			if server.TLSConfig != nil {
				scheme = "https"
				listener = tls.NewListener(listener, server.TLSConfig)
			}
			fmt.Fprintf(stdout, "CronWatch hub: accepting reports at %s://%s%s\n", scheme, listener.Addr(), hub.ReportPath)
			if pings {
				fmt.Fprintf(stdout, "CronWatch hub: accepting pings at %s://%s%sSLUG\n", scheme, listener.Addr(), hub.PingPath)
			}
			if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				fmt.Fprintln(stderr, "hub:", err)
			}
			return
		}
		if err.Error() != failed {
			failed = err.Error()
			fmt.Fprintf(stderr, "hub: cannot accept reports yet: %v (retrying every %s)\n", err, shortDuration(hubRetryEvery))
		}
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-time.After(hubRetryEvery):
		}
	}
}

func maintenanceLoop(ctx context.Context, done <-chan struct{}, s *storage.Store, syncTab bool, report *reporter, stderr io.Writer) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	lastProblems := ""
	for {
		if syncTab {
			result, err := syncFromUserCrontab(ctx, s)
			switch {
			case errors.Is(err, errNoCrontabCommand):
				syncTab = false
			case err != nil && ctx.Err() == nil:
				fmt.Fprintln(stderr, "crontab sync:", err)
			case err == nil:
				for _, name := range result.Added {
					fmt.Fprintf(stderr, "crontab sync: added %s\n", name)
				}
				for _, name := range result.Updated {
					fmt.Fprintf(stderr, "crontab sync: updated %s\n", name)
				}
				for _, name := range result.Unscheduled {
					fmt.Fprintf(stderr, "crontab sync: unscheduled %s, which is no longer in the crontab\n", name)
				}
				if n := len(result.Changes); n > 0 {
					fmt.Fprintf(stderr, "crontab history: recorded %s\n", plural(n, "change", "changes"))
				}
				// Report unreadable lines when they change, not every minute.
				if problems := strings.Join(result.Problems, "; "); problems != lastProblems {
					if problems != "" {
						fmt.Fprintln(stderr, "crontab sync: could not read:", problems)
					}
					lastProblems = problems
				}
			}
		}
		if err := maintain(ctx, s, stderr); err != nil && ctx.Err() == nil {
			fmt.Fprintln(stderr, "maintenance:", err)
		}
		// Report after maintenance, so missed runs are up to date.
		if report != nil {
			report.send(ctx, s)
		}
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
		}
	}
}

// maintain reaps abandoned runs and times out heartbeat runs past their
// --max-duration, then records missed occurrences, queues the --on-failure
// alerts of jobs that just started missing runs, and delivers every job's due
// alerts. Settling dead and overdue runs first means their status is known
// before missed runs are judged.
func maintain(ctx context.Context, s *storage.Store, stderr io.Writer) error {
	runs := lifecycle{s: s, stderr: stderr}
	if err := runs.reapAbandoned(ctx); err != nil {
		return fmt.Errorf("reap abandoned runs: %w", err)
	}
	runs.expireHeartbeats(ctx, "", time.Now())
	// Failures recorded before signatures existed are signed a batch at a time.
	if _, err := s.BackfillFailureSignatures(ctx, 500); err != nil {
		return fmt.Errorf("sign earlier failures: %w", err)
	}
	missed, err := s.DetectMissedJobs(ctx, time.Now())
	notifyMissed(ctx, s, missed, stderr)
	// Deliver the alerts just queued and retry earlier ones that failed.
	deliverAlerts(ctx, s, "", stderr)
	if err != nil {
		return fmt.Errorf("detect missed runs: %w", err)
	}
	// cronwatch doctor reports when missed runs were last checked.
	if err := s.SetMaintained(ctx, time.Now()); err != nil {
		return fmt.Errorf("record the missed-run check: %w", err)
	}
	return nil
}

func syncFromUserCrontab(ctx context.Context, s *storage.Store) (syncResult, error) {
	text, err := readUserCrontab(ctx)
	if err != nil {
		return syncResult{}, err
	}
	return syncUserCrontab(ctx, s, text)
}
