package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/cronwatch/internal/storage"
)

func TestMaskCrontab(t *testing.T) {
	masked := maskCrontab("API_TOKEN=abc123\nDB_PASSWORD = \"s3cret\"\nPATH=/usr/bin:/bin\nMAILTO=\n0 2 * * * echo token=x\n")
	for _, secret := range []string{"abc123", "s3cret"} {
		if strings.Contains(masked, secret) {
			t.Fatalf("secret %q stored:\n%s", secret, masked)
		}
	}
	for _, kept := range []string{"API_TOKEN=‹hidden ", "DB_PASSWORD = ‹hidden ", "PATH=/usr/bin:/bin", "MAILTO=\n", "0 2 * * * echo token=x"} {
		if !strings.Contains(masked, kept) {
			t.Fatalf("masked crontab lacks %q:\n%s", kept, masked)
		}
	}
	if maskCrontab("API_TOKEN=one") == maskCrontab("API_TOKEN=two") {
		t.Fatal("a changed secret is not noticed")
	}
}

func TestDiffCrontabs(t *testing.T) {
	before := `CW=/usr/local/bin/cronwatch
API_KEY=old
0 2 * * * $CW run --name Backup --schedule "0 2 * * *" -- backup.sh
*/5 * * * * $CW run --name Ingest -- ingest.sh
0 4 * * * $CW run --name Report -- report.sh
30 4 * * 0 docker system prune -f
`
	after := `CW=/usr/local/bin/cronwatch
API_KEY=new
0 3 * * * $CW run --name Backup --schedule "0 3 * * *" -- backup.sh
*/5 * * * * $CW run --name Ingest -- ingest.sh --fast
@daily $CW run --name Cleanup -- cleanup.sh
`
	got := map[string]string{} // job changes by slug and kind
	var other []string         // other lines, as "+ line" or "- line"
	for _, c := range diffCrontabs(maskCrontab(before), maskCrontab(after)) {
		switch c.Kind {
		case "line_added":
			other = append(other, "+ "+c.After)
		case "line_removed":
			other = append(other, "- "+c.Before)
		default:
			got[c.JobSlug+" "+c.Kind] = c.Before + " -> " + c.After
		}
	}
	want := map[string]string{
		"backup schedule": "0 2 * * * -> 0 3 * * *",
		"ingest changed":  "*/5 * * * * $CW run --name Ingest -- ingest.sh -> */5 * * * * $CW run --name Ingest -- ingest.sh --fast",
		"report removed":  "0 4 * * * $CW run --name Report -- report.sh -> ",
		"cleanup added":   " -> @daily $CW run --name Cleanup -- cleanup.sh",
	}
	if len(got) != len(want) {
		t.Errorf("job changes = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	// The changed API_KEY shows as a removed and an added line, both masked.
	if len(other) != 3 || !strings.HasPrefix(other[0], "+ API_KEY=‹hidden ") ||
		!strings.HasPrefix(other[1], "- API_KEY=‹hidden ") || other[2] != "- 30 4 * * 0 docker system prune -f" {
		t.Errorf("other lines = %q", other)
	}
	for _, l := range other {
		if strings.Contains(l, "old") || strings.Contains(l, "=new") {
			t.Errorf("secret value in %q", l)
		}
	}
}

func TestRecordCrontab(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := storage.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v1 := "0 2 * * * cronwatch run --name Backup -- backup.sh\n"
	v2 := "0 3 * * * cronwatch run --name Backup -- backup.sh\n"
	now := time.Now()
	for i, tc := range []struct {
		text    string
		changes int
	}{{v1, 0}, {v1, 0}, {v2, 1}, {v2, 0}} {
		changes, err := recordCrontab(ctx, s, tc.text, now.Add(time.Duration(i)*time.Minute))
		if err != nil || len(changes) != tc.changes {
			t.Fatalf("step %d: changes = %+v, %v", i, changes, err)
		}
	}
	var snapshots int
	if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM crontab_snapshots").Scan(&snapshots); err != nil || snapshots != 2 {
		t.Fatalf("snapshots = %d, %v", snapshots, err)
	}
	s.Close()

	var out bytes.Buffer
	if err := Run(ctx, []string{"crontab-history", "--data-dir", dir}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "  backup: schedule changed from 0 2 * * * to 0 3 * * *") {
		t.Fatalf("crontab-history output:\n%s", out.String())
	}
	out.Reset()
	if err := Run(ctx, []string{"crontab-history", "--data-dir", dir, "other"}, &out, &bytes.Buffer{}); err != nil || out.String() != "No changes recorded.\n" {
		t.Fatalf("history of an unchanged job = %q, %v", out.String(), err)
	}

	// sync --crontab FILE registers jobs but does not record history.
	other := t.TempDir()
	file := filepath.Join(t.TempDir(), "crontab")
	if err := os.WriteFile(file, []byte(v2), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, []string{"sync", "--data-dir", other, "--crontab", file}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(ctx, []string{"crontab-history", "--data-dir", other}, &out, &bytes.Buffer{}); err != nil || !strings.HasPrefix(out.String(), "No crontab recorded yet.") {
		t.Fatalf("history after sync --crontab = %q, %v", out.String(), err)
	}
}
