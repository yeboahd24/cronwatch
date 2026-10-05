package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	check := func(args ...string) (int, string) {
		var out bytes.Buffer
		err := Run(ctx, append([]string{"check", "--data-dir", dir}, args...), &out, &bytes.Buffer{})
		if exit, ok := errors.AsType[*ExitError](err); ok {
			return exit.Code, out.String()
		} else if err != nil {
			t.Fatal(err)
		}
		return 0, out.String()
	}
	if code, out := check(); code != 0 || out != "CRONWATCH OK - 0 jobs ok | jobs=0 critical=0 warning=0 ok=0\n" {
		t.Fatalf("empty: %d %q", code, out)
	}
	_ = Run(ctx, []string{"run", "--name", "Good", "--data-dir", dir, "--", "true"}, &bytes.Buffer{}, &bytes.Buffer{})
	if code, out := check(); code != 0 || !strings.HasPrefix(out, "CRONWATCH OK - 1 job ok | jobs=1 critical=0") {
		t.Fatalf("one good job: %d %q", code, out)
	}
	_ = Run(ctx, []string{"run", "--name", "Bad", "--data-dir", dir, "--", "sh", "-c", "echo 'disk full' >&2; exit 1"}, &bytes.Buffer{}, &bytes.Buffer{})
	code, out := check()
	if code != 2 || !strings.HasPrefix(out, "CRONWATCH CRITICAL - Bad failed | jobs=2 critical=1 warning=0 ok=1\nCRITICAL: Bad: failed ") ||
		!strings.Contains(out, ": disk full\n") {
		t.Fatalf("one failing job: %d %q", code, out)
	}
	// Checking only the good job ignores the bad one.
	if code, out := check("good"); code != 0 || !strings.Contains(out, "jobs=1 critical=0") {
		t.Fatalf("check good: %d %q", code, out)
	}
	if code, out := check("nope"); code != 3 || out != "CRONWATCH UNKNOWN - no job with slug \"nope\"\n" {
		t.Fatalf("unknown slug: %d %q", code, out)
	}
}
