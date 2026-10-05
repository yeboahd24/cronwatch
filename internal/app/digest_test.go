package app

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestDigest(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	digest := func(args ...string) string {
		var out bytes.Buffer
		if err := Run(ctx, append([]string{"digest", "--data-dir", dir}, args...), &out, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if out := digest("--quiet"); out != "" {
		t.Fatalf("quiet digest with no jobs = %q", out)
	}
	run := func(name, script string) {
		_ = Run(ctx, []string{"run", "--name", name, "--data-dir", dir, "--", "sh", "-c", script}, &bytes.Buffer{}, &bytes.Buffer{})
	}
	run("Good", "true")
	run("Good", "true")
	if out := digest("--quiet"); out != "" {
		t.Fatalf("quiet digest with only successes = %q", out)
	}
	run("Bad", "echo 'disk full' >&2; exit 2")
	out := digest("--quiet")
	for _, want := range []string{"Needs attention (1)\n  Bad: failed ", "(exit 2): disk full", "Good  success  2     0       0       "} {
		if !strings.Contains(out, want) {
			t.Fatalf("digest lacks %q:\n%s", want, out)
		}
	}
	// A job that failed and recovered in the period still counts as a problem.
	run("Bad", "true")
	out = digest("--quiet")
	if !strings.Contains(out, "Nothing needs attention") || !strings.Contains(out, "Bad   success  2     1       0       ") {
		t.Fatalf("digest after recovery:\n%s", out)
	}
}
