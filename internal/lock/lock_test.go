package lock

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/scm"
)

func TestAcquireWithCancelledCtxFailsFast(t *testing.T) {
	r := &scm.Repository{Host: "github.com", Owner: "test", Name: "repo"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Acquire(ctx, r, "code")
	require.ErrorIs(t, err, context.Canceled)
}

func TestAcquireDifferentKeysDoNotBlock(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll(".gitrieve") })
	a := &scm.Repository{Host: "github.com", Owner: "test", Name: "repo-a"}
	b := &scm.Repository{Host: "github.com", Owner: "test", Name: "repo-b"}

	releaseA, err := Acquire(context.Background(), a, "code")
	require.NoError(t, err)
	defer releaseA()

	releaseB, err := Acquire(context.Background(), b, "code")
	require.NoError(t, err)
	releaseB()
}

func TestAcquireSameKeySerializes(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll(".gitrieve") })
	r := &scm.Repository{Host: "github.com", Owner: "test", Name: "repo"}

	releaseA, err := Acquire(context.Background(), r, "code")
	require.NoError(t, err)

	acquired := make(chan struct{})
	var acquireErr error
	go func() {
		releaseB, err := Acquire(context.Background(), r, "code")
		if err == nil {
			releaseB()
		}
		acquireErr = err
		close(acquired)
	}()

	// Second Acquire must block while the first holds the lock.
	select {
	case <-acquired:
		t.Fatal("second Acquire should block while the first holds the lock")
	case <-time.After(200 * time.Millisecond):
	}

	releaseA() // unblock the waiter

	select {
	case <-acquired:
		require.NoError(t, acquireErr)
	case <-time.After(2 * time.Second):
		t.Fatal("second Acquire should proceed after the first releases")
	}
}

func TestAcquireCancelledWhileWaiting(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll(".gitrieve") })
	r := &scm.Repository{Host: "github.com", Owner: "test", Name: "repo"}

	releaseA, err := Acquire(context.Background(), r, "code")
	require.NoError(t, err)
	defer releaseA()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		releaseB, err := Acquire(ctx, r, "code")
		if err == nil {
			releaseB()
		}
		result <- err
	}()

	time.Sleep(100 * time.Millisecond) // let the waiter start blocking
	cancel()

	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("Acquire should return when the context is cancelled while waiting")
	}
}

// TestCrossProcessLock proves the file lock excludes a second *process*, and
// that the OS releases it when the holder is killed.
func TestCrossProcessLock(t *testing.T) {
	if os.Getenv("LOCK_HELPER") == "1" {
		// Child: acquire and hold until killed; never release explicitly.
		r := &scm.Repository{Host: "github.com", Owner: "test", Name: "xproc-repo"}
		release, err := Acquire(context.Background(), r, "code")
		if err != nil {
			fmt.Fprintf(os.Stderr, "child acquire: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("LOCKED")
		time.Sleep(60 * time.Second)
		_ = release
		os.Exit(0)
	}

	t.Cleanup(func() { os.RemoveAll(".gitrieve") })
	r := &scm.Repository{Host: "github.com", Owner: "test", Name: "xproc-repo"}

	cmd := exec.Command(os.Args[0], "-test.run=^TestCrossProcessLock$")
	cmd.Env = append(os.Environ(), "LOCK_HELPER=1")
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())

	// Always reap the child, even if a later assertion fails.
	killed := false
	defer func() {
		if !killed {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	lockedCh := make(chan struct{})
	go func() {
		br := bufio.NewReader(stdout)
		line, _ := br.ReadString('\n')
		if strings.Contains(line, "LOCKED") {
			close(lockedCh)
		}
	}()
	select {
	case <-lockedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("child process never acquired the lock")
	}

	// The child holds the lock: a same-key Acquire from this process blocks.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	_, err = Acquire(ctx, r, "code")
	cancel()
	require.ErrorIs(t, err, context.DeadlineExceeded, "parent must be blocked while the child holds the lock")

	// Killing the child releases the lock (advisory lock dies with the process).
	// Wait returns *exec.ExitError because the child was killed, not a clean exit.
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
	killed = true

	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	release, err := Acquire(ctx, r, "code")
	cancel()
	require.NoError(t, err)
	release()
}
