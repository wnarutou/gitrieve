package githubapi

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v56/github"
	"github.com/stretchr/testify/require"
)

func TestCoordinatorLimitsConcurrentRequests(t *testing.T) {
	c := newCoordinator(Config{Concurrency: 1})
	p, err := c.acquire(context.Background(), "core")
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = c.acquire(ctx, "core")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	p.Done(Observation{})

	p2, err := c.acquire(context.Background(), "core")
	require.NoError(t, err)
	p2.Done(Observation{})
}

func TestReadPublicationUsesReadBoundary(t *testing.T) {
	gateHeld := make(chan bool, 1)
	gateReleased := make(chan bool, 1)
	hook := &publicationReadBoundaryTestHook{
		afterLock: func() {
			unexpectedlyUnlocked := publicationMu.TryLock()
			if unexpectedlyUnlocked {
				publicationMu.Unlock()
			}
			gateHeld <- !unexpectedlyUnlocked
		},
		afterUnlock: func() {
			unlocked := publicationMu.TryLock()
			if unlocked {
				publicationMu.Unlock()
			}
			gateReleased <- unlocked
		},
	}
	publicationReadBoundaryHookForTest.Store(hook)
	t.Cleanup(func() { publicationReadBoundaryHookForTest.CompareAndSwap(hook, nil) })

	callbackCalled := false
	ReadPublication(func() { callbackCalled = true })
	require.True(t, callbackCalled)
	select {
	case held := <-gateHeld:
		require.True(t, held, "ReadPublication callback ran without the publication read lock")
	default:
		t.Fatal("ReadPublication bypassed the publication read boundary")
	}
	select {
	case released := <-gateReleased:
		require.True(t, released, "ReadPublication retained the publication read lock")
	default:
		t.Fatal("ReadPublication bypassed the publication unlock boundary")
	}
}

func TestAcquireWaitsForPublicationBeforeSelectingCoordinator(t *testing.T) {
	previous := current.Load()
	old := newCoordinator(Config{Concurrency: 1})
	publicationMu.Lock()
	current.Store(old)
	publicationMu.Unlock()
	t.Cleanup(func() {
		publicationMu.Lock()
		current.Store(previous)
		publicationMu.Unlock()
	})

	oldPermit, err := old.acquire(context.Background(), "core")
	require.NoError(t, err)
	t.Cleanup(func() { oldPermit.Done(Observation{}) })

	publishEntered := make(chan struct{})
	publishRelease := make(chan struct{})
	publishDone := make(chan struct{})
	var publishReleaseOnce sync.Once
	selectionEntered := make(chan struct{})
	selectionProceed := make(chan struct{})
	selectionGateHeld := make(chan bool, 1)
	var selectionProceedOnce sync.Once
	type acquireResult struct {
		permit Permit
		err    error
	}
	acquireDone := make(chan acquireResult, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	var workers sync.WaitGroup
	hook := &publicationReadBoundaryTestHook{
		beforeLock: func() {
			close(selectionEntered)
			<-selectionProceed
		},
		afterLock: func() {
			unexpectedlyUnlocked := publicationMu.TryLock()
			if unexpectedlyUnlocked {
				publicationMu.Unlock()
			}
			selectionGateHeld <- !unexpectedlyUnlocked
		},
	}
	t.Cleanup(func() {
		cancel()
		publishReleaseOnce.Do(func() { close(publishRelease) })
		selectionProceedOnce.Do(func() { close(selectionProceed) })
		workers.Wait()
		publicationReadBoundaryHookForTest.CompareAndSwap(hook, nil)
		select {
		case result := <-acquireDone:
			if result.permit != nil {
				result.permit.Done(Observation{})
			}
		default:
		}
	})

	workers.Add(1)
	go func() {
		defer workers.Done()
		Publish(Config{Concurrency: 2}, func() {
			close(publishEntered)
			<-publishRelease
		})
		close(publishDone)
	}()
	select {
	case <-publishEntered:
	case <-ctx.Done():
		t.Fatal("timed out waiting for publication writer")
	}

	publicationReadBoundaryHookForTest.Store(hook)
	workers.Add(1)
	go func() {
		defer workers.Done()
		permit, acquireErr := Acquire(ctx, "core")
		acquireDone <- acquireResult{permit: permit, err: acquireErr}
	}()

	select {
	case <-selectionEntered:
	case <-ctx.Done():
		t.Fatal("timed out waiting for Acquire to reach the pre-selection barrier")
	}
	select {
	case result := <-acquireDone:
		if result.permit != nil {
			result.permit.Done(Observation{})
		}
		t.Fatal("Acquire returned while blocked inside coordinator selection")
	default:
	}
	if publicationMu.TryRLock() {
		publicationMu.RUnlock()
		t.Fatal("publication writer did not hold the selection gate")
	}

	publishReleaseOnce.Do(func() { close(publishRelease) })
	select {
	case <-publishDone:
	case <-ctx.Done():
		t.Fatal("timed out waiting for publication writer completion")
	}
	selectionProceedOnce.Do(func() { close(selectionProceed) })

	select {
	case gateHeld := <-selectionGateHeld:
		require.True(t, gateHeld, "coordinator selection did not hold the publication read gate")
	case <-ctx.Done():
		t.Fatal("timed out waiting for coordinator selection gate acquisition")
	}

	select {
	case result := <-acquireDone:
		require.NoError(t, result.err)
		require.NotNil(t, result.permit)
		selectedPermit, ok := result.permit.(*permit)
		require.True(t, ok)
		require.NotSame(t, old, selectedPermit.c)
		require.Same(t, current.Load(), selectedPermit.c)
		result.permit.Done(Observation{})
	case <-ctx.Done():
		t.Fatal("Acquire remained blocked on the saturated old coordinator")
	}
}

func TestAcquireReleasesPublicationLockBeforeWaitingForPermit(t *testing.T) {
	previous := current.Load()
	old := newCoordinator(Config{Concurrency: 1})
	publicationMu.Lock()
	current.Store(old)
	publicationMu.Unlock()
	t.Cleanup(func() {
		publicationMu.Lock()
		current.Store(previous)
		publicationMu.Unlock()
	})

	oldPermit, err := old.acquire(context.Background(), "core")
	require.NoError(t, err)
	t.Cleanup(func() { oldPermit.Done(Observation{}) })

	selectionGateHeld := make(chan bool, 1)
	selectionGateReleased := make(chan bool, 1)
	hook := &publicationReadBoundaryTestHook{
		afterLock: func() {
			unexpectedlyUnlocked := publicationMu.TryLock()
			if unexpectedlyUnlocked {
				publicationMu.Unlock()
			}
			selectionGateHeld <- !unexpectedlyUnlocked
		},
		afterUnlock: func() {
			unlocked := publicationMu.TryLock()
			if unlocked {
				publicationMu.Unlock()
			}
			selectionGateReleased <- unlocked
		},
	}
	publicationReadBoundaryHookForTest.Store(hook)

	type acquireResult struct {
		permit Permit
		err    error
	}
	ctx, cancel := context.WithCancel(context.Background())
	acquireDone := make(chan acquireResult, 1)
	var workers sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		workers.Wait()
		publicationReadBoundaryHookForTest.CompareAndSwap(hook, nil)
		select {
		case result := <-acquireDone:
			if result.permit != nil {
				result.permit.Done(Observation{})
			}
		default:
		}
	})
	workers.Add(1)
	go func() {
		defer workers.Done()
		permit, acquireErr := Acquire(ctx, "core")
		acquireDone <- acquireResult{permit: permit, err: acquireErr}
	}()

	select {
	case gateHeld := <-selectionGateHeld:
		require.True(t, gateHeld, "coordinator selection did not hold the publication read gate")
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timed out waiting for coordinator selection gate acquisition")
	}
	select {
	case gateReleased := <-selectionGateReleased:
		require.True(t, gateReleased, "coordinator selection retained the publication read gate")
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timed out waiting for coordinator selection gate release")
	}

	publishDone := make(chan struct{})
	workers.Add(1)
	go func() {
		defer workers.Done()
		Publish(Config{Concurrency: 2}, nil)
		close(publishDone)
	}()
	select {
	case <-publishDone:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("publication was blocked while Acquire waited for a permit")
	}

	cancel()
	select {
	case result := <-acquireDone:
		require.Nil(t, result.permit)
		require.ErrorIs(t, result.err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for canceled Acquire")
	}
}

