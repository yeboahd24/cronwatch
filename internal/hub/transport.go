package hub

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/yeboahd24/cronwatch/internal/storage"
)

// ReportPath is where a hub accepts reports.
const ReportPath = "/api/v1/report"

// Client sends reports to a hub.
type Client struct {
	url   string // the hub's report endpoint
	token string
	http  *http.Client
}

// NewClient returns a client for the hub at base, such as
// https://hub.example:8766. base must use HTTPS unless it is a loopback
// address, so the token is never sent in the clear. caFile, if not empty, is
// a PEM file of certificates to trust for the hub, such as its own
// self-signed one, in place of the system's.
func NewClient(base, token, caFile string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("hub address %q is not a URL like https://hub.example:8766", base)
	}
	if u.Scheme == "http" && !loopback(u.Hostname()) {
		return nil, fmt.Errorf("hub address %q must use https, so the token is not sent in the clear", base)
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("no hub token: pass --report-token-file or set CRONWATCH_REPORT_TOKEN")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s has no PEM certificates", caFile)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	u.Path += ReportPath
	return &Client{url: u.String(), token: strings.TrimSpace(token), http: &http.Client{Transport: transport, Timeout: 20 * time.Second}}, nil
}

// Send delivers a report.
func (c *Client) Send(ctx context.Context, r Report) error {
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	return fmt.Errorf("hub answered %s: %s", resp.Status, strings.TrimSpace(string(msg)))
}

// Handler accepts reports at ReportPath and stores each as its host's
// latest, and, if ping is not nil, pings at PingPath. It serves nothing
// else, so the port it listens on exposes only these. Both need a host's
// token. now is the hub's clock, which decides when a report arrived.
func Handler(s *storage.Store, now func() time.Time, logger *log.Logger, ping PingFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isPing := ping != nil && strings.HasPrefix(r.URL.Path, PingPath)
		if r.URL.Path != ReportPath && !isPing {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "use POST", http.StatusMethodNotAllowed)
			return
		}
		what := "a report"
		if isPing {
			what = "a ping"
		}
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		host, err := storage.Host{}, storage.ErrNoHost
		if ok && token != "" {
			host, err = s.HostByToken(r.Context(), token)
		}
		if err != nil {
			if !errors.Is(err, storage.ErrNoHost) {
				logger.Printf("hub: could not check a token: %v", err)
				http.Error(w, "try again later", http.StatusServiceUnavailable)
				return
			}
			logger.Printf("hub: rejected %s from %s with an unknown token", what, remoteHost(r))
			http.Error(w, "unknown token", http.StatusUnauthorized)
			return
		}
		if isPing {
			servePing(w, r, host.Name, ping)
			return
		}
		var report Report
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxReportBytes))
		if err := dec.Decode(&report); err != nil {
			http.Error(w, "the report is not valid JSON, or is over 1 MiB: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := report.Check(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		stored, err := json.Marshal(report)
		if err == nil {
			err = s.SaveHostReport(r.Context(), host.ID, now(), stored)
		}
		if err != nil {
			logger.Printf("hub: could not store %s's report: %v", host.Name, err)
			http.Error(w, "try again later", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func remoteHost(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
