package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

var Version = "0.1.0-dev"

// errHelpShown is returned once a command has printed its --help text; Run
// reports it as success.
var errHelpShown = errors.New("help shown")

// newFlagSet returns a flag set whose --help prints synopsis, about and the
// flags. Parse errors are returned, not printed, so they appear once.
func newFlagSet(name, synopsis, about string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {
		w := fs.Output()
		fmt.Fprintf(w, "Usage: %s\n\n%s\n", synopsis, about)
		hasFlags := false
		fs.VisitAll(func(*flag.Flag) { hasFlags = true })
		if hasFlags {
			fmt.Fprintln(w, "\nFlags:")
			fs.PrintDefaults()
		}
	}
	return fs
}

// parseFlags parses args, printing help to stdout for -h/--help.
func parseFlags(fs *flag.FlagSet, args []string, stdout io.Writer) error {
	err := fs.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		fs.SetOutput(stdout)
		fs.Usage()
		return errHelpShown
	}
	return err
}

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	err := dispatch(ctx, args, stdout, stderr)
	if errors.Is(err, errHelpShown) {
		return nil
	}
	return err
}

func dispatch(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printHelp(stdout)
		return nil
	}

	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, Version)
		return nil
	case "run":
		return runCommand(ctx, args[1:], stdout, stderr)
	case "serve":
		return serveCommand(ctx, args[1:], stdout, stderr)
	case "jobs":
		return jobsCommand(ctx, args[1:], stdout)
	case "runs":
		return runsCommand(ctx, args[1:], stdout)
	case "prune":
		return pruneCommand(ctx, args[1:], stdout)
	case "sync":
		return syncCommand(ctx, args[1:], stdout)
	case "envdiff":
		return envdiffCommand(ctx, args[1:], stdout)
	case "try":
		return tryCommand(ctx, args[1:], stdout, stderr)
	case "digest":
		return digestCommand(ctx, args[1:], stdout)
	case "crontab-history":
		return crontabHistoryCommand(ctx, args[1:], stdout)
	case "check":
		return checkCommand(ctx, args[1:], stdout)
	case "ping":
		return pingCommand(ctx, args[1:], stdout, stderr)
	case "timers":
		return timersCommand(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		// "cronwatch help run" shows the help for one command.
		if len(args) > 1 && args[1] != "help" && !strings.HasPrefix(args[1], "-") {
			return dispatch(ctx, []string{args[1], "--help"}, stdout, stderr)
		}
		printHelp(stdout)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func printHelp(w io.Writer) {
	fmt.Fprintln(w, `CronWatch - local cron/background-job monitor

Usage:
  cronwatch run [flags] -- command [args...]
  cronwatch serve [flags]
  cronwatch jobs
  cronwatch runs [job]
  cronwatch prune [--keep N] [--older-than DURATION]
  cronwatch sync [--crontab FILE]
  cronwatch envdiff [--last-success] JOB-SLUG | --run RUN-ID
  cronwatch try JOB-SLUG | --run RUN-ID
  cronwatch digest [--since DURATION] [--quiet]
  cronwatch crontab-history [--limit N] [JOB-SLUG]
  cronwatch check [JOB-SLUG...]
  cronwatch ping [--start | --fail] JOB-SLUG
  cronwatch timers [--all] [--json]
  cronwatch version

Run "cronwatch COMMAND --help" for a command's flags.`)
}
