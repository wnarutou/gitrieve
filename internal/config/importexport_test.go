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

func TestParseImportFullDoc(t *testing.T) {
	doc, err := ParseImport(`repository:
  - name: acme
    type: org
    orgName: acme
  - name: repo
    url: https://github.com/acme/repo
storage:
  - name: local
    type: file
    path: /tmp/x
githubToken: tok
retryBaseDelay: "5s"
server:
  host: 127.0.0.1
  port: "8081"
`)
	require.NoError(t, err)
	require.Len(t, doc.Repository, 2)
	// org entry gets a synthesized concrete URL so its identity resolves.
	require.Equal(t, "https://github.com/acme", doc.Repository[0].EffectiveURL())
	require.Equal(t, "github.com/acme", doc.Repository[0].Key())
	require.Equal(t, "repo", doc.Repository[1].Name)
	require.Len(t, doc.Storage, 1)
	require.Equal(t, "local", doc.Storage[0].Name)
	require.Equal(t, "tok", doc.GitHubToken)
	require.Equal(t, DurationString(5*time.Second), doc.RetryBaseDelay)
	require.Equal(t, "127.0.0.1", doc.Server.Host)
	require.Equal(t, "8081", doc.Server.Port)
}

func TestParseImportSeedsDefaults(t *testing.T) {
	// Omitted globals behave as configured (same seeding as Init), so an import
	// that omits them diffs as unchanged against a default-seeded current config.
	doc, err := ParseImport("repository:\n  - name: r\n    url: github.com/a/b\n")
	require.NoError(t, err)
	require.Equal(t, 3, doc.RetryMaxCount)
	require.Equal(t, DurationString(5*time.Second), doc.RetryBaseDelay)
	require.Equal(t, uint(3), doc.ConcurrencyNum)

	// Missing/partial server section falls back to defaults so it does not force
	// empty host/port/dbPath onto the config on apply.
	require.Equal(t, "0.0.0.0", doc.Server.Host)
	require.Equal(t, "8080", doc.Server.Port)
	require.Equal(t, "gitrieve.db", doc.Server.DbPath)
	require.False(t, doc.Server.AuthEnabled)
}

func TestParseImportInvalidYAML(t *testing.T) {
	_, err := ParseImport("repository: [unclosed\n")
	require.Error(t, err)
}

func TestValidateImport(t *testing.T) {
	// Identityless repo is rejected.
	doc, err := ParseImport("repository:\n  - name: orphan\n")
	require.NoError(t, err)
	errs := ValidateImport(doc)
	require.Len(t, errs, 1)
	require.Contains(t, errs[0], "orphan")

	// Duplicate normalized URL across entries is rejected. The second URL must
	// carry the full host (https://github.com/X/Y.git) so it normalizes to the
	// same "github.com/x/y" key as the first — a bare https://X/Y.git would
	// normalize to "x/y" (host omitted) and not collide.
	doc, err = ParseImport("repository:\n  - name: a\n    url: github.com/x/y\n  - name: b\n    url: https://github.com/X/Y.git\n")
	require.NoError(t, err)
	errs = ValidateImport(doc)
	require.Len(t, errs, 1)
	require.Contains(t, errs[0], "duplicates")

	// Duplicate storage names are rejected.
	doc, err = ParseImport("storage:\n  - name: local\n    type: file\n    path: /a\n  - name: local\n    type: file\n    path: /b\n")
	require.NoError(t, err)
	errs = ValidateImport(doc)
	require.Len(t, errs, 1)
	require.Contains(t, errs[0], "local")

	// A fully valid doc returns no errors.
	doc, err = ParseImport("repository:\n  - name: r\n    url: github.com/a/b\n")
	require.NoError(t, err)
	require.Empty(t, ValidateImport(doc))
}
