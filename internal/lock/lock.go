// Package lock serializes concurrent writes to the same gitrieve resource.
// go-git has no internal locking, so two goroutines or processes syncing the
// same repo+component (daemon overlap, executor re-trigger, parallel CLI runs)
// could corrupt the shared .git object database or storage paths. The lock is
// keyed by (repo, component): different repos and the code/wiki components stay
// fully parallel.
package lock

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/wnarutou/gitrieve/internal/scm"
)

// In-process layer: a binary semaphore per key. This guarantees exclusion
// between goroutines in the same process on every platform — Windows byte-range
// lock semantics for same-process re-locking across handles are unreliable, so
// we cannot rely on the file lock alone for the executor fast-re-trigger case.
var (
	procLocks   = make(map[string]chan struct{})
	procLocksMu sync.Mutex
)

// Acquire takes an exclusive lock for (r, component) and returns a release
// function that must be called exactly once. While waiting, a cancelled ctx
// makes Acquire return ctx.Err() promptly. The lock file lives under
// .gitrieve/locks and is never deleted (unlinking would race between holders).
// The lock is advisory and per-host + per-working-directory: it does not guard
// multi-host writes to shared storage.
func Acquire(ctx context.Context, r *scm.Repository, component string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := path.Join(r.Host, r.Owner, r.Name, component)

	procLocksMu.Lock()
	ch, ok := procLocks[key]
	if !ok {
		ch = make(chan struct{}, 1)
		procLocks[key] = ch
	}
	procLocksMu.Unlock()

	select {
	case ch <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	releaseProc := func() { <-ch }

	cwd, err := os.Getwd()
	if err != nil {
		releaseProc()
		return nil, err
	}
	lockPath := filepath.Join(cwd, ".gitrieve", "locks", r.Host, r.Owner, r.Name, component+".lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		releaseProc()
		return nil, err
	}

	f := flock.New(lockPath)
	locked, err := f.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		releaseProc()
		return nil, err
	}
	if !locked {
		releaseProc()
		return nil, fmt.Errorf("lock for %s not acquired", key)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			_ = f.Unlock()
			_ = f.Close()
			releaseProc()
		})
	}, nil
}
