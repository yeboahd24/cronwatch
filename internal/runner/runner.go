package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type Result struct {
	Stdout, Stderr, Combined string
	Truncated                bool
	ExitCode                 int
	Status                   string
	Duration                 time.Duration
}

type capture struct {
	mu                       sync.Mutex
	stdout, stderr, combined bytes.Buffer
	max                      int64
	truncated                bool
}

type stream struct {
	c    *capture
	dst  *bytes.Buffer
	echo io.Writer
}

func appendBounded(dst *bytes.Buffer, p []byte, max int64) bool {
	remaining := max - int64(dst.Len())
	if remaining <= 0 {
		return len(p) > 0
	}
	n := len(p)
	if int64(n) > remaining {
		n = int(remaining)
	}
	dst.Write(p[:n])
	return n < len(p)
}

func (s stream) Write(p []byte) (int, error) {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	if s.echo != nil {
		_, _ = s.echo.Write(p)
	}
	if appendBounded(s.dst, p, s.c.max) {
		s.c.truncated = true
	}
	if appendBounded(&s.c.combined, p, 2*s.c.max) {
		s.c.truncated = true
	}
	return len(p), nil
}

func Execute(ctx context.Context, command []string, echoStdout, echoStderr io.Writer, maxLogBytes int64) (Result, error) {
	var result Result
	if maxLogBytes < 0 {
		return result, errors.New("max log bytes must be non-negative")
	}
	c := &capture{max: maxLogBytes}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdout = stream{c: c, dst: &c.stdout, echo: echoStdout}
	cmd.Stderr = stream{c: c, dst: &c.stderr, echo: echoStderr}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	started := time.Now()
	err := cmd.Run()
	result.Duration = time.Since(started)
	c.mu.Lock()
	result.Stdout = c.stdout.String()
	result.Stderr = c.stderr.String()
	result.Combined = c.combined.String()
	result.Truncated = c.truncated
	c.mu.Unlock()
	result.Status = "success"
	if ctx.Err() != nil {
		result.Status = "cancelled"
	} else if err != nil {
		result.Status = "failed"
	}
	if err != nil {
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			result.ExitCode = exit.ExitCode()
		} else {
			result.ExitCode = 127
		}
	}
	return result, err
}
