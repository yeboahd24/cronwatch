package runner

import (
	"context"
	"strings"
	"testing"
)

func TestCaptureTruncatesWithoutStoppingChild(t *testing.T) {
	result, err := Execute(context.Background(), []string{"sh", "-c", "printf 123456789; printf abcdefghi >&2"}, nil, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "success" || result.ExitCode != 0 || !result.Truncated {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Stdout) != 4 || len(result.Stderr) != 4 || len(result.Combined) > 8 {
		t.Fatalf("capture limits: %+v", result)
	}
	if strings.Contains(result.Combined, "123456789") {
		t.Fatalf("combined log exceeded limit: %q", result.Combined)
	}
}
