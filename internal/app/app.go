package app

import (
	"context"
	"fmt"
	"io"
)

var Version = "0.1.0-dev"

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
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
	case "help", "-h", "--help":
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
  cronwatch version`)
}
