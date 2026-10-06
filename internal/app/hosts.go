package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/yeboahd24/cronwatch/internal/hub"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

// envReportToken holds a hub token when --report-token-file is not passed.
const envReportToken = "CRONWATCH_REPORT_TOKEN"

// hostName is a host's name on the hub.
var hostName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

func hostsCommand(ctx context.Context, args []string, stdout io.Writer) error {
	usage := "cronwatch hosts list | add NAME | token NAME | remove NAME"
	fs := newFlagSet("hosts", usage, "Manage the servers that report to this CronWatch as their hub. add prints a new host's token;\n"+
		"token replaces a host's token; remove deletes a host and its report.")
	dir := fs.String("data-dir", "", "data directory")
	// Flags may come anywhere, as in "hosts add web-1 --data-dir DIR".
	var words []string
	for {
		if err := parseFlags(fs, args, stdout); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		words, args = append(words, fs.Arg(0)), fs.Args()[1:]
	}
	sub, rest := "list", words
	if len(rest) > 0 {
		sub, rest = rest[0], rest[1:]
	}
	wantArgs := map[string]int{"list": 0, "add": 1, "token": 1, "remove": 1}
	n, known := wantArgs[sub]
	switch {
	case !known:
		return fmt.Errorf("unknown hosts command %q; usage: %s", sub, usage)
	case len(rest) != n:
		return fmt.Errorf("usage: %s", usage)
	case n == 1 && !hostName.MatchString(rest[0]):
		return fmt.Errorf("host name %q must be lowercase letters, digits, dots, dashes or underscores", rest[0])
	}
	s, err := openForList(ctx, *dir)
	if err != nil {
		return err
	}
	defer s.Close()
	switch sub {
	case "add":
		if _, err := s.HostByName(ctx, rest[0]); err == nil {
			return fmt.Errorf("host %s already exists; cronwatch hosts token %s gives it a new token", rest[0], rest[0])
		}
		token, err := s.CreateHost(ctx, rest[0], time.Now())
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Added host %s.\n", rest[0])
		printToken(stdout, rest[0], token)
	case "token":
		token, err := s.NewHostToken(ctx, rest[0])
		if errors.Is(err, storage.ErrNoHost) {
			return fmt.Errorf("no host named %s", rest[0])
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Gave %s a new token; the old one no longer works.\n", rest[0])
		printToken(stdout, rest[0], token)
	case "remove":
		if err := s.DeleteHost(ctx, rest[0]); errors.Is(err, storage.ErrNoHost) {
			return fmt.Errorf("no host named %s", rest[0])
		} else if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Removed %s and its report; its token no longer works.\n", rest[0])
	case "list":
		hosts, err := s.ListHosts(ctx)
		if err != nil {
			return err
		}
		if len(hosts) == 0 {
			fmt.Fprintln(stdout, "No hosts report here yet. cronwatch hosts add NAME adds one.")
			return nil
		}
		now := time.Now()
		w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSTATUS\tLAST REPORT\tJOBS\tVERSION")
		for _, h := range hosts {
			st := hub.Assess(h, now)
			status, last, jobs, version := st.Status, "never", "—", "—"
			if st.Status == "problems" {
				status = plural(st.Problems, "problem", "problems")
			}
			if h.ReportedAt != nil {
				last = shortDuration(st.Age.Round(time.Second)) + " ago"
			}
			if r := st.Report; r != nil {
				jobs, version = fmt.Sprint(len(r.Jobs)), r.Cronwatch
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", h.Name, status, last, jobs, version)
		}
		return w.Flush()
	}
	return nil
}

// printToken shows a token once, with how to use it on the host.
func printToken(w io.Writer, name, token string) {
	fmt.Fprintf(w, `
Its token, shown only now:

  %s

On %s, save it where only cronwatch's user can read it:

  install -m 600 /dev/null ~/.config/cronwatch/hub-token
  $EDITOR ~/.config/cronwatch/hub-token    # paste the token

then report to this hub with:

  cronwatch serve --report-to https://THIS-HUB:8766 --report-token-file ~/.config/cronwatch/hub-token
`, token, name)
}

// reportConfig is how a server reports to its hub.
type reportConfig struct {
	to, tokenFile, ca string
}

// client returns a hub client for the config, reading the token from its
// file or from $CRONWATCH_REPORT_TOKEN.
func (c reportConfig) client(stderr io.Writer) (*hub.Client, error) {
	token := os.Getenv(envReportToken)
	if c.tokenFile != "" {
		info, err := os.Stat(c.tokenFile)
		if err != nil {
			return nil, err
		}
		if info.Mode().Perm()&0o077 != 0 {
			fmt.Fprintf(stderr, "cronwatch: warning: %s can be read by other users; chmod 600 it\n", c.tokenFile)
		}
		b, err := os.ReadFile(c.tokenFile)
		if err != nil {
			return nil, err
		}
		token = strings.TrimSpace(string(b))
	}
	return hub.NewClient(c.to, token, c.ca)
}

// reporter sends reports to a hub, logging only when delivery starts or
// stops failing, so a hub that is down does not fill the log.
type reporter struct {
	client  *hub.Client
	stderr  io.Writer
	failing string // the last error, "" while reports are delivered
}

func (r *reporter) send(ctx context.Context, s *storage.Store) {
	report, err := hub.BuildReport(ctx, s, Version, time.Now())
	if err == nil {
		sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = r.client.Send(sendCtx, report)
		cancel()
	}
	switch {
	case err != nil && ctx.Err() != nil:
	case err != nil && err.Error() != r.failing:
		r.failing = err.Error()
		fmt.Fprintf(r.stderr, "hub report: %v (retrying every minute)\n", err)
	case err == nil && r.failing != "":
		r.failing = ""
		fmt.Fprintln(r.stderr, "hub report: delivered again")
	}
}

func reportCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("report", "cronwatch report --to URL [--token-file FILE] [--ca FILE]",
		"Send this server's job summary to its hub once. cronwatch serve --report-to does this every minute;\n"+
			"run this from cron on a server without serve. The token is read from --token-file or $"+envReportToken+".")
	dir := fs.String("data-dir", "", "data directory")
	var c reportConfig
	fs.StringVar(&c.to, "to", "", "the hub's `URL`, such as https://hub.example:8766")
	fs.StringVar(&c.tokenFile, "token-file", "", "read the hub token from this `file`")
	fs.StringVar(&c.ca, "ca", "", "trust the hub certificate in this PEM `file` instead of the system's")
	if err := parseFlags(fs, args, stdout); err != nil {
		return err
	}
	if fs.NArg() != 0 || c.to == "" {
		return errors.New("usage: cronwatch report --to URL [--token-file FILE] [--ca FILE]")
	}
	client, err := c.client(stderr)
	if err != nil {
		return err
	}
	s, err := openForList(ctx, *dir)
	if err != nil {
		return err
	}
	defer s.Close()
	report, err := hub.BuildReport(ctx, s, Version, time.Now())
	if err != nil {
		return err
	}
	if err := client.Send(ctx, report); err != nil {
		return fmt.Errorf("report to %s: %w", c.to, err)
	}
	fmt.Fprintf(stdout, "Reported %s to %s.\n", plural(len(report.Jobs), "job", "jobs"), c.to)
	return nil
}
