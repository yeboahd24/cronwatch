package runner

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCaptureTruncatesWithoutStoppingChild(t *testing.T) {
	result, err := Execute(context.Background(), []string{"sh", "-c", "printf 123456789; printf abcdefghi >&2"}, nil, nil, 4)
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
	if !strings.HasPrefix(result.Combined, "1234\n") || !strings.HasSuffix(result.Combined, "\nfghi") {
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
	result, err := Execute(context.Background(), []string{"cronwatch-no-such-command"}, nil, nil, 1024)
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
	result, _ := Execute(context.Background(), []string{"sh", "-c", "kill -KILL $$"}, nil, nil, 1024)
	if result.ExitCode != 128+9 {
		t.Fatalf("exit code = %d", result.ExitCode)
	}
}

func TestCancelStopsGrandchildren(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	// The grandchild sleep holds the output pipe open; without process-group
	// termination Execute would wait for WaitDelay or the full sleep.
	result, _ := Execute(ctx, []string{"sh", "-c", "sleep 30; :"}, nil, nil, 1024)
	if result.Status != "cancelled" {
		t.Fatalf("status = %s", result.Status)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("cancel took %s", elapsed)
	}
}
