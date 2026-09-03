package config

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/githubapi"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

// writeTmpConfig points the package global at a temp config file and loads it,
// mirroring the pattern used by internal/release tests. Cleanup resets Path.
func writeTmpConfig(t *testing.T, content string) {
	t.Helper()
	tmp, err := os.CreateTemp(t.TempDir(), "config-*.yaml")
	require.NoError(t, err)
	_, err = tmp.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, tmp.Close())
	Path = tmp.Name()
	Init()
	t.Cleanup(func() { Path = "" })
}

func TestGetRetryDefaults(t *testing.T) {
	writeTmpConfig(t, "githubtoken: test\n")
	require.Equal(t, 3, GetRetryMaxCount())
	require.Equal(t, 5*time.Second, GetRetryBaseDelay())
	rc := GetRetryConfig()
	require.Equal(t, 3, rc.MaxRetries)
	require.Equal(t, 5*time.Second, rc.BaseDelay)
}

func TestSyncHealthDefaults(t *testing.T) {
	cfg := &Config{}
	seedDefaults(cfg)
	require.Equal(t, 30*time.Minute, cfg.SyncOverdueGrace)
	require.Equal(t, 24*time.Hour, cfg.SyncStuckThreshold)
}

func TestSyncHealthNonPositiveValuesUseDefaults(t *testing.T) {
	cfg := &Config{SyncOverdueGrace: -time.Second, SyncStuckThreshold: -time.Second}
	seedDefaults(cfg)
	require.Equal(t, DefaultSyncOverdueGrace, cfg.SyncOverdueGrace)
	require.Equal(t, DefaultSyncStuckThreshold, cfg.SyncStuckThreshold)
}

func TestSyncHealthGettersAndSaveRoundTrip(t *testing.T) {
	writeTmpConfig(t, "githubtoken: test\nsyncOverdueGrace: 45m\nsyncStuckThreshold: 12h\n")
	require.Equal(t, 45*time.Minute, GetSyncOverdueGrace())
	require.Equal(t, 12*time.Hour, GetSyncStuckThreshold())

	require.NoError(t, Save())
	Init()
	require.Equal(t, 45*time.Minute, GetSyncOverdueGrace())
	require.Equal(t, 12*time.Hour, GetSyncStuckThreshold())
}

func TestGetGitHubAPIDefaults(t *testing.T) {
	writeTmpConfig(t, "githubtoken: test\n")
	require.Equal(t, uint(2), GetGitHubAPIConcurrency())
	require.Equal(t, 200*time.Millisecond, GetGitHubMinRequestInterval())
	require.Equal(t, 100, GetGitHubLowRemainingThreshold())
	require.Equal(t, 30*time.Second, GetGitHubScheduleJitter())
}

func TestExplicitZeroGitHubScheduleJitterDisablesStaggering(t *testing.T) {
	writeTmpConfig(t, "githubtoken: test\ngithubScheduleJitter: 0s\n")
	require.Zero(t, GetGitHubScheduleJitter())
}

func TestGitHubAPISettingsRoundTrip(t *testing.T) {
	writeTmpConfig(t, "githubApiConcurrency: 4\ngithubMinRequestInterval: 750ms\ngithubLowRemainingThreshold: 250\ngithubScheduleJitter: 12s\n")
	require.NoError(t, Save())
	Init()
	require.Equal(t, uint(4), GetGitHubAPIConcurrency())
	require.Equal(t, 750*time.Millisecond, GetGitHubMinRequestInterval())
	require.Equal(t, 250, GetGitHubLowRemainingThreshold())
	require.Equal(t, 12*time.Second, GetGitHubScheduleJitter())
}

func TestGetRetryFromConfig(t *testing.T) {
	writeTmpConfig(t, "githubtoken: test\nretryMaxCount: 7\nretryBaseDelay: 10s\n")
	require.Equal(t, 7, GetRetryMaxCount())
	require.Equal(t, 10*time.Second, GetRetryBaseDelay())
	rc := GetRetryConfig()
	require.Equal(t, 7, rc.MaxRetries)
	require.Equal(t, 10*time.Second, rc.BaseDelay)
}

func TestGetRetryNonPositiveDefaults(t *testing.T) {
	// A negative retryMaxCount means "unset -> default", not a no-op config.
	writeTmpConfig(t, "githubtoken: test\nretryMaxCount: -1\n")
	require.Equal(t, 3, GetRetryMaxCount())
	require.Equal(t, 3, GetRetryConfig().MaxRetries)

	// A negative retryBaseDelay means "unset -> default".
	writeTmpConfig(t, "githubtoken: test\nretryBaseDelay: -5s\n")
	require.Equal(t, 5*time.Second, GetRetryBaseDelay())
}

