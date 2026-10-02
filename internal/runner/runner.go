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

	"github.com/yeboahd24/cronwatch/internal/logs"
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

// maxPendingLine bounds a partial line held back from the combined log, so
// output without newlines (progress bars, binary data) cannot grow memory.
const maxPendingLine = 64 << 10

type stream struct {
	c       *capture
	dst     *boundedBuffer
	echo    io.Writer
	stderr  bool
	pending []byte // partial line not yet in the combined log
}

func (s *stream) Write(p []byte) (int, error) {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	if s.echo != nil {
		_, _ = s.echo.Write(p)
	}
	s.dst.Write(p)
	// The combined log is built from whole lines, so lines from stdout and
	// stderr never interleave mid-line and each can be tagged by stream.
	s.pending = append(s.pending, p...)
	for {
		i := bytes.IndexByte(s.pending, '\n')
		if i < 0 {
			break
		}
		s.emit(s.pending[:i])
		s.pending = s.pending[i+1:]
	}
	if len(s.pending) >= maxPendingLine {
		s.emit(s.pending)
		s.pending = s.pending[:0]
	}
	s.pending = append([]byte(nil), s.pending...) // release consumed prefix
	return len(p), nil
}

func (s *stream) emit(line []byte) {
	if s.stderr {
		s.c.combined.Write([]byte{logs.StderrMark})
	}
	s.c.combined.Write(line)
	s.c.combined.Write([]byte{'\n'})
}

// flush emits a final unterminated line. The caller holds c.mu.
func (s *stream) flush() {
	if len(s.pending) > 0 {
		s.emit(s.pending)
		s.pending = nil
	}
}

func Execute(ctx context.Context, command []string, echoStdout, echoStderr io.Writer, maxLogBytes int64) (Result, error) {
	var result Result
	if maxLogBytes < 0 {
		return result, errors.New("max log bytes must be non-negative")
	}
	c := &capture{stdout: newBoundedBuffer(maxLogBytes), stderr: newBoundedBuffer(maxLogBytes), combined: newBoundedBuffer(2 * maxLogBytes)}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	stdoutStream := &stream{c: c, dst: c.stdout, echo: echoStdout}
	stderrStream := &stream{c: c, dst: c.stderr, echo: echoStderr, stderr: true}
	cmd.Stdout = stdoutStream
	cmd.Stderr = stderrStream
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
		_, _ = stderrStream.Write(note)
	}
	c.mu.Lock()
	stdoutStream.flush()
	stderrStream.flush()
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
