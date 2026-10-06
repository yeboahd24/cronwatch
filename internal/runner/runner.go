package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/yeboahd24/cronwatch/internal/logs"
	"github.com/yeboahd24/cronwatch/internal/model"
)

type Result struct {
	Stdout, Stderr, Combined string
	Truncated                bool
	ExitCode                 int
	Status                   string // success, failed, cancelled or timeout
	Duration                 time.Duration
	Usage                    *model.Usage // nil if the command never started
}

// LineFunc is called with each line of output as it is captured, before any
// of it is dropped to fit the log limits. line excludes the newline, a line
// longer than 64 KiB arrives in 64 KiB pieces, and line is only valid during
// the call. Calls are serialized, in the order the lines reach the combined log.
type LineFunc func(line []byte, stderr bool)

type capture struct {
	mu                       sync.Mutex
	stdout, stderr, combined *boundedBuffer
	onLine                   LineFunc
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
	if s.c.onLine != nil {
		s.c.onLine(line, s.stderr)
	}
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

// Output is the output captured so far, as kept within the log limits.
type Output struct {
	Stdout, Stderr, Combined string
	Truncated                bool
	Bytes                    int64 // written so far, including any dropped
}

// Options configures Execute.
type Options struct {
	// Stdout and Stderr, if not nil, also receive the command's output.
	Stdout, Stderr io.Writer
	// MaxLogBytes bounds the output kept per stream; the combined log keeps
	// twice that.
	MaxLogBytes int64
	// OnLine, if not nil, sees every line as it is captured.
	OnLine LineFunc
	// Progress, if not nil, is called from another goroutine with the output
	// so far, at most every ProgressEvery and only when there is new output,
	// while the command runs. Execute waits for a call in progress before it
	// returns. ProgressAfter, if not zero, is the slower interval used once
	// the output kept has grown past ProgressLarge bytes.
	Progress                     func(Output)
	ProgressEvery, ProgressAfter time.Duration
	ProgressLarge                int
}

// snapshot returns the output so far. The caller holds c.mu.
func (c *capture) snapshot() Output {
	return Output{Stdout: c.stdout.String(), Stderr: c.stderr.String(), Combined: c.combined.String(),
		Truncated: c.stdout.Truncated() || c.stderr.Truncated() || c.combined.Truncated(), Bytes: c.stdout.total + c.stderr.total}
}

// reportProgress calls opts.Progress with new output until done is closed.
func reportProgress(c *capture, opts Options, done <-chan struct{}) {
	every := opts.ProgressEvery
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	var reported int64
	var last time.Time // of the last report
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
		}
		c.mu.Lock()
		if c.stdout.total+c.stderr.total == reported {
			c.mu.Unlock()
			continue
		}
		out := c.snapshot()
		c.mu.Unlock()
		size := len(out.Stdout) + len(out.Stderr) + len(out.Combined)
		if opts.ProgressAfter > 0 && size > opts.ProgressLarge && !last.IsZero() && time.Since(last) < opts.ProgressAfter {
			continue
		}
		reported, last = out.Bytes, time.Now()
		opts.Progress(out)
	}
}

func Execute(ctx context.Context, command []string, opts Options) (Result, error) {
	var result Result
	maxLogBytes := opts.MaxLogBytes
	if maxLogBytes < 0 {
		return result, errors.New("max log bytes must be non-negative")
	}
	c := &capture{stdout: newBoundedBuffer(maxLogBytes), stderr: newBoundedBuffer(maxLogBytes), combined: newBoundedBuffer(2 * maxLogBytes), onLine: opts.OnLine}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	stdoutStream := &stream{c: c, dst: c.stdout, echo: opts.Stdout}
	stderrStream := &stream{c: c, dst: c.stderr, echo: opts.Stderr, stderr: true}
	cmd.Stdout = stdoutStream
	cmd.Stderr = stderrStream
	// Run the child in its own process group so cancellation also reaches
	// grandchildren such as the commands started by "sh -c".
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	var progress sync.WaitGroup
	done := make(chan struct{})
	if opts.Progress != nil && opts.ProgressEvery > 0 {
		progress.Go(func() { reportProgress(c, opts, done) })
	}
	started := time.Now()
	err := cmd.Run()
	result.Duration = time.Since(started)
	close(done)
	progress.Wait()
	if ctx.Err() != nil && cmd.Process != nil {
		// WaitDelay's SIGKILL reaches only the child; finish off the rest of
		// its group, such as grandchildren that ignored SIGTERM.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	exit, isExit := errors.AsType[*exec.ExitError](err)
	if err != nil && !isExit {
		// The child never ran (e.g. not found), so record why in the logs.
		note := []byte("cronwatch: " + err.Error() + "\n")
		_, _ = stderrStream.Write(note)
	}
	c.mu.Lock()
	stdoutStream.flush()
	stderrStream.flush()
	out := c.snapshot()
	c.mu.Unlock()
	result.Stdout, result.Stderr, result.Combined, result.Truncated = out.Stdout, out.Stderr, out.Combined, out.Truncated
	result.Status = "success"
	result.Usage = usage(cmd.ProcessState)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.Status = "timeout"
	} else if ctx.Err() != nil {
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

// usage reads the resources a finished child used, including the children it
// waited for.
func usage(ps *os.ProcessState) *model.Usage {
	if ps == nil {
		return nil
	}
	ru, ok := ps.SysUsage().(*syscall.Rusage)
	if !ok {
		return nil
	}
	maxRSS := int64(ru.Maxrss)
	if runtime.GOOS == "darwin" {
		maxRSS /= 1024 // bytes on macOS, kilobytes on Linux
	}
	return &model.Usage{
		MaxRSSKB:  maxRSS,
		UserCPUMS: time.Duration(ru.Utime.Nano()).Milliseconds(),
		SysCPUMS:  time.Duration(ru.Stime.Nano()).Milliseconds(),
	}
}
