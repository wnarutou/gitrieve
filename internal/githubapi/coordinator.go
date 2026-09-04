package githubapi

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/go-github/v56/github"
	"github.com/wnarutou/gitrieve/internal/ui"
)

type Config struct {
	Concurrency           uint
	MinRequestInterval    time.Duration
	LowRemainingThreshold int
}

type Observation struct {
	Resource               string
	Limit, Remaining, Used int
	Reset                  time.Time
	Secondary              bool
	RetryAfter             time.Duration
}

type Permit interface{ Done(Observation) }

type coordinator struct {
	cfg            Config
	sem            chan struct{}
	mu             sync.Mutex
	nextStart      time.Time
	resourcePause  map[string]time.Time
	secondaryPause time.Time
}

type permit struct {
	c    *coordinator
	once sync.Once
}

type scopeContextKey struct{}

// Scope owns the API coordinator for one immutable runtime configuration
// generation. Jobs from the same generation share pacing and quota state,
// while an already-admitted old job never switches to a newly published one.
type Scope struct {
	cfg         Config
	coordinator *coordinator
}

func NewScope(cfg Config) *Scope {
	return &Scope{cfg: cfg, coordinator: newCoordinator(cfg)}
}

func WithScope(ctx context.Context, scope *Scope) context.Context {
	if scope == nil {
		return ctx
	}
	return context.WithValue(ctx, scopeContextKey{}, scope)
}

func ConfigFromContext(ctx context.Context) (Config, bool) {
	if ctx == nil {
		return Config{}, false
	}
	scope, ok := ctx.Value(scopeContextKey{}).(*Scope)
	if !ok || scope == nil {
		return Config{}, false
	}
	return scope.cfg, true
}

var current atomic.Pointer[coordinator]
var publicationMu sync.RWMutex

// publicationReadBoundaryTestHook is a package-private synchronization seam
// for tests that need to observe the exact publication read-lock boundary.
type publicationReadBoundaryTestHook struct {
	beforeLock  func()
	afterLock   func()
	afterUnlock func()
}

var publicationReadBoundaryHookForTest atomic.Pointer[publicationReadBoundaryTestHook]

func newCoordinator(cfg Config) *coordinator {
	if cfg.Concurrency == 0 {
		cfg.Concurrency = 1
	}
	return &coordinator{cfg: cfg, sem: make(chan struct{}, cfg.Concurrency), resourcePause: make(map[string]time.Time)}
}

// Publish installs an application configuration snapshot and its matching
// GitHub coordinator under one reader-visible publication boundary. install
// must only publish immutable state; it runs while publicationMu is held.
func Publish(cfg Config, install func()) {
	next := newCoordinator(cfg)
	publicationMu.Lock()
	defer publicationMu.Unlock()
	if install != nil {
		install()
	}
	current.Store(next)
}

// ReadPublication runs read while no paired configuration/coordinator
// publication is in progress.
func ReadPublication(read func()) {
	readPublication(read)
}

func readPublication(read func()) {
	hook := publicationReadBoundaryHookForTest.Load()
	if hook != nil && hook.beforeLock != nil {
		hook.beforeLock()
	}
	publicationMu.RLock()
	defer func() {
		publicationMu.RUnlock()
		if hook != nil && hook.afterUnlock != nil {
			hook.afterUnlock()
		}
	}()
	if hook != nil && hook.afterLock != nil {
		hook.afterLock()
	}
	read()
}

func Configure(cfg Config) { Publish(cfg, nil) }

func Acquire(ctx context.Context, resource string) (Permit, error) {
	if ctx != nil {
		if scope, ok := ctx.Value(scopeContextKey{}).(*Scope); ok && scope != nil {
			return scope.coordinator.acquire(ctx, resource)
		}
	}
	c := loadCoordinator()
	return c.acquire(ctx, resource)
}

func loadCoordinator() *coordinator {
	var c *coordinator
	readPublication(func() {
		c = current.Load()
	})
	if c != nil {
		return c
	}

	publicationMu.Lock()
	defer publicationMu.Unlock()
	if c = current.Load(); c == nil {
		c = newCoordinator(Config{Concurrency: 1})
		current.Store(c)
	}
	return c
}

func (c *coordinator) acquire(ctx context.Context, resource string) (Permit, error) {
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	for {
		c.mu.Lock()
		now := time.Now()
		resumed := false
		if !c.secondaryPause.IsZero() && !c.secondaryPause.After(now) {
			c.secondaryPause = time.Time{}
			resumed = true
		}
		if r, ok := c.resourcePause[resource]; ok && !r.After(now) {
			delete(c.resourcePause, resource)
			resumed = true
		}
		until := c.secondaryPause
		if r := c.resourcePause[resource]; r.After(until) {
			until = r
		}
		if c.nextStart.After(until) {
			until = c.nextStart
		}
		if !until.After(now) {
			c.nextStart = now.Add(c.cfg.MinRequestInterval)
			c.mu.Unlock()
			if resumed {
				ui.Printf("GitHub API traffic resumed for %s", resource)
			}
			return &permit{c: c}, nil
		}
		c.mu.Unlock()
		t := time.NewTimer(time.Until(until))
		select {
		case <-ctx.Done():
			t.Stop()
			<-c.sem
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

func (p *permit) Done(obs Observation) {
	p.once.Do(func() {
		p.c.observe(obs)
		<-p.c.sem
	})
}

func (c *coordinator) observe(obs Observation) {
	c.mu.Lock()
	now := time.Now()
	secondaryExtended := false
	var secondaryUntil time.Time
	resourceExtended := false
	if obs.Secondary {
		d := obs.RetryAfter
		if d <= 0 {
			d = time.Minute
		}
		if until := now.Add(d); until.After(c.secondaryPause) {
			c.secondaryPause = until
			secondaryUntil = until
			secondaryExtended = true
		}
	}
	if obs.Resource != "" && !obs.Reset.IsZero() && obs.Remaining <= c.cfg.LowRemainingThreshold && obs.Reset.After(c.resourcePause[obs.Resource]) {
		c.resourcePause[obs.Resource] = obs.Reset
		resourceExtended = true
	}
	c.mu.Unlock()
	if secondaryExtended {
		ui.Printf("GitHub API secondary-limit pause active until %s", secondaryUntil.Format(time.RFC3339))
	}
	if resourceExtended {
		ui.Printf("GitHub API %s quota low (%d remaining); paused until %s", obs.Resource, obs.Remaining, obs.Reset.Format(time.RFC3339))
	}
}

func ObserveError(err error) Observation {
	var abuse *github.AbuseRateLimitError
	if errors.As(err, &abuse) {
		d := time.Minute
		if abuse.RetryAfter != nil {
			d = *abuse.RetryAfter
		}
		return Observation{Secondary: true, RetryAfter: d}
	}
	var limited *github.RateLimitError
	if errors.As(err, &limited) {
		return Observation{Resource: "core", Remaining: 0, Reset: limited.Rate.Reset.Time}
	}
	return Observation{}
}

func ObserveREST(resp *github.Response, err error) Observation {
	obs := ObserveError(err)
	if resp == nil {
		return obs
	}
	obs.Limit = resp.Rate.Limit
	obs.Remaining = resp.Rate.Remaining
	obs.Reset = resp.Rate.Reset.Time
	if resp.Response != nil {
		obs.Resource = resp.Header.Get("X-RateLimit-Resource")
		obs.Used, _ = strconv.Atoi(resp.Header.Get("X-RateLimit-Used"))
	}
	return obs
}
