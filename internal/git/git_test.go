package git

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var ctx = context.Background()

// fixture is a throwaway repository with two commits on master.
type fixture struct {
	dir    string
	c1, c2 string
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Alice Author", "GIT_AUTHOR_EMAIL=alice@example.com",
		"GIT_COMMITTER_NAME=Carol Committer", "GIT_COMMITTER_EMAIL=carol@example.com",
		"GIT_AUTHOR_DATE=1700000000 +0000", "GIT_COMMITTER_DATE=1700003600 +0000",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "master")
	write(t, filepath.Join(dir, "a.c"), "int a;\nint b;\n")
	gitRun(t, dir, "add", "a.c")
	gitRun(t, dir, "commit", "-q", "-m", "Initial commit")
	c1 := gitRun(t, dir, "rev-parse", "HEAD")
	write(t, filepath.Join(dir, "a.c"), "int a;\n  int b;\nint c;\n")
	gitRun(t, dir, "add", "a.c")
	gitRun(t, dir, "commit", "-q", "-m", "net: fix leak in foo")
	c2 := gitRun(t, dir, "rev-parse", "HEAD")
	return &fixture{dir: dir, c1: c1, c2: c2}
}

// Stream must stop git when the reader gives up, whatever is still in the
// pipe: a git blocked on a full pipe would hang Wait forever.
func TestStream(t *testing.T) {
	fx := newFixture(t)
	big := strings.Repeat("0123456789abcdef", 1<<16) // 1 MiB, far over a pipe buffer
	write(t, filepath.Join(fx.dir, "big"), big)
	blob := gitRun(t, fx.dir, "hash-object", "-w", "big")

	within := func(t *testing.T, f func() error) error {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- f() }()
		select {
		case err := <-done:
			return err
		case <-time.After(30 * time.Second):
			t.Fatal("Stream did not return")
			return nil
		}
	}

	var got int64
	err := within(t, func() error {
		return Stream(ctx, fx.dir, nil, func(r io.Reader) (err error) {
			got, err = io.Copy(io.Discard, r)
			return err
		}, "cat-file", "blob", blob)
	})
	if err != nil || got != int64(len(big)) {
		t.Fatalf("whole read: %d bytes, %v", got, err)
	}

	errStop := errors.New("stop")
	err = within(t, func() error {
		return Stream(ctx, fx.dir, nil, func(r io.Reader) error {
			_, err := io.ReadFull(r, make([]byte, 10))
			return errors.Join(err, errStop)
		}, "cat-file", "blob", blob)
	})
	if !errors.Is(err, errStop) {
		t.Errorf("reader error: got %v", err)
	}

	// A reader that returns early without an error leaves the rest unread.
	err = within(t, func() error {
		return Stream(ctx, fx.dir, nil, func(r io.Reader) error {
			_, err := io.ReadFull(r, make([]byte, 10))
			return err
		}, "cat-file", "blob", blob)
	})
	if err != nil {
		t.Errorf("early return: %v", err)
	}

	var stdinOut string
	err = Stream(ctx, fx.dir, []byte(fx.c2+"\n"), func(r io.Reader) error {
		b, err := io.ReadAll(r)
		stdinOut = string(b)
		return err
	}, "log", "--stdin", "--no-walk", "--format=%s")
	if err != nil || stdinOut != "net: fix leak in foo\n" {
		t.Errorf("stdin: %q, %v", stdinOut, err)
	}

	var gitErr *Error
	err = Stream(ctx, fx.dir, nil, func(r io.Reader) error {
		_, err := io.Copy(io.Discard, r)
		return err
	}, "cat-file", "blob", strings.Repeat("0", 40))
	if !errors.As(err, &gitErr) || gitErr.Stderr == "" {
		t.Errorf("git failure: got %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	err = within(t, func() error {
		return Stream(cancelled, fx.dir, nil, func(r io.Reader) error {
			_, err := io.Copy(io.Discard, r)
			return err
		}, "cat-file", "blob", blob)
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: got %v", err)
	}
}
