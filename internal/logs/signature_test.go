package logs

import (
	"slices"
	"testing"
)

func TestFailureSignature(t *testing.T) {
	one, two := 1, 2
	const ssh = "[Mon 02:00:01] ssh: connect to host api port 22: Connection timed out\nscp: Connection closed\n"
	base := FailureSignature("failed", ssh, "", &one)
	same := map[string]string{
		"different times and exit code":        FailureSignature("failed", "[Tue 02:00:07] ssh: connect to host api port 22: Connection timed out\nscp: Connection closed\n\n", "", &two),
		"stdout ignored when stderr has lines": FailureSignature("failed", ssh, "Dump complete. Size: 1.7G\n", &one),
	}
	for name, other := range same {
		if other != base {
			t.Errorf("%s: signature changed", name)
		}
	}
	different := map[string]string{
		"different error": FailureSignature("failed", "pg_dump: command not found\n", "", &one),
		"timeout":         FailureSignature("timeout", ssh, "", &one),
		"stdout only":     FailureSignature("failed", "", "ERROR: disk full\n", &one),
		"silent exit 1":   FailureSignature("failed", "", "", &one),
	}
	for name, other := range different {
		if other == base {
			t.Errorf("%s: same signature as the ssh failure", name)
		}
	}
	if FailureSignature("failed", "", "", &one) == FailureSignature("failed", "", "", &two) {
		t.Error("silent failures with different exit codes share a signature")
	}
	// Only the last 5 lines count, so earlier progress output does not split groups.
	tail := "e1\ne2\ne3\ne4\ne5\n"
	if FailureSignature("failed", "step 1 of 9\n"+tail, "", &one) != FailureSignature("failed", "warming up\nstep 1 of 9\n"+tail, "", &one) {
		t.Error("lines before the last 5 changed the signature")
	}
	if len(base) != 16 {
		t.Errorf("signature length = %d", len(base))
	}
}

func TestLastNonBlank(t *testing.T) {
	text := "a\n\nb\n... [cronwatch: 5 bytes truncated] ...\nc\n  \nd\n"
	if got := lastNonBlank(text, 3); !slices.Equal(got, []string{"b", "c", "d"}) {
		t.Fatalf("lastNonBlank = %q", got)
	}
	if got := lastNonBlank("only", 5); !slices.Equal(got, []string{"only"}) {
		t.Fatalf("lastNonBlank = %q", got)
	}
}
