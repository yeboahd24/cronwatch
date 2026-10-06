package app

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yeboahd24/cronwatch/internal/runner"
)

// runRules decide whether a run succeeded beyond its exit code, for scripts
// that exit 0 when they fail or use nonzero codes for success.
type runRules struct {
	OKCodes      []int          // exit codes besides 0 that count as success
	FailOnStderr bool           // any non-blank stderr fails the run
	FailMatch    *regexp.Regexp // output matching this fails the run
	SuccessMatch *regexp.Regexp // output must match this to succeed
	Timeout      time.Duration  // 0 means none
}

// parseOKCodes parses "0,3,4".
func parseOKCodes(v string) ([]int, error) {
	var codes []int
	for f := range strings.SplitSeq(v, ",") {
		code, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || code < 0 || code > 255 {
			return nil, fmt.Errorf("--ok-codes: %q is not an exit code (0-255)", f)
		}
		codes = append(codes, code)
	}
	return codes, nil
}

// outputSeen is what the output rules found. It is collected line by line
// while the command runs, so dropping the middle of a long log to fit
// --max-log-bytes cannot hide a match or a write to stderr.
type outputSeen struct {
	stderr     bool   // a non-blank line was written to stderr
	stderrLine string // the first one
	failed     bool   // a line matched FailMatch
	failLine   string // the first one
	succeeded  bool   // a line matched SuccessMatch
}

// watch returns a runner.LineFunc that records into seen what the rules look
// for. Patterns are matched against one line of stdout or stderr at a time,
// without its newline or a trailing carriage return, so ^ and $ anchor to the
// line and a pattern never spans lines.
func (r runRules) watch(seen *outputSeen) runner.LineFunc {
	return func(line []byte, stderr bool) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if stderr && !seen.stderr && len(bytes.TrimSpace(line)) > 0 {
			seen.stderr, seen.stderrLine = true, brief(line)
		}
		if r.FailMatch != nil && !seen.failed && r.FailMatch.Match(line) {
			seen.failed, seen.failLine = true, brief(line)
		}
		if r.SuccessMatch != nil && !seen.succeeded && r.SuccessMatch.Match(line) {
			seen.succeeded = true
		}
	}
}

// judge returns the run's status and, when the exit code alone would not
// explain it, the reason. Cancelled runs are left as they are.
func (r runRules) judge(res runner.Result, seen outputSeen) (status, reason string) {
	switch res.Status {
	case "cancelled":
		return res.Status, ""
	case "timeout":
		return res.Status, fmt.Sprintf("timed out after %s (--timeout)", r.Timeout)
	}
	if res.ExitCode != 0 && !slices.Contains(r.OKCodes, res.ExitCode) {
		return "failed", ""
	}
	if r.FailOnStderr && seen.stderr {
		return "failed", "wrote to stderr (--fail-on-stderr): " + seen.stderrLine
	}
	if r.FailMatch != nil && seen.failed {
		return "failed", fmt.Sprintf("output matched --fail-if-match %q: %s", r.FailMatch, seen.failLine)
	}
	if r.SuccessMatch != nil && !seen.succeeded {
		return "failed", fmt.Sprintf("output did not match --success-if-match %q", r.SuccessMatch)
	}
	if res.ExitCode != 0 {
		return "success", fmt.Sprintf("exit code %d is listed in --ok-codes", res.ExitCode)
	}
	return "success", ""
}

// brief trims a line of output to a readable length for a reason.
func brief(line []byte) string {
	s := strings.TrimSpace(string(line[:min(len(line), 1024)]))
	if len(s) > 200 {
		s = strings.ToValidUTF8(s[:200], "") + "…"
	}
	return s
}

// exitCode is what "cronwatch run" exits with. By default it is the command's
// own code, so cron sees what it saw before; strict makes it follow the
// recorded status instead.
func exitCode(status string, commandCode int, strict bool) int {
	switch {
	case status == "skipped":
		return 0
	case !strict:
		return commandCode
	case status == "success":
		return 0
	case commandCode != 0:
		return commandCode
	default:
		return 1
	}
}

// errRuleFailed is returned when a run fails only because of a rule.
var errRuleFailed = errors.New("run marked failed")
