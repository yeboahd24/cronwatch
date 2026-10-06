package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/yeboahd24/cronwatch/internal/logs"
)

func TestCaptureTruncatesWithoutStoppingChild(t *testing.T) {
	result, err := Execute(context.Background(), []string{"sh", "-c", "printf 123456789; printf abcdefghi >&2"}, Options{MaxLogBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "success" || result.ExitCode != 0 || !result.Truncated {
		t.Fatalf("result = %+v", result)
	}
	// Head and tail are kept; the middle is replaced by a marker.
	if !strings.HasPrefix(result.Stdout, "12\n") || !strings.HasSuffix(result.Stdout, "\n89") || !strings.Contains(result.Stdout, "5 bytes truncated") {
		t.Fatalf("stdout = %q", result.Stdout)
	}
	if !strings.HasPrefix(result.Stderr, "ab\n") || !strings.HasSuffix(result.Stderr, "\nhi") {
		t.Fatalf("stderr = %q", result.Stderr)
	}
	// Combined is "123456789\n" + StderrMark + "abcdefghi\n", capped at 8 bytes.
	if !strings.HasPrefix(result.Combined, "1234\n") || !strings.HasSuffix(result.Combined, "\nghi\n") {
		t.Fatalf("combined = %q", result.Combined)
	}
}

func TestBoundedBuffer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		max    int64
		writes []string
		want   string
	}{
		{"fits", 10, []string{"hello"}, "hello"},
		{"exact", 4, []string{"ab", "cd"}, "abcd"},
		{"many small writes wrap the ring", 6, []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}, "abc\n... [cronwatch: 3 bytes truncated] ...\nghi"},
		{"one large write", 6, []string{"abcdefghij"}, "abc\n... [cronwatch: 4 bytes truncated] ...\nhij"},
		{"zero captures nothing", 0, []string{"abc"}, ""},
		// "é" is 2 bytes and "€" is 3; cuts must not split them.
		{"utf8 head cut", 6, []string{"aaé", "€xyz"}, "aa\n... [cronwatch: 5 bytes truncated] ...\nxyz"},
		{"utf8 head ends on full rune", 10, []string{"€é", "zzzzzzzzz"}, "€é\n... [cronwatch: 4 bytes truncated] ...\nzzzzz"},
		{"utf8 tail cut", 6, []string{"abc", "x€yz"}, "abc\n... [cronwatch: 4 bytes truncated] ...\nyz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBoundedBuffer(tc.max)
			for _, w := range tc.writes {
				b.Write([]byte(w))
			}
			if got := b.String(); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStartFailureIsRecorded(t *testing.T) {
	result, err := Execute(context.Background(), []string{"cronwatch-no-such-command"}, Options{MaxLogBytes: 1024})
	if err == nil {
		t.Fatal("expected error")
	}
	if result.Status != "failed" || result.ExitCode != 127 {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(result.Stderr, "cronwatch-no-such-command") || !strings.Contains(result.Combined, "cronwatch-no-such-command") {
		t.Fatalf("start error not logged: %+v", result)
	}
}

func TestSignalExitCode(t *testing.T) {
	result, _ := Execute(context.Background(), []string{"sh", "-c", "kill -KILL $$"}, Options{MaxLogBytes: 1024})
	if result.ExitCode != 128+9 {
		t.Fatalf("exit code = %d", result.ExitCode)
	}
}

func TestCancelStopsGrandchildren(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(200*time.Millisecond, cancel)
	started := time.Now()
	// The grandchild sleep holds the output pipe open; without process-group
	// termination Execute would wait for WaitDelay or the full sleep.
	result, _ := Execute(ctx, []string{"sh", "-c", "sleep 30; :"}, Options{MaxLogBytes: 1024})
	if result.Status != "cancelled" {
		t.Fatalf("status = %s", result.Status)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("cancel took %s", elapsed)
	}
}

func TestDeadlineIsTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	result, err := Execute(ctx, []string{"sh", "-c", "sleep 30; :"}, Options{MaxLogBytes: 1024})
	if err == nil || result.Status != "timeout" || result.ExitCode != 128+15 {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestUsageIsRecorded(t *testing.T) {
	result, err := Execute(context.Background(), []string{"sh", "-c", "true"}, Options{MaxLogBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage == nil || result.Usage.MaxRSSKB <= 0 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	missing, _ := Execute(context.Background(), []string{"cronwatch-no-such-command"}, Options{MaxLogBytes: 1024})
	if missing.Usage != nil {
		t.Fatalf("a command that never started has usage %+v", missing.Usage)
	}
}

func TestCombinedLogTagsStderrLines(t *testing.T) {
	result, err := Execute(context.Background(), []string{"sh", "-c", "echo out1; echo err1 >&2; printf 'tail-no-newline'"}, Options{MaxLogBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	lines := logs.Parse(result.Combined)
	got := map[string]bool{}
	for _, l := range lines {
		got[l.Text] = l.IsStderr()
	}
	if len(lines) != 3 || got["out1"] || !got["err1"] || got["tail-no-newline"] {
		t.Fatalf("lines = %+v", lines)
	}
	if result.Stdout != "out1\ntail-no-newline" || result.Stderr != "err1\n" {
		t.Fatalf("streams = %q / %q", result.Stdout, result.Stderr)
	}
}

func TestLinesAreSeenBeforeTruncation(t *testing.T) {
	// The streams are separate pipes, so only the order within each is fixed.
	var stdout, stderr []string
	onLine := func(line []byte, isStderr bool) {
		if isStderr {
			stderr = append(stderr, string(line))
		} else {
			stdout = append(stdout, string(line))
		}
	}
	// Nothing is kept with a zero limit, but every line is still seen.
	script := "echo a; echo b >&2; printf c"
	if _, err := Execute(context.Background(), []string{"sh", "-c", script}, Options{OnLine: onLine}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(stdout, []string{"a", "c"}) || !slices.Equal(stderr, []string{"b"}) {
		t.Fatalf("lines = %q / %q", stdout, stderr)
	}
}

func TestLongLineWithoutNewlineIsBounded(t *testing.T) {
	c := &capture{stdout: newBoundedBuffer(1 << 20), stderr: newBoundedBuffer(1 << 20), combined: newBoundedBuffer(2 << 20)}
	s := &stream{c: c, dst: c.stdout}
	chunk := []byte(strings.Repeat("x", 1000))
	for range 200 {
		_, _ = s.Write(chunk)
		if len(s.pending) >= maxPendingLine {
			t.Fatalf("pending grew to %d", len(s.pending))
		}
	}
}

func TestTimeoutKillsGrandchildrenThatIgnoreSIGTERM(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	pidFile := filepath.Join(t.TempDir(), "pid")
	// A background grandchild that ignores SIGTERM and records its PID.
	script := "(trap '' TERM; exec sh -c 'echo $$ > " + pidFile + "; exec sleep 30') >/dev/null 2>&1 & wait"
	if _, err := Execute(ctx, []string{"sh", "-c", script}, Options{MaxLogBytes: 1024}); err == nil {
		t.Fatal("expected an error")
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatal("grandchild that ignored SIGTERM is still running")
		}
	}
}

func TestProgressReportsNewOutputWhileRunning(t *testing.T) {
	var mu sync.Mutex
	var seen []Output
	opts := Options{MaxLogBytes: 1024, ProgressEvery: 20 * time.Millisecond, Progress: func(o Output) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, o)
	}}
	// Output, a quiet spell, more output, then another quiet spell.
	result, err := Execute(context.Background(), []string{"sh", "-c", "echo one; sleep 0.3; echo two >&2; sleep 0.3"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	// Execute has returned, so no report is in progress.
	// Quiet spells are not reported again, so there are two reports.
	if len(seen) != 2 || seen[0].Combined != "one\n" || seen[1].Stderr != "two\n" || seen[1].Combined != "one\n\x02two\n" {
		t.Fatalf("progress = %+v", seen)
	}
	if result.Combined != seen[1].Combined {
		t.Fatalf("final output %q differs from the last report", result.Combined)
	}

	// Large output is reported less often: once, then not for an hour.
	seen = nil
	opts.ProgressAfter, opts.ProgressLarge = time.Hour, 10
	if _, err := Execute(context.Background(), []string{"sh", "-c", "for i in 1 2 3 4 5 6 7 8; do echo line $i; sleep 0.05; done"}, opts); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 {
		t.Fatalf("%d reports of large output, want 1 before the slower interval", len(seen))
	}
}
