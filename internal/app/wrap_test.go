package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeboahd24/cronwatch/internal/storage"
)

const unwrappedCrontab = `CW=/home/u/.local/bin/cronwatch
# nightly
0 2 * * * /usr/local/bin/backup.sh >> /var/log/backup.log 2>&1
*/5 * * * * $CW run --name "Ingest queue" -- bash ingest.sh
30 4 * * 0 cd /srv/app && ./backup.sh
15 3 * * * mail -s report me%body
`

func TestProposeWrap(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, t.TempDir())
	// An existing job's slug is not reused.
	if _, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "backup", Name: "backup", Command: `"x"`}); err != nil {
		t.Fatal(err)
	}
	p, err := proposeWrap(ctx, s, unwrappedCrontab, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(strings.Replace(unwrappedCrontab,
		"0 2 * * * /usr/local/bin/backup.sh", "0 2 * * * $CW run --name 'backup 2' -- /usr/local/bin/backup.sh", 1),
		"30 4 * * 0 cd /srv/app && ./backup.sh", "30 4 * * 0 $CW run --name 'backup 3' -- sh -c 'cd /srv/app && ./backup.sh'", 1)
	if p.After != want {
		t.Fatalf("after:\n%s\nwant:\n%s", p.After, want)
	}
	if len(p.Skipped) != 1 || !strings.HasPrefix(p.Skipped[0], "line 6: uses %") {
		t.Fatalf("skipped = %q", p.Skipped)
	}
	diff := lineDiff(p.Before, p.After, "crontab")
	wantDiff := `--- crontab
+++ crontab (wrapped)
@@ -2,5 +2,5 @@
 # nightly
-0 2 * * * /usr/local/bin/backup.sh >> /var/log/backup.log 2>&1
+0 2 * * * $CW run --name 'backup 2' -- /usr/local/bin/backup.sh >> /var/log/backup.log 2>&1
 */5 * * * * $CW run --name "Ingest queue" -- bash ingest.sh
-30 4 * * 0 cd /srv/app && ./backup.sh
+30 4 * * 0 $CW run --name 'backup 3' -- sh -c 'cd /srv/app && ./backup.sh'
 15 3 * * * mail -s report me%body
`
	if diff != wantDiff {
		t.Fatalf("diff:\n%s\nwant:\n%s", diff, wantDiff)
	}

	// --lines picks lines, and explains the ones it cannot wrap.
	p, err = proposeWrap(ctx, s, unwrappedCrontab, []int{4, 5, 9}, "cw")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Wrapped) != 1 || p.Wrapped[0].Line != 5 || !strings.Contains(p.Wrapped[0].After, "cw run --name 'backup 2' -- sh -c") ||
		strings.Join(p.Skipped, "|") != "line 9: not a scheduled command|line 4: already uses cronwatch" {
		t.Fatalf("proposal = %+v", p)
	}
}

func TestSyncWrapApplyAndRestoreFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	tab := filepath.Join(t.TempDir(), "crontab")
	if err := os.WriteFile(tab, []byte(unwrappedCrontab), 0o640); err != nil {
		t.Fatal(err)
	}
	sync := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := Run(ctx, append([]string{"sync", "--data-dir", dir, "--crontab", tab}, args...), &out, &bytes.Buffer{}); err != nil {
			t.Fatalf("sync %v: %v", args, err)
		}
		return out.String()
	}
	read := func() string {
		b, err := os.ReadFile(tab)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	// --wrap alone changes nothing.
	if out := sync("--wrap"); !strings.Contains(out, "Would wrap 2 lines") || !strings.Contains(out, "run the same command with --apply") || read() != unwrappedCrontab {
		t.Fatalf("preview:\n%s", out)
	}
	if out := sync(); !strings.Contains(out, "cronwatch sync --wrap") {
		t.Fatalf("plain sync does not point to --wrap:\n%s", out)
	}
	out := sync("--wrap", "--lines", "3", "--apply")
	if !strings.Contains(out, "Wrapped 1 line") || !strings.Contains(out, "Added    backup") || !strings.Contains(out, "--crontab "+tab+" --restore ") {
		t.Fatalf("apply:\n%s", out)
	}
	wrapped := read()
	if !strings.Contains(wrapped, "0 2 * * * $CW run --name backup -- /usr/local/bin/backup.sh") {
		t.Fatalf("crontab after apply:\n%s", wrapped)
	}
	if info, _ := os.Stat(tab); info.Mode().Perm() != 0o640 {
		t.Fatalf("mode after apply = %v", info.Mode())
	}
	if out := sync("--backups"); len(strings.Fields(out)) != 1 {
		t.Fatalf("backups:\n%s", out)
	}

	// Restoring puts the original back and keeps the wrapped one.
	if out := sync("--restore", "latest"); !strings.Contains(out, "Restored") || read() != unwrappedCrontab {
		t.Fatalf("restore:\n%s\ncrontab:\n%s", out, read())
	}
	names := strings.Fields(sync("--backups"))
	if len(names) != 2 {
		t.Fatalf("backups after restore = %q", names)
	}
	sync("--restore", names[1])
	if read() != wrapped {
		t.Fatal("restoring the second backup did not bring back the wrapped crontab")
	}
	if out := sync("--restore", names[1]); !strings.Contains(out, "already matches") {
		t.Fatalf("restore of the current content:\n%s", out)
	}

	// Backups of a file are not your crontab's.
	var errOut bytes.Buffer
	if err := Run(ctx, []string{"sync", "--data-dir", dir, "--crontab", tab + ".other", "--restore", "latest"}, &bytes.Buffer{}, &errOut); err == nil {
		t.Fatal("restored another crontab's backup")
	}
	for _, bad := range [][]string{{"--apply"}, {"--wrap", "--backups"}, {"--wrap", "--json"}, {"--wrap", "--lines", "x"}} {
		if err := Run(ctx, append([]string{"sync", "--data-dir", dir, "--crontab", tab}, bad...), &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestSyncWrapApplyUserCrontab(t *testing.T) {
	ctx := context.Background()
	// A fake crontab command keeps the crontab in a file.
	bin, store := t.TempDir(), filepath.Join(t.TempDir(), "tab")
	script := "#!/bin/sh\ncase \"$1\" in\n-l) cat " + store + " ;;\n-) cat > " + store + " ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "crontab"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store, []byte("0 1 * * * /opt/nightly.sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	dir := t.TempDir()
	var out bytes.Buffer
	if err := Run(ctx, []string{"sync", "--data-dir", dir, "--wrap", "--cronwatch", "/usr/local/bin/cronwatch", "--apply"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(store); string(b) != "0 1 * * * /usr/local/bin/cronwatch run --name nightly -- /opt/nightly.sh\n" {
		t.Fatalf("crontab = %q\n%s", b, out.String())
	}
	if !strings.Contains(out.String(), "--data-dir "+dir+" --restore ") || strings.Contains(out.String(), "--crontab") {
		t.Fatalf("undo hint:\n%s", out.String())
	}
	if err := Run(ctx, []string{"sync", "--data-dir", dir, "--restore", "latest"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(store); string(b) != "0 1 * * * /opt/nightly.sh\n" {
		t.Fatalf("restored crontab = %q", b)
	}
}
