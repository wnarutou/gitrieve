package config

import (
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