func TestSaveRetryRoundTrip(t *testing.T) {
	writeTmpConfig(t, "githubtoken: test\nretryMaxCount: 7\nretryBaseDelay: 10s\n")
	require.Equal(t, 7, GetRetryMaxCount())
	require.Equal(t, 10*time.Second, GetRetryBaseDelay())

	require.NoError(t, Save())

	// Re-load from the same (rewritten) file: explicit values must survive.
	Init()
	require.Equal(t, 7, GetRetryMaxCount())
	require.Equal(t, 10*time.Second, GetRetryBaseDelay())
}

func TestGetLegacyDefaultsSeededByInit(t *testing.T) {
	// Config omits the fields: Init seeds the defaults, so the getters must
	// return them without any lazy mutation (which would race under the
	// daemon's concurrent release workers).
	writeTmpConfig(t, "githubtoken: test\n")
	require.Equal(t, uint(3), GetConcurrencyNum())
	require.Equal(t, 3, GetReleaseNumLimit())
	require.Equal(t, 300000000, GetReleaseSizeLimit())
}

func TestGetLegacyFromConfig(t *testing.T) {
	// Explicit values pass through untouched. The ConcurrencyNum field carries
	// a mapstructure:"cocurrencyNum" tag so the documented config key maps
	// (viper's decoder matches struct tags, not field names).
	writeTmpConfig(t, "githubtoken: test\ncocurrencyNum: 5\nreleaseNumLimit: 7\nreleaseSizeLimit: 1000\n")
	require.Equal(t, uint(5), GetConcurrencyNum())
	require.Equal(t, 7, GetReleaseNumLimit())
	require.Equal(t, 1000, GetReleaseSizeLimit())
}

func TestSaveConcurrencyNumRoundTrip(t *testing.T) {
	// Save() persists the key as "cocurrencyNum" (config.go:136); the
	// mapstructure tag must let that exact key decode back on re-init.
	writeTmpConfig(t, "githubtoken: test\ncocurrencyNum: 6\n")
	require.Equal(t, uint(6), GetConcurrencyNum())

	require.NoError(t, Save())

	Init()
	require.Equal(t, uint(6), GetConcurrencyNum())
}

func TestGetLegacyNegativeMeansNoLimit(t *testing.T) {
	// Negative release limits are meaningful ("no limit") and must NOT be
	// defaulted by Init's zero-only seeding.
	writeTmpConfig(t, "githubtoken: test\nreleaseNumLimit: -1\nreleaseSizeLimit: -1\n")
	require.Equal(t, -1, GetReleaseNumLimit())
	require.Equal(t, -1, GetReleaseSizeLimit())
}

func TestSaveStorageRoundTripKeepsFlatFields(t *testing.T) {
	writeTmpConfig(t, `storage:
  - name: localFile
    type: file
    path: /app/repo
`)
	require.Len(t, GetIns().Storage, 1)
	require.Equal(t, "localFile", GetIns().Storage[0].Name)

	require.NoError(t, Save())
	saved, err := os.ReadFile(Path)
	require.NoError(t, err)
	require.NotContains(t, string(saved), "- storage:")

	Init()
	require.Len(t, GetIns().Storage, 1)
	require.Equal(t, "localFile", GetIns().Storage[0].Name)
	require.Equal(t, "file", GetIns().Storage[0].Type)
	require.Equal(t, "/app/repo", GetIns().Storage[0].Path)
}

func TestValidateIdentity(t *testing.T) {
	// 非 user/org 且无 URL → 校验拒绝。
	err := validateIdentity(&Config{Repository: []typedef.Repository{
		{Name: "orphan", Type: typedef.TypeRepo},
	}})
	require.Error(t, err)

	// user/org 且带 orgName → 通过；合成 URL 即为身份。
	acme := Config{Repository: []typedef.Repository{
		{Name: "acme", Type: typedef.TypeOrg, OrgName: "acme"},
	}}
	require.NoError(t, validateIdentity(&acme))
	require.Equal(t, "https://github.com/acme", acme.Repository[0].EffectiveURL())

	// 空仓库列表 → 通过。
	require.NoError(t, validateIdentity(&Config{}))
}

