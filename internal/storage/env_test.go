package storage

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/yeboahd24/cronwatch/internal/runenv"
)

func TestRunEnvironments(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	job, err := s.UpsertJob(ctx, testSpec("env", "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LatestRunWithEnv(ctx, job.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("no runs: err = %v", err)
	}
	cron := runenv.FromEnviron([]string{"PATH=/usr/bin:/bin", "SECRET=x"})
	shell := runenv.FromEnviron([]string{"PATH=/usr/local/bin:/usr/bin:/bin"})
	base := time.Now().Add(-time.Hour)
	var ids []string
	for i, tc := range []struct {
		env    runenv.Env
		status string
	}{{cron, "success"}, {cron, "success"}, {shell, "failed"}} {
		run, err := s.CreateRun(ctx, job.ID, base.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetRunEnv(ctx, run.ID, tc.env); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishRun(ctx, run.ID, time.Now(), time.Second, tc.status, nil, "", "", "", false); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, run.ID)
	}
	var stored int
	if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM environments").Scan(&stored); err != nil || stored != 2 {
		t.Fatalf("stored environments = %d, %v; want 2", stored, err)
	}

	latest, err := s.LatestRunWithEnv(ctx, job.ID)
	if err != nil || latest.ID != ids[2] {
		t.Fatalf("latest = %v, %v", latest.ID, err)
	}
	env, err := s.GetEnvironment(ctx, latest.EnvHash)
	if err != nil || env.Vars["PATH"] != "/usr/local/bin:/usr/bin:/bin" {
		t.Fatalf("environment = %+v, %v", env, err)
	}
	success, err := s.LastSuccessWithEnvBefore(ctx, job.ID, latest.StartedAt)
	if err != nil || success.ID != ids[1] {
		t.Fatalf("last success = %v, %v", success.ID, err)
	}
	if _, err := s.LastSuccessWithEnvBefore(ctx, job.ID, base); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("before the first run: err = %v", err)
	}

	// Pruning the last runs that use an environment removes the environment.
	if _, err := s.Prune(ctx, 1, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM environments").Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("after prune: stored environments = %d, %v; want 1", stored, err)
	}
}
