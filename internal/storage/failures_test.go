package storage

import (
	"context"
	"testing"
	"time"
)

func TestFailureGroups(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	job, err := s.UpsertJob(ctx, testSpec("backup", "", 0))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-10 * 24 * time.Hour)
	one := 1
	var ids []string
	for i, tc := range []struct{ status, stderr string }{
		{"failed", "[Day 1] ssh: connect to host api port 22: Connection timed out\n"},
		{"success", ""},
		{"failed", "[Day 3] ssh: connect to host api port 22: Connection timed out\n"},
		{"failed", "pg_dump: command not found\n"},
		{"failed", "[Day 5] ssh: connect to host api port 22: Connection timed out\n"},
	} {
		run, err := s.CreateRun(ctx, job.ID, base.Add(time.Duration(i)*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteRun(ctx, run.ID, Completion{Ended: run.StartedAt, Status: tc.status, ExitCode: &one, Stderr: tc.stderr}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, run.ID)
	}
	runs := map[string]string{}
	for _, id := range ids {
		r, err := s.GetRun(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		runs[id] = r.FailureSignature
	}
	if runs[ids[1]] != "" || runs[ids[0]] == "" || runs[ids[0]] != runs[ids[2]] || runs[ids[0]] == runs[ids[3]] {
		t.Fatalf("signatures = %v", runs)
	}

	last, _ := s.GetRun(ctx, ids[4])
	h, err := s.FailureHistoryBefore(ctx, last)
	if err != nil || h.Earlier != 2 || h.FirstSeen == nil || !h.FirstSeen.Equal(base) {
		t.Fatalf("history = %+v, %v", h, err)
	}
	first, _ := s.GetRun(ctx, ids[3])
	if h, err := s.FailureHistoryBefore(ctx, first); err != nil || h.Earlier != 0 || h.FirstSeen != nil {
		t.Fatalf("history of a new error = %+v, %v", h, err)
	}

	groups, err := s.FailureGroups(ctx, job.ID, 10)
	if err != nil || len(groups) != 2 {
		t.Fatalf("groups = %+v, %v", groups, err)
	}
	if g := groups[0]; g.Runs != 3 || g.Latest.ID != ids[4] || !g.FirstSeen.Equal(base) {
		t.Fatalf("ssh group = %+v", g)
	}
	if g := groups[1]; g.Runs != 1 || g.Latest.ID != ids[3] {
		t.Fatalf("pg_dump group = %+v", g)
	}

	// Failures recorded before signatures existed are signed by the backfill.
	if _, err := s.DB.ExecContext(ctx, "UPDATE runs SET failure_signature = NULL"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.BackfillFailureSignatures(ctx, 500); err != nil || n != 4 {
		t.Fatalf("backfill = %d, %v", n, err)
	}
	if again, _ := s.GetRun(ctx, ids[0]); again.FailureSignature != runs[ids[0]] {
		t.Fatal("backfilled signature differs from the one recorded at completion")
	}
	if n, err := s.BackfillFailureSignatures(ctx, 500); err != nil || n != 0 {
		t.Fatalf("second backfill = %d, %v", n, err)
	}
}
