package githubapi

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/go-github/v56/github"
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

var current atomic.Pointer[coordinator]

func newCoordinator(cfg Config) *coordinator {
	if cfg.Concurrency == 0 {
		cfg.Concurrency = 1
	}
	return &coordinator{cfg: cfg, sem: make(chan struct{}, cfg.Concurrency), resourcePause: make(map[string]time.Time)}
}

func Configure(cfg Config) { current.Store(newCoordinator(cfg)) }

func Acquire(ctx context.Context, resource string) (Permit, error) {
	c := current.Load()
	if c == nil {
		c = newCoordinator(Config{Concurrency: 1})
		current.CompareAndSwap(nil, c)
		c = current.Load()
	}
	return c.acquire(ctx, resource)
}

func (c *coordinator) acquire(ctx context.Context, resource string) (Permit, error) {
	for {
		c.mu.Lock()
		now := time.Now()
		until := c.secondaryPause
		if r := c.resourcePause[resource]; r.After(until) {
			until = r
		}
		if c.nextStart.After(until) {
			until = c.nextStart
		}
		c.mu.Unlock()
		if !until.After(now) {
			break
		}
		t := time.NewTimer(time.Until(until))
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	c.mu.Lock()
	c.nextStart = time.Now().Add(c.cfg.MinRequestInterval)
	c.mu.Unlock()
	return &permit{c: c}, nil
}

func (p *permit) Done(obs Observation) {
	p.once.Do(func() {
		p.c.observe(obs)
		<-p.c.sem
	})
}

func (c *coordinator) observe(obs Observation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if obs.Secondary {
		d := obs.RetryAfter
		if d <= 0 {
			d = time.Minute
		}
		if until := now.Add(d); until.After(c.secondaryPause) {
			c.secondaryPause = until
		}
	}
	if obs.Resource != "" && !obs.Reset.IsZero() && obs.Remaining <= c.cfg.LowRemainingThreshold && obs.Reset.After(c.resourcePause[obs.Resource]) {
		c.resourcePause[obs.Resource] = obs.Reset
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
