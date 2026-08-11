package config

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
