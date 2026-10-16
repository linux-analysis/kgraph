// Package git drives the git command line: it runs commands in a
// repository and streams their output, and depends on the standard
// library only.
package git

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Error is a failed git invocation.
type Error struct {
	Args   []string
	Dir    string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("git %s", strings.Join(e.Args, " "))
	if e.Dir != "" {
		msg += " (in " + e.Dir + ")"
	}
	msg += ": " + e.Err.Error()
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// command prepares git with args in dir: no prompts, the C locale, and
// SIGTERM with a grace period when ctx ends.
func command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// Run executes git with args in dir, feeding stdin when non-nil, and
// returns stdout.
func Run(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
	var out []byte
	err := Stream(ctx, dir, stdin, func(r io.Reader) (err error) {
		out, err = io.ReadAll(r)
		return err
	}, args...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Stream executes git with args in dir, feeding stdin when non-nil, and
// hands its stdout to read as it arrives, for output too large to hold.
// When read fails, git is stopped rather than left blocked on a full pipe;
// when it returns early without an error, the rest of the output is
// discarded. The error is ctx's when it ended, else read's, else git's.
func Stream(ctx context.Context, dir string, stdin []byte, read func(io.Reader) error, args ...string) error {
	gctx, stop := context.WithCancel(ctx)
	defer stop()
	cmd := command(gctx, dir, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &Error{Args: args, Dir: dir, Err: err}
	}
	readErr := read(out)
	if readErr != nil {
		stop()
	} else {
		_, readErr = io.Copy(io.Discard, out)
	}
	err = cmd.Wait()
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case readErr != nil:
		return readErr
	case err != nil:
		return &Error{Args: args, Dir: dir, Stderr: strings.TrimSpace(stderr.String()), Err: err}
	}
	return nil
}
