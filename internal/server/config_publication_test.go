package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/db"
	"github.com/wnarutou/gitrieve/internal/executor"
	"github.com/wnarutou/gitrieve/internal/logger"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

type generationBlockingScheduleRefresher struct {
	api          *API
	firstRelease chan struct{}
	entered      chan int
	calls        atomic.Int32
	outsideLock  atomic.Bool

	mu      sync.Mutex
	configs []*config.Config
}

func (r *generationBlockingScheduleRefresher) RefreshSchedules(cfg *config.Config) error {
	if r.api.configMu.TryLock() {
		r.outsideLock.Store(true)
		r.api.configMu.Unlock()
	}
	call := int(r.calls.Add(1))
	if call == 1 {
		r.entered <- call
		<-r.firstRelease
	}
	r.mu.Lock()
	r.configs = append(r.configs, config.Clone(cfg))
	r.mu.Unlock()
	if call != 1 {
		r.entered <- call
	}
	return nil
}

func (r *generationBlockingScheduleRefresher) lastConfig() *config.Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.configs) == 0 {
		return nil
	}
	return config.Clone(r.configs[len(r.configs)-1])
}

func TestConfigMutationSerializesPublishPersistenceAndScheduleRefresh(t *testing.T) {
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	cfg := &config.Config{Repository: []typedef.Repository{{Name: "base", URL: "github.com/acme/base"}}}
	exec := executor.NewExecutorWithRunners(logger.NewLogger(testDB), testDB, cfg, executor.Runners{})
	t.Cleanup(func() {
		require.NoError(t, exec.Close())
		require.NoError(t, testDB.Close())
	})
	api := NewAPI(cfg, testDB, exec)
	refresher := &generationBlockingScheduleRefresher{
		api:          api,
		firstRelease: make(chan struct{}),
		entered:      make(chan int, 2),
	}
	api.SetScheduleRefresher(refresher)
	router := gin.New()
	router.POST("/api/config/import", api.ApplyImport)
	router.POST("/api/repositories", api.CreateRepository)

	importBody, err := json.Marshal(map[string]string{
		"config": "repository:\n  - name: generation-a\n    url: github.com/acme/base\n",
	})
	require.NoError(t, err)
	type response struct {
		code int
		body string
	}
	applyDone := make(chan response, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/config/import", bytes.NewReader(importBody))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		applyDone <- response{code: resp.Code, body: resp.Body.String()}
	}()
	require.Equal(t, 1, <-refresher.entered)

	crudStarted := make(chan struct{})
	crudDone := make(chan response, 1)
	go func() {
		close(crudStarted)
		req := httptest.NewRequest(http.MethodPost, "/api/repositories", bytes.NewBufferString(
			`{"Name":"generation-b","URL":"github.com/acme/generation-b"}`,
		))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		crudDone <- response{code: resp.Code, body: resp.Body.String()}
	}()
	<-crudStarted

	if refresher.outsideLock.Load() {
		// On the broken implementation, let B finish its refresh before A so
		// A deterministically overwrites the scheduler with its stale snapshot.
		require.Equal(t, 2, <-refresher.entered)
		close(refresher.firstRelease)
	} else {
		// With the generation lock held, B cannot publish until A completes.
		close(refresher.firstRelease)
		require.Equal(t, 2, <-refresher.entered)
	}
	applyResponse := <-applyDone
	crudResponse := <-crudDone
	require.Equal(t, http.StatusOK, applyResponse.code, applyResponse.body)
	require.Equal(t, http.StatusOK, crudResponse.code, crudResponse.body)
	require.False(t, refresher.outsideLock.Load(), "schedule refresh ran outside the config generation lock")

	assertGenerationB := func(label string, snapshot *config.Config) {
		t.Helper()
		require.NotNil(t, snapshot, label)
		require.Len(t, snapshot.Repository, 2, label)
		require.Equal(t, "generation-a", snapshot.Repository[0].Name, label)
		require.Equal(t, "generation-b", snapshot.Repository[1].Name, label)
	}
	assertGenerationB("API", api.configSnapshot())
	assertGenerationB("Executor", exec.RuntimeConfigSnapshot().Config())
	assertGenerationB("global", config.GetIns())
	assertGenerationB("scheduler", refresher.lastConfig())
}
