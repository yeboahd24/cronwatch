package logs

import "testing"

func TestParse(t *testing.T) {
	stored := "start\n\x02boom\n... [cronwatch: 12 bytes truncated] ...\nend\n"
	lines := Parse(stored)
	if len(lines) != 4 {
		t.Fatalf("lines = %+v", lines)
	}
	if lines[0].Number != 1 || lines[0].IsStderr() || lines[1].Text != "boom" || !lines[1].IsStderr() || lines[1].Number != 2 {
		t.Fatalf("lines = %+v", lines)
	}
	if !lines[2].IsTruncated() || lines[2].Number != 0 || lines[3].Number != 3 {
		t.Fatalf("lines = %+v", lines)
	}
	if LastError(lines) != "boom" {
		t.Fatalf("last error = %q", LastError(lines))
	}
	if Plain(stored) != "start\nboom\n... [cronwatch: 12 bytes truncated] ...\nend\n" {
		t.Fatalf("plain = %q", Plain(stored))
	}
	if got := Filter(lines, true, ""); len(got) != 1 || got[0].Text != "boom" {
		t.Fatalf("stderr filter = %+v", got)
	}
	if got := Filter(lines, false, "END"); len(got) != 1 || got[0].Text != "end" {
		t.Fatalf("query filter = %+v", got)
	}
	if Parse("") != nil {
		t.Fatal("empty log should have no lines")
	}
}

func TestPlainLegacyLog(t *testing.T) {
	lines := Parse("a\nb")
	if len(lines) != 2 || lines[0].IsStderr() || lines[1].IsStderr() {
		t.Fatalf("lines = %+v", lines)
	}
}
