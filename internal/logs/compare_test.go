package logs

import (
	"slices"
	"testing"
)

func TestNormalize(t *testing.T) {
	for _, pair := range [][2]string{
		{"[Mon Oct  5 02:00:01 UTC 2026] Starting backup", "[Tue Oct 6 02:00:02 UTC 2026] Starting backup"},
		{"wrote /tmp/tmp.Ab12Xy/db.sql.gz in 42s", "wrote /tmp/tmp.Zq98Kp/db.sql.gz in 1m3s"},
		{"run 9b56e10bd024da12 finished", "run 0486399e3775b729 finished"},
		{"request 3f2a9c1e-5b7d-4e8a-9c21-7d4e5f6a8b90 ok", "request 11111111-2222-4333-8444-555555555555 ok"},
	} {
		if a, b := Normalize(pair[0]), Normalize(pair[1]); a != b {
			t.Errorf("Normalize differs:\n  %q -> %q\n  %q -> %q", pair[0], a, pair[1], b)
		}
	}
	for _, pair := range [][2]string{
		{"connected to db", "connection refused"},
		{"bad config", "add config"},
		{"took 5s", "took 5 seconds"},
	} {
		if Normalize(pair[0]) == Normalize(pair[1]) {
			t.Errorf("Normalize(%q) == Normalize(%q)", pair[0], pair[1])
		}
	}
}

func TestCompare(t *testing.T) {
	success := Parse("[Mon 02:00] Starting backup\nDump complete. Size: 1.7G\nUploaded to api\nretry\nretry\n")
	failed := Parse("[Tue 02:00] Starting backup\nDump complete. Size: 1.8G\nretry\n" +
		string(StderrMark) + "ssh: connect to host api port 22: Connection timed out\n" +
		string(StderrMark) + "scp: Connection closed\n")
	added, removed := Compare(success, failed)
	text := func(lines []Line) []string {
		var out []string
		for _, l := range lines {
			out = append(out, l.Text)
		}
		return out
	}
	if want := []string{"ssh: connect to host api port 22: Connection timed out", "scp: Connection closed"}; !slices.Equal(text(added), want) {
		t.Fatalf("added = %q, want %q", text(added), want)
	}
	if !added[0].IsStderr() || added[0].Number != 4 {
		t.Fatalf("added keeps stream and number: %+v", added[0])
	}
	// One "retry" matched; the second is missing.
	if want := []string{"Uploaded to api", "retry"}; !slices.Equal(text(removed), want) {
		t.Fatalf("removed = %q, want %q", text(removed), want)
	}
	// The same text on another stream is a change.
	if added, _ := Compare(Parse("warning\n"), Parse(string(StderrMark)+"warning\n")); len(added) != 1 {
		t.Fatal("a line moving to stderr was not reported")
	}
}
