package logs

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// signatureLines is how many trailing lines identify a failure: enough to
// tell errors apart, few enough that earlier progress output does not count.
const signatureLines = 5

// FailureSignature identifies how a run failed, so failures that print the
// same error can be grouped. It hashes status and the normalized last few
// non-blank stderr lines, or of stdout when stderr is empty, or the exit code
// when the run printed nothing. Runs that differ only in numbers, dates, IDs
// or /tmp paths get the same signature.
func FailureSignature(status, stderr, stdout string, exitCode *int) string {
	var keys []string
	lines := lastNonBlank(stderr, signatureLines)
	if len(lines) == 0 {
		lines = lastNonBlank(stdout, signatureLines)
	}
	for _, l := range lines {
		keys = append(keys, Normalize(l))
	}
	// A silent failure is told apart by its exit code, which is not normalized.
	if len(keys) == 0 && exitCode != nil {
		keys = []string{"exit " + strconv.Itoa(*exitCode)}
	}
	h := sha256.New()
	h.Write([]byte(status))
	for _, k := range keys {
		h.Write([]byte{'\n'})
		h.Write([]byte(k))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// lastNonBlank returns up to n of text's last non-blank lines, oldest first,
// skipping truncation markers.
func lastNonBlank(text string, n int) []string {
	var out []string
	rest := text
	for len(out) < n && rest != "" {
		i := strings.LastIndexByte(rest, '\n')
		line := rest[i+1:]
		rest = rest[:max(i, 0)]
		if strings.TrimSpace(line) == "" || truncationMarker.MatchString(line) {
			continue
		}
		out = append(out, line)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
