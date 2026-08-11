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