func TestSetInsPublishesDefensiveImmutableSnapshots(t *testing.T) {
	previous := GetIns()
	t.Cleanup(func() {
		if previous == nil {
			SetIns(&Config{})
			return
		}
		SetIns(previous)
	})

	input := &Config{
		Repository: []typedef.Repository{{
			Name:    "original",
			URL:     "github.com/acme/original",
			Storage: []string{"archive"},
		}},
		Storage: []typedef.MultiStorage{{Storage: typedef.Storage{Name: "archive", Type: "file", Path: "old"}}},
	}
	SetIns(input)
	input.Repository[0].Name = "input mutation"
	input.Repository[0].Storage[0] = "input mutation"
	input.Storage[0].Path = "input mutation"

	callerSnapshot := GetIns()
	callerSnapshot.Repository[0].Name = "caller mutation"
	callerSnapshot.Repository[0].Storage[0] = "caller mutation"
	callerSnapshot.Storage[0].Path = "caller mutation"

	actual := GetIns()
	require.Equal(t, "original", actual.Repository[0].Name)
	require.Equal(t, []string{"archive"}, actual.Repository[0].Storage)
	require.Equal(t, "old", actual.Storage[0].Path)
}

func TestGetInsPreservesUninitializedNil(t *testing.T) {
	previous := GetIns()
	t.Cleanup(func() { SetIns(previous) })
	SetIns(nil)
	require.Nil(t, GetIns())
}

func TestConcurrentConfigPublicationAndSnapshotReads(t *testing.T) {
	previous := GetIns()
	t.Cleanup(func() { SetIns(previous) })
	configs := []*Config{
		{Repository: []typedef.Repository{{Name: "one", URL: "github.com/acme/one"}}, SyncOverdueGrace: time.Minute},
		{Repository: []typedef.Repository{{Name: "two", URL: "github.com/acme/two"}}, SyncOverdueGrace: 2 * time.Minute},
	}
	start := make(chan struct{})
	var workers sync.WaitGroup
	var incoherent atomic.Bool
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		for index := 0; index < 1000; index++ {
			SetIns(configs[index%len(configs)])
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for index := 0; index < 1000; index++ {
			snapshot := GetIns()
			if snapshot == nil || len(snapshot.Repository) == 0 {
				continue
			}
			switch snapshot.Repository[0].Name {
			case "one":
				incoherent.CompareAndSwap(false, snapshot.SyncOverdueGrace != time.Minute)
			case "two":
				incoherent.CompareAndSwap(false, snapshot.SyncOverdueGrace != 2*time.Minute)
			default:
				incoherent.Store(true)
			}
			_ = GetSyncOverdueGrace()
		}
	}()
	close(start)
	workers.Wait()
	require.False(t, incoherent.Load(), "observed partial configuration generation")
}

func TestGetViperReturnsDetachedSnapshot(t *testing.T) {
	writeTmpConfig(t, "server:\n  port: \"8081\"\n")
	detached := GetViper()
	require.NotNil(t, detached)
	detached.Set("server.port", "9999")

	require.Equal(t, "8081", GetServerSection().Port)
	require.Equal(t, "8081", GetViper().GetString("server.port"))
}

func TestGetViperReturnsDeepDetachedCompositeSnapshot(t *testing.T) {
	writeTmpConfig(t, `repository:
  - name: original
    url: github.com/acme/original
    storage:
      - archive
storage:
  - name: archive
    type: file
    path: original-path
`)
	// Save installs typed slices in viper, which is the aliasing path a shallow
	// AllSettings/MergeConfigMap copy fails to detach.
	require.NoError(t, Save())

	detached := GetViper()
	repositories, ok := detached.Get("repository").([]typedef.Repository)
	require.Truef(t, ok, "repository setting has unexpected type %T", detached.Get("repository"))
	storages, ok := detached.Get("storage").([]typedef.MultiStorage)
	require.Truef(t, ok, "storage setting has unexpected type %T", detached.Get("storage"))
	repositories[0].Name = "detached mutation"
	repositories[0].Storage[0] = "detached mutation"
	storages[0].Path = "detached mutation"

	fresh := GetViper()
	freshRepositories, ok := fresh.Get("repository").([]typedef.Repository)
	require.Truef(t, ok, "fresh repository setting has unexpected type %T", fresh.Get("repository"))
	freshStorages, ok := fresh.Get("storage").([]typedef.MultiStorage)
	require.Truef(t, ok, "fresh storage setting has unexpected type %T", fresh.Get("storage"))
	live := GetIns()
	assert.Equal(t, "original", freshRepositories[0].Name)
	assert.Equal(t, []string{"archive"}, freshRepositories[0].Storage)
	assert.Equal(t, "original-path", freshStorages[0].Path)
	assert.Equal(t, "original", live.Repository[0].Name)
	assert.Equal(t, []string{"archive"}, live.Repository[0].Storage)
	assert.Equal(t, "original-path", live.Storage[0].Path)

	require.NoError(t, Save())
	saved, err := os.ReadFile(Path)
	require.NoError(t, err)
	assert.Contains(t, string(saved), "name: original")
	assert.Contains(t, string(saved), "- archive")
	assert.Contains(t, string(saved), "path: original-path")
	assert.NotContains(t, string(saved), "detached mutation")
}

