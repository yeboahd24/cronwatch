package app

import (
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

// judge returns the run's status and, when the exit code alone would not
// explain it, the reason. Cancelled runs are left as they are.
func (r runRules) judge(res runner.Result) (status, reason string) {
	switch res.Status {
	case "cancelled":
		return res.Status, ""
	case "timeout":
		return res.Status, fmt.Sprintf("timed out after %s (--timeout)", r.Timeout)
	}
	if res.ExitCode != 0 && !slices.Contains(r.OKCodes, res.ExitCode) {
		return "failed", ""
	}
	if r.FailOnStderr && strings.TrimSpace(res.Stderr) != "" {
		return "failed", "wrote to stderr (--fail-on-stderr): " + firstLine(res.Stderr, nil)
	}
	output := res.Stdout + "\n" + res.Stderr
	if r.FailMatch != nil && r.FailMatch.MatchString(output) {
		return "failed", fmt.Sprintf("output matched --fail-if-match %q: %s", r.FailMatch, firstLine(output, r.FailMatch))
	}
	if r.SuccessMatch != nil && !r.SuccessMatch.MatchString(output) {
		return "failed", fmt.Sprintf("output did not match --success-if-match %q", r.SuccessMatch)
	}
	if res.ExitCode != 0 {
		return "success", fmt.Sprintf("exit code %d is listed in --ok-codes", res.ExitCode)
	}
	return "success", ""
}

// firstLine returns the first non-blank line of text, or the first that
// matches re, trimmed to a readable length.
func firstLine(text string, re *regexp.Regexp) string {
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || re != nil && !re.MatchString(line) {
			continue
		}
		if len(line) > 200 {
			line = line[:200] + "…"
		}
		return line
	}
	return ""
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
