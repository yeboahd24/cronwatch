package runner

import (
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
	stdout, stderr, combined *boundedBuffer
}

type stream struct {
	c    *capture
	dst  *boundedBuffer
	echo io.Writer
}

func (s stream) Write(p []byte) (int, error) {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	if s.echo != nil {
		_, _ = s.echo.Write(p)
	}
	s.dst.Write(p)
	s.c.combined.Write(p)
	return len(p), nil
}

func Execute(ctx context.Context, command []string, echoStdout, echoStderr io.Writer, maxLogBytes int64) (Result, error) {
	var result Result
	if maxLogBytes < 0 {
		return result, errors.New("max log bytes must be non-negative")
	}
	c := &capture{stdout: newBoundedBuffer(maxLogBytes), stderr: newBoundedBuffer(maxLogBytes), combined: newBoundedBuffer(2 * maxLogBytes)}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdout = stream{c: c, dst: c.stdout, echo: echoStdout}
	cmd.Stderr = stream{c: c, dst: c.stderr, echo: echoStderr}
	// Run the child in its own process group so cancellation also reaches
	// grandchildren such as the commands started by "sh -c".
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	started := time.Now()
	err := cmd.Run()
	result.Duration = time.Since(started)
	exit, isExit := errors.AsType[*exec.ExitError](err)
	if err != nil && !isExit {
		// The child never ran (e.g. not found), so record why in the logs.
		note := []byte("cronwatch: " + err.Error() + "\n")
		_, _ = (stream{c: c, dst: c.stderr, echo: echoStderr}).Write(note)
	}
	c.mu.Lock()
	result.Stdout = c.stdout.String()
	result.Stderr = c.stderr.String()
	result.Combined = c.combined.String()
	result.Truncated = c.stdout.Truncated() || c.stderr.Truncated() || c.combined.Truncated()
	c.mu.Unlock()
	result.Status = "success"
	if ctx.Err() != nil {
		result.Status = "cancelled"
	} else if err != nil {
		result.Status = "failed"
	}
	switch {
	case isExit:
		result.ExitCode = exit.ExitCode()
		// Follow the shell convention of 128+signal for signal deaths.
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			result.ExitCode = 128 + int(ws.Signal())
		}
	case err != nil:
		result.ExitCode = 127
	}
	return result, err
}