func TestConcurrentSetInsKeepsConfigAndGitHubCoordinatorPaired(t *testing.T) {
	previousConfig := GetIns()
	previousPublish := publishGitHubAPI
	t.Cleanup(func() {
		publishGitHubAPI = previousPublish
		SetIns(previousConfig)
	})

	firstConfigureEntered := make(chan struct{})
	firstConfigureRelease := make(chan struct{})
	var appliedConcurrency atomic.Uint64
	publishGitHubAPI = func(cfg githubapi.Config, install func()) {
		previousPublish(cfg, func() {
			install()
			if cfg.Concurrency == 1 {
				close(firstConfigureEntered)
				<-firstConfigureRelease
			}
		})
		appliedConcurrency.Store(uint64(cfg.Concurrency))
	}

	firstDone := make(chan struct{})
	go func() {
		SetIns(&Config{GitHubToken: "one", GitHubAPIConcurrency: 1})
		close(firstDone)
	}()
	<-firstConfigureEntered
	secondStarted := make(chan struct{})
	secondDone := make(chan struct{})
	go func() {
		close(secondStarted)
		SetIns(&Config{GitHubToken: "two", GitHubAPIConcurrency: 2})
		close(secondDone)
	}()
	<-secondStarted

	lockedAcrossConfigure := !stateMu.TryLock()
	if !lockedAcrossConfigure {
		stateMu.Unlock()
	}
	close(firstConfigureRelease)
	<-firstDone
	<-secondDone

	require.True(t, lockedAcrossConfigure, "config state lock was released before GitHub coordinator publication")
	require.Equal(t, "two", GetIns().GitHubToken)
	require.Equal(t, uint(2), GetIns().GitHubAPIConcurrency)
	require.Equal(t, uint64(2), appliedConcurrency.Load())
}

func TestSetInsPublicationExcludesConfigAndCoordinatorReaders(t *testing.T) {
	previousConfig := GetIns()
	previousPublish := publishGitHubAPI
	SetIns(&Config{GitHubToken: "old", GitHubAPIConcurrency: 1})
	oldPermit, err := githubapi.Acquire(context.Background(), "core")
	require.NoError(t, err)

	publishEntered := make(chan struct{})
	publishRelease := make(chan struct{})
	publishGitHubAPI = func(cfg githubapi.Config, install func()) {
		previousPublish(cfg, func() {
			install()
			if cfg.Concurrency == 2 {
				close(publishEntered)
				<-publishRelease
			}
		})
	}

	publishDone := make(chan struct{})
	go func() {
		SetIns(&Config{GitHubToken: "new", GitHubAPIConcurrency: 2})
		close(publishDone)
	}()
	<-publishEntered

	configReadStarted := make(chan struct{})
	configReadDone := make(chan *Config, 1)
	go func() {
		close(configReadStarted)
		configReadDone <- GetIns()
	}()
	<-configReadStarted

	acquireCtx, cancelAcquire := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelAcquire()
	type acquireResult struct {
		permit githubapi.Permit
		err    error
	}
	acquireStarted := make(chan struct{})
	acquireDone := make(chan acquireResult, 1)
	go func() {
		close(acquireStarted)
		permit, acquireErr := githubapi.Acquire(acquireCtx, "core")
		acquireDone <- acquireResult{permit: permit, err: acquireErr}
	}()
	<-acquireStarted

	configReturnedDuringPublication := false
	var publishedConfig *Config
	select {
	case publishedConfig = <-configReadDone:
		configReturnedDuringPublication = true
	case <-time.After(100 * time.Millisecond):
	}
	close(publishRelease)
	<-publishDone

	if publishedConfig == nil {
		publishedConfig = <-configReadDone
	}
	acquired := <-acquireDone
	oldPermit.Done(githubapi.Observation{})
	if acquired.permit != nil {
		acquired.permit.Done(githubapi.Observation{})
	}
	publishGitHubAPI = previousPublish
	SetIns(previousConfig)

	assert.False(t, configReturnedDuringPublication, "GetIns observed the ins-before-coordinator publication window")
	require.Equal(t, "new", publishedConfig.GitHubToken)
	assert.NoError(t, acquired.err, "Acquire remained queued on the saturated old coordinator")
}