func TestCoordinatorPausesOnlyObservedResource(t *testing.T) {
	c := newCoordinator(Config{Concurrency: 2, LowRemainingThreshold: 10})
	p, err := c.acquire(context.Background(), "core")
	require.NoError(t, err)
	p.Done(Observation{Resource: "core", Remaining: 10, Reset: time.Now().Add(time.Second)})

	other, err := c.acquire(context.Background(), "graphql")
	require.NoError(t, err)
	other.Done(Observation{})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = c.acquire(ctx, "core")
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestCoordinatorSpacesStartsAfterSemaphoreAcquisition(t *testing.T) {
	c := newCoordinator(Config{Concurrency: 2, MinRequestInterval: 40 * time.Millisecond})
	starts := make(chan time.Time, 2)
	for i := 0; i < 2; i++ {
		go func() {
			p, err := c.acquire(context.Background(), "core")
			require.NoError(t, err)
			starts <- time.Now()
			p.Done(Observation{})
		}()
	}
	a, b := <-starts, <-starts
	if b.Before(a) {
		a, b = b, a
	}
	require.GreaterOrEqual(t, b.Sub(a), 30*time.Millisecond)
}

func TestObserveErrorSecondaryDefaultsToOneMinute(t *testing.T) {
	err := &github.AbuseRateLimitError{Response: &http.Response{StatusCode: http.StatusForbidden}}
	obs := ObserveError(err)
	require.True(t, obs.Secondary)
	require.Equal(t, time.Minute, obs.RetryAfter)
}

func TestObserveRESTReadsRateHeaders(t *testing.T) {
	reset := time.Now().Add(time.Hour).Unix()
	r := &github.Response{Response: &http.Response{Header: http.Header{}}}
	r.Header.Set("X-RateLimit-Resource", "core")
	r.Header.Set("X-RateLimit-Limit", "5000")
	r.Header.Set("X-RateLimit-Remaining", "99")
	r.Header.Set("X-RateLimit-Used", "4901")
	r.Header.Set("X-RateLimit-Reset", time.Unix(reset, 0).Format("150405"))
	// Use the go-github parsed Rate values for numeric quota/reset data.
	r.Rate = github.Rate{Limit: 5000, Remaining: 99, Reset: github.Timestamp{Time: time.Unix(reset, 0)}}
	obs := ObserveREST(r, nil)
	require.Equal(t, "core", obs.Resource)
	require.Equal(t, 5000, obs.Limit)
	require.Equal(t, 99, obs.Remaining)
	require.Equal(t, time.Unix(reset, 0), obs.Reset)
}
