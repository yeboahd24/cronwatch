package app

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestBadgeCommand(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv(envOnFailure, "")
	t.Setenv(envOnRecover, "")
	t.Setenv(envNotify, "")
	cli := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := Run(ctx, append(args[:1:1], append([]string{"--data-dir", dir}, args[1:]...)...), &out, io.Discard)
		return out.String(), err
	}
	if _, err := cli("run", "--name", "Backup", "--tag", "db", "--", "true"); err != nil {
		t.Fatal(err)
	}
	if out, err := cli("badge", "backup"); err != nil || !strings.HasPrefix(out, "<svg") || !strings.Contains(out, ">ok</text>") {
		t.Errorf("badge backup: %v\n%s", err, out)
	}
	if out, err := cli("badge", "--tag", "db", "--label", "databases"); err != nil || !strings.Contains(out, ">databases</text>") {
		t.Errorf("badge --tag: %v\n%s", err, out)
	}
	if out, err := cli("badge", "--json", "backup"); err != nil || !strings.Contains(out, `"message":"ok"`) {
		t.Errorf("badge --json: %v\n%s", err, out)
	}
	for _, args := range [][]string{{"badge"}, {"badge", "--tag", "db", "backup"}, {"badge", "nope"}, {"badge", "a", "b"}} {
		if _, err := cli(args...); err == nil {
			t.Errorf("%q succeeded", args)
		}
	}
}
