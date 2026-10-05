package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestJSONOutput(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	_ = Run(ctx, []string{"run", "--name", "Backup", "--schedule", "0 2 * * *", "--data-dir", dir, "--", "sh", "-c", "exit 3"}, &bytes.Buffer{}, &bytes.Buffer{})

	var out bytes.Buffer
	if err := Run(ctx, []string{"jobs", "--json", "--data-dir", dir}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var jobs []map[string]any
	if err := json.Unmarshal(out.Bytes(), &jobs); err != nil {
		t.Fatalf("jobs --json is not JSON: %v\n%s", err, out.String())
	}
	if len(jobs) != 1 || jobs[0]["slug"] != "backup" || jobs[0]["status"] != "failed" || jobs[0]["schedule"] != "0 2 * * *" ||
		jobs[0]["next_expected_at"] == nil || jobs[0]["missed_at"] != nil {
		t.Fatalf("jobs = %v", jobs)
	}
	last := jobs[0]["last_run"].(map[string]any)
	if last["exit_code"] != float64(3) || last["job_slug"] != "backup" || last["ended_at"] == nil {
		t.Fatalf("last_run = %v", last)
	}

	out.Reset()
	if err := Run(ctx, []string{"runs", "--json", "--data-dir", dir, "backup"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var runs []map[string]any
	if err := json.Unmarshal(out.Bytes(), &runs); err != nil || len(runs) != 1 || runs[0]["job"] != "Backup" || runs[0]["status"] != "failed" {
		t.Fatalf("runs --json = %v, %v\n%s", runs, err, out.String())
	}

	tab := filepath.Join(t.TempDir(), "crontab")
	if err := os.WriteFile(tab, []byte("0 2 * * * cronwatch run --name Backup -- b.sh\n30 4 * * 0 docker system prune -f\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(ctx, []string{"sync", "--json", "--data-dir", dir, "--crontab", tab}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var sync struct {
		Jobs        int      `json:"jobs"`
		Added       []string `json:"added"`
		Unmonitored []struct {
			Line    int    `json:"line"`
			Command string `json:"command"`
		} `json:"unmonitored"`
		Changes []any `json:"crontab_changes"`
	}
	if err := json.Unmarshal(out.Bytes(), &sync); err != nil {
		t.Fatalf("sync --json: %v\n%s", err, out.String())
	}
	if sync.Jobs != 1 || sync.Added == nil || len(sync.Unmonitored) != 1 || sync.Unmonitored[0].Line != 2 || sync.Changes == nil {
		t.Fatalf("sync = %+v\n%s", sync, out.String())
	}
}
