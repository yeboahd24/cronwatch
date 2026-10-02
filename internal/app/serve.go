package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/yeboahd24/cronwatch/internal/config"
	"github.com/yeboahd24/cronwatch/internal/httpserver"
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
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", "127.0.0.1:8765", "HTTP listen address")
	public := fs.Bool("public", false, "allow non-loopback bind")
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
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
	web, err := httpserver.New(s, httpserver.Options{AnyHost: *public})
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
	go maintenanceLoop(ctx, done, s, stderr)
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = server.Shutdown(shutdownCtx)
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

// maintenanceLoop periodically closes out runs whose cronwatch process died
// and records missed occurrences, so missed runs are detected even when nobody
// is looking at the UI.
func maintenanceLoop(ctx context.Context, done <-chan struct{}, s *storage.Store, stderr io.Writer) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := maintain(ctx, s); err != nil && ctx.Err() == nil {
			fmt.Fprintln(stderr, "maintenance:", err)
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

// maintain reaps abandoned runs, then records missed occurrences. Reaping
// first means a dead run's status is settled before missed runs are judged.
func maintain(ctx context.Context, s *storage.Store) error {
	if _, err := s.ReapAbandonedRuns(ctx); err != nil {
		return fmt.Errorf("reap abandoned runs: %w", err)
	}
	if _, err := s.DetectMissed(ctx, time.Now()); err != nil {
		return fmt.Errorf("detect missed runs: %w", err)
	}
	return nil
}
