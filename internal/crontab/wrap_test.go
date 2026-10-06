package crontab

import (
	"errors"
	"testing"
)

func TestWrap(t *testing.T) {
	env := map[string]string{"HOME": "/home/u"}
	for _, tc := range []struct{ command, want string }{
		// A simple command is prefixed; its redirections now apply to cronwatch.
		{"/usr/local/bin/backup.sh >> /var/log/backup.log 2>&1", "cw run --name job -- /usr/local/bin/backup.sh >> /var/log/backup.log 2>&1"},
		{`date +\%F`, `cw run --name job -- date +\%F`},
		// Anything else runs with sh -c, and trailing redirections stay outside.
		{"cd /srv/app && ./run.sh >> ~/run.log 2>&1", "cw run --name job -- sh -c 'cd /srv/app && ./run.sh' >> ~/run.log 2>&1"},
		{"a | b", "cw run --name job -- sh -c 'a | b'"},
		{"echo 'it''s' && true", `cw run --name job -- sh -c 'echo '\''it'\'''\''s'\'' && true'`},
		{"FOO=bar ./x.sh", "cw run --name job -- sh -c 'FOO=bar ./x.sh'"},
		{"cd /x; export A=1 && ./run > 'my log'", `cw run --name job -- sh -c 'cd /x; export A=1 && ./run' > 'my log'`},
		// Moving these would also redirect the first command's output.
		{"echo simple; echo err >&2", `cw run --name job -- sh -c 'echo simple; echo err >&2'`},
		{"a | b > out", `cw run --name job -- sh -c 'a | b > out'`},
		{"a && b > out.txt c", "cw run --name job -- sh -c 'a && b > out.txt c'"},
	} {
		got, err := Wrap(Entry{Text: tc.command}, env, "cw", "job")
		if err != nil || got != tc.want {
			t.Errorf("Wrap(%q) = %q, %v\n                     want %q", tc.command, got, err, tc.want)
		}
	}
	if _, err := Wrap(Entry{Text: "mail -s hi me%body"}, env, "cw", "job"); !errors.Is(err, ErrStdin) {
		t.Errorf("a %% command was wrapped: %v", err)
	}
	if got, _ := Wrap(Entry{Text: "true"}, env, "/opt/my tools/cronwatch", "DB backup"); got != "/opt/my tools/cronwatch run --name 'DB backup' -- true" {
		t.Errorf("name quoting: %q", got)
	}
}

func TestParseKeepsCommandText(t *testing.T) {
	tab := Parse("  0 2 * * *   date +\\%F%input\n")
	if e := tab.Entries[0]; e.Text != `date +\%F%input` || e.Command != "date +%F" {
		t.Fatalf("entry = %+v", e)
	}
}

func TestSuggestName(t *testing.T) {
	env := map[string]string{"HOME": "/home/u", "APP": "/srv/app"}
	for command, want := range map[string]string{
		"/usr/local/bin/backup.sh >> /var/log/backup.log 2>&1": "backup",
		"cd /srv/app && ./scripts/report.py --daily":           "report",
		"python3 -u $APP/etl.py":                               "etl",
		"nice -n 10 ionice -c3 rsync -a /src /dst":             "rsync",
		"flock -n /tmp/sync.lock /usr/bin/sync-mail":           "sync-mail",
		"timeout 1h ./long-job":                                "long-job",
		"FOO=1 BAR=2 ./thing.sh":                               "thing",
		"bash -c 'cd /x && ./deploy.sh'":                       "deploy",
		"> /dev/null docker system prune -f":                   "docker",
		"cd /tmp":                                              "",
	} {
		if got := SuggestName(command, env); got != want {
			t.Errorf("SuggestName(%q) = %q, want %q", command, got, want)
		}
	}
}
