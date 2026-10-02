package crontab

import (
	"reflect"
	"testing"
)

const sample = `# m h dom mon dow command
SHELL=/bin/bash
CW="/home/exedev/.local/bin/cronwatch"

0 2 * * * cd /home/exedev/GPS/GhanaPostGPS && $CW run --name "GhanaPostGPS DB backup" --schedule "0 2 * * *" --grace 10m -- bash scripts/backup-db.sh >> /var/log/ghanapostgps-backup.log 2>&1
0 7 * * * /home/exedev/.local/bin/cronwatch run --name valhalla-tile-staleness --slug valhalla-tile-staleness --schedule "0 7 * * *" --grace 10m -- bash ~/valhalla-tile-staleness-check.sh >> ~/valhalla-tile-staleness.log 2>&1
30 4 * * 0 docker builder prune -af --filter until=72h >> ~/docker-prune.log 2>&1
  17 */6 * 4-10 *   /home/exedev/gfm_nrt_cron.sh>>/home/exedev/gfm_nrt.log 2>&1
@daily ${CW} run --name Nightly -- echo 'it''s 100\% done' % stdin data
@reboot /bin/flock -n /tmp/serve.lock /home/exedev/.local/bin/cronwatch serve --addr 127.0.0.1:8765 >> /tmp/serve.log 2>&1
@bogus ignored
`

func TestParse(t *testing.T) {
	c := Parse(sample)
	if c.Env["CW"] != "/home/exedev/.local/bin/cronwatch" || c.Env["SHELL"] != "/bin/bash" {
		t.Fatalf("env = %v", c.Env)
	}
	if len(c.Entries) != 6 {
		t.Fatalf("entries = %+v", c.Entries)
	}
	gfm := c.Entries[3]
	if gfm.Line != 8 || gfm.Schedule != "17 */6 * 4-10 *" || gfm.Command != "/home/exedev/gfm_nrt_cron.sh>>/home/exedev/gfm_nrt.log 2>&1" {
		t.Fatalf("gfm = %+v", gfm)
	}
	daily := c.Entries[4]
	if daily.Schedule != "0 0 * * *" || daily.Raw != "@daily" || daily.Command != `${CW} run --name Nightly -- echo 'it''s 100% done' ` {
		t.Fatalf("daily = %+v", daily)
	}
	if reboot := c.Entries[5]; reboot.Schedule != "" || reboot.Raw != "@reboot" {
		t.Fatalf("reboot = %+v", reboot)
	}
}

func words(tokens []Token) []string {
	var out []string
	for _, t := range tokens {
		out = append(out, t.Text)
	}
	return out
}

func TestSplit(t *testing.T) {
	env := map[string]string{"HOME": "/home/u", "CW": "/bin/cronwatch"}
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{`a "b c" 'd e' f\ g`, []string{"a", "b c", "d e", "f g"}},
		{`$CW run -- x>>log 2>&1`, []string{"/bin/cronwatch", "run", "--", "x", ">>", "log", "2>&1"}},
		{`cd /x && ${CW} run`, []string{"cd", "/x", "&&", "/bin/cronwatch", "run"}},
		{`bash ~/a.sh ~other/b "~/c"`, []string{"bash", "/home/u/a.sh", "~other/b", "~/c"}},
		{`echo "$HOME/\"q\"" 'no $CW' # comment`, []string{"echo", `/home/u/"q"`, "no $CW"}},
		{`a;b|c||d &`, []string{"a", ";", "b", "|", "c", "||", "d", "&"}},
		{`echo price$`, []string{"echo", "price$"}},
	} {
		if got := words(Split(tc.in, env)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Split(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if tokens := Split(`run --name "$(hostname) job"`, env); !tokens[2].Dynamic {
		t.Errorf("command substitution not flagged: %+v", tokens)
	}
}

func TestFindRun(t *testing.T) {
	c := Parse(sample)
	args, found, dynamic := FindRun(Split(c.Entries[0].Command, c.Env))
	want := []string{"--name", "GhanaPostGPS DB backup", "--schedule", "0 2 * * *", "--grace", "10m", "--", "bash", "scripts/backup-db.sh"}
	if !found || dynamic || !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %q (found %v)", args, found)
	}
	args, _, _ = FindRun(Split(c.Entries[1].Command, map[string]string{"HOME": "/home/exedev"}))
	if args[len(args)-1] != "/home/exedev/valhalla-tile-staleness-check.sh" {
		t.Fatalf("tilde not expanded: %q", args)
	}
	if _, found, _ := FindRun(Split(c.Entries[2].Command, c.Env)); found {
		t.Fatal("unwrapped line reported as cronwatch run")
	}
	reboot := Split(c.Entries[5].Command, c.Env)
	if _, found, _ := FindRun(reboot); found || !MentionsCronwatch(reboot) {
		t.Fatal("cronwatch serve line misclassified")
	}
}
