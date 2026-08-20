package config

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/typedef"
	"gopkg.in/yaml.v3"
)

func TestDurationStringYAML(t *testing.T) {
	// Marshal produces the Go string form, usable as config.yaml.
	out, err := yaml.Marshal(ExportConfig{RetryBaseDelay: DurationString(5 * time.Second)})
	require.NoError(t, err)
	require.Contains(t, string(out), "retryBaseDelay: 5s")

	// Unmarshal accepts both the string form and integer nanoseconds.
	for _, src := range []string{"retryBaseDelay: 5s", "retryBaseDelay: 5000000000"} {
		var doc ExportConfig
		require.NoError(t, yaml.Unmarshal([]byte(src), &doc))
		require.Equal(t, DurationString(5*time.Second), doc.RetryBaseDelay)
	}

	// A bad duration is rejected.
	var doc ExportConfig
	require.Error(t, yaml.Unmarshal([]byte("retryBaseDelay: 5pigs"), &doc))
}

func TestExportFromRoundTrip(t *testing.T) {
	writeTmpConfig(t, "server:\n  host: 127.0.0.1\n  port: \"8081\"\n")

	cfg := &Config{
		Repository:     []typedef.Repository{{Name: "r1", URL: "github.com/a/b"}},
		Storage:        []typedef.MultiStorage{{Storage: typedef.Storage{Name: "local", Type: "file", Path: "/tmp"}}},
		GitHubToken:    "tok",
		ConcurrencyNum: 3,
		RetryBaseDelay: 5 * time.Second,
	}
	yamlStr, err := ExportFrom(cfg)
	require.NoError(t, err)
	require.Contains(t, yamlStr, "repository:")
	require.Contains(t, yamlStr, "url: github.com/a/b")
	require.Contains(t, yamlStr, "githubToken: tok")
	require.Contains(t, yamlStr, "retryBaseDelay: 5s")
	require.Contains(t, yamlStr, "server:")
	require.Contains(t, yamlStr, "host: 127.0.0.1")

	// Round-trip: the exported YAML parses back with the same globals.
	var doc ExportConfig
	require.NoError(t, yaml.Unmarshal([]byte(yamlStr), &doc))
	require.Equal(t, DurationString(5*time.Second), doc.RetryBaseDelay)
	require.Equal(t, "tok", doc.GitHubToken)
	require.Equal(t, "127.0.0.1", doc.Server.Host)
}

func TestReload(t *testing.T) {
	writeTmpConfig(t, "repository:\n  - name: one\n    url: github.com/one/repo\n")
	require.Equal(t, "one", GetIns().Repository[0].Name)

	// Rewrite the file, reload, and verify the new config is in effect.
	tmp, err := os.CreateTemp(t.TempDir(), "config-*.yaml")
	require.NoError(t, err)
	_, err = tmp.WriteString("repository:\n  - name: two\n    url: github.com/two/repo\n")
	require.NoError(t, err)
	require.NoError(t, tmp.Close())
	Path = tmp.Name()
	require.NoError(t, Reload())
	require.Equal(t, "two", GetIns().Repository[0].Name)
	Path = ""

	// A broken file leaves the previous config untouched and returns an error.
	bad, err := os.CreateTemp(t.TempDir(), "config-*.yaml")
	require.NoError(t, err)
	_, err = bad.WriteString("repository: [unclosed\n")
	require.NoError(t, err)
	require.NoError(t, bad.Close())
	Path = bad.Name()
	require.Error(t, Reload())
	require.Equal(t, "two", GetIns().Repository[0].Name)
	Path = ""
}

func TestReloadRejectsIdentitylessRepo(t *testing.T) {
	writeTmpConfig(t, "repository:\n  - name: one\n    url: github.com/one/repo\n")
	tmp, err := os.CreateTemp(t.TempDir(), "config-*.yaml")
	require.NoError(t, err)
	_, err = tmp.WriteString("repository:\n  - name: orphan\n")
	require.NoError(t, err)
	require.NoError(t, tmp.Close())
	Path = tmp.Name()
	require.Error(t, Reload())
	require.Equal(t, "one", GetIns().Repository[0].Name)
	Path = ""
}

func TestGetServerSectionReadsFromConfigFile(t *testing.T) {
	writeTmpConfig(t, "server:\n  host: 0.0.0.0\n  port: \"8080\"\n  dbPath: /tmp/t.db\n  authEnabled: true\n  authToken: secret\n")
	s := GetServerSection()
	require.Equal(t, "0.0.0.0", s.Host)
	require.Equal(t, "8080", s.Port)
	require.Equal(t, "/tmp/t.db", s.DbPath)
	require.True(t, s.AuthEnabled)
	require.Equal(t, "secret", s.AuthToken)
}

func TestGetServerSectionDefaults(t *testing.T) {
	writeTmpConfig(t, "")
	s := GetServerSection()
	require.Equal(t, "0.0.0.0", s.Host)
	require.Equal(t, "8080", s.Port)
	require.Equal(t, "gitrieve.db", s.DbPath)
	require.False(t, s.AuthEnabled)
	require.Equal(t, "", s.AuthToken)
}
