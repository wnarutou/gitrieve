# Config Import/Export Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Config tab to the web UI that exports the full config as YAML, imports (merge) a YAML config with a per-entry diff preview and user choices, and reloads config.yaml from disk at runtime.

**Architecture:** Config package gains server-section, export, reload, and import-parse/validate primitives (new `internal/config/importexport.go`). The server exposes four endpoints (export, import/preview, import, reload) and a two-phase import flow: preview computes a precise diff with zero mutation; apply mutates `a.config` in memory, persists via the existing `config.Save()`, and writes chosen `server:` fields to viper. The executor gains a `RefreshConfig` setter so a reload repoints it without restart. The frontend adds a Config page rendering export/import/operations panels with clickable summary chips, per-row radios, bulk buttons, and a file picker.

**Tech Stack:** Go 1.23, gin-gonic, `gopkg.in/yaml.v3` (already an indirect dep — becomes direct), viper, vanilla JS SPA (no build step), existing `.checkbox`-style CSS.

## Global Constraints

1. **Envelope:** every endpoint returns `{code, data, message}`; `code >= 400` means error and the human-readable text is in `message`.
2. **Export = full config** (repository + storage + globals + `server:` section), secrets **in plaintext**, YAML only, usable directly as `config.yaml`.
3. **`retryBaseDelay` serializes as a string** (`"5s"`); import accepts the string form **or** integer nanoseconds.
4. **Two-phase import**: `POST /api/config/import/preview` (parse+validate+diff, **no mutation**) → `POST /api/config/import` (apply with choices). There is **no `mode` field**.
5. **Choices apply only to `modified`-class entries** (`repository_choices`/`storage_choices`/`global_choices`/`server_choices`); values for added/deleted/unchanged keys are ignored. `repository_deletions`/`storage_deletions` apply **only to `deleted`-class keys**.
6. **Defaults when a choice is absent**: added→imported, modified→imported, deleted→**keep**, globals/server→imported.
7. **Secrets masked in preview diffs**: `githubToken`, `secretAccessKey`, `authToken` shown as `***` (never echoed in plaintext).
8. **`config.Reload()` must never call `ui.ErrorfExit`** (must not kill the server); on any error the previous in-memory config is kept and the error returned.
9. **`server:` section is never hot-applied**; applying it only persists to config.yaml. The UI must warn that server changes (especially `authEnabled`/`authToken`) require a server restart and may lock the user out.
10. **Unchanged items are not shown**; the diff is precise to each repository/storage/field. Summary counts are clickable and preview the corresponding changes.

---

### Task 1: Config package — ServerSection, DurationString, Export, Reload

**Files:**
- Modify: `internal/config/config.go`
- Create: `internal/config/importexport.go`
- Modify: `internal/server/config.go`
- Test: `internal/config/importexport_test.go`

**Interfaces:**
- Consumes: existing `Config` struct, `GetViper()`, package globals `vp`/`ins` (same package).
- Produces (later tasks rely on these):
  - `type ServerSection struct { Host, Port, AuthToken, DbPath string; AuthEnabled bool }` with `yaml:` and `mapstructure:` tags.
  - `type DurationString time.Duration` with `MarshalYAML() (interface{}, error)` and `UnmarshalYAML(*yaml.Node) error`.
  - `type ExportConfig struct` — full config document.
  - `func GetServerSection() ServerSection`
  - `func ExportFrom(cfg *Config) (string, error)`
  - `func Export() (string, error)`
  - `func Reload() error`
  - `server.GetServerConfig() config.ServerSection` (signature change from `*ServerConfig`).

- [ ] **Step 1: Write the failing tests**

Create `internal/config/importexport_test.go` (package `config`, same package as `config_test.go`; reuse its `writeTmpConfig` helper):

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd internal/config && go test -run 'TestDurationString|TestExportFromRoundTrip|TestReload|TestGetServerSection' ./...`
Expected: FAIL — `ServerSection`, `ExportConfig`, `DurationString`, `ExportFrom`, `Reload`, `GetServerSection` undefined.

- [ ] **Step 3: Extract `seedDefaults` in `config.go`**

In `internal/config/config.go`, replace the inline seeding block inside `Init()` (currently lines 40-59) with a call, and add the extracted function:

```go
func Init() {
	vp = viper.New()
	vp.SetConfigFile(Path)
	err := vp.ReadInConfig()
	if err != nil {
		ui.ErrorfExit("Error reading config file, %s", err)
	}
	err = vp.Unmarshal(&ins)
	if err != nil {
		ui.ErrorfExit("Error unmarshalling config file, %s", err)
	}
	seedDefaults(ins)
	// 启动校验：每个仓库条目都必须有可用身份。身份键为空意味着永远无法被
	// 匹配或执行，直接拒绝启动。
	if err := validateIdentity(ins); err != nil {
		ui.ErrorfExit("Invalid configuration: %s", err)
	}
}

// seedDefaults fills zero-valued global options with their defaults. It runs in
// Init and Reload (both single-threaded) so the in-memory config always has the
// same interpretation as the getters; lazy mutation in getters would race under
// the daemon's concurrent workers.
func seedDefaults(cfg *Config) {
	if cfg.RetryMaxCount <= 0 {
		cfg.RetryMaxCount = 3
	}
	if cfg.RetryBaseDelay <= 0 {
		cfg.RetryBaseDelay = 5 * time.Second
	}
	if cfg.ConcurrencyNum == 0 {
		cfg.ConcurrencyNum = 3
	}
	if cfg.ReleaseNumLimit == 0 {
		cfg.ReleaseNumLimit = 3
	}
	if cfg.ReleaseSizeLimit == 0 {
		cfg.ReleaseSizeLimit = 300000000
	}
}
```

- [ ] **Step 4: Create `internal/config/importexport.go`**

```go
package config

import (
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

// ServerSection mirrors the `server:` section of config.yaml. It carries both
// yaml and mapstructure tags so it can be (de)serialized directly and read via
// viper's UnmarshalKey.
type ServerSection struct {
	Host        string `yaml:"host" mapstructure:"host"`
	Port        string `yaml:"port" mapstructure:"port"`
	AuthEnabled bool   `yaml:"authEnabled" mapstructure:"authEnabled"`
	AuthToken   string `yaml:"authToken" mapstructure:"authToken"`
	DbPath      string `yaml:"dbPath" mapstructure:"dbPath"`
}

// DurationString renders a time.Duration as its Go string form ("5s") in YAML
// and accepts either that form or integer nanoseconds on parse. This keeps
// exported config usable as config.yaml (viper parses "5s"); a bare
// time.Duration would serialize as nanoseconds.
type DurationString time.Duration

func (d DurationString) MarshalYAML() (interface{}, error) {
	return time.Duration(d).String(), nil
}

func (d *DurationString) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("retryBaseDelay must be a string or integer")
	}
	if node.Tag == "!!int" {
		n, err := strconv.ParseInt(node.Value, 10, 64)
		if err != nil {
			return err
		}
		*d = DurationString(n)
		return nil
	}
	parsed, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("invalid retryBaseDelay %q: %w", node.Value, err)
	}
	*d = DurationString(parsed)
	return nil
}

// ExportConfig is the full config document used for export/import: the Config
// fields plus the server section (which Config does not model).
type ExportConfig struct {
	Repository       []typedef.Repository   `yaml:"repository"`
	Storage          []typedef.MultiStorage `yaml:"storage"`
	GitHubToken      string                 `yaml:"githubToken"`
	ConcurrencyNum   uint                   `yaml:"cocurrencyNum"`
	ReleaseSizeLimit int                    `yaml:"releaseSizeLimit"`
	ReleaseNumLimit  int                    `yaml:"releaseNumLimit"`
	RetryMaxCount    int                    `yaml:"retryMaxCount"`
	RetryBaseDelay   DurationString         `yaml:"retryBaseDelay"`
	Server           ServerSection          `yaml:"server"`
}

// GetServerSection returns the `server:` section, applying defaults for unset
// fields. It reads from the viper instance that loaded the config file (see
// Init) so host/port/dbPath in config.yaml are honored; if Init has not run yet
// it falls back to the global viper singleton so callers can still override
// settings directly.
func GetServerSection() ServerSection {
	v := GetViper()
	if v == nil {
		v = viper.GetViper()
	}

	// Defaults — set before UnmarshalKey so missing keys fall back to them.
	v.SetDefault("server.port", "8080")
	v.SetDefault("server.host", "0.0.0.0")
	v.SetDefault("server.authEnabled", false)
	v.SetDefault("server.authToken", "")
	v.SetDefault("server.dbPath", "gitrieve.db")

	var cfg ServerSection
	if err := v.UnmarshalKey("server", &cfg); err != nil {
		panic(err)
	}
	return cfg
}

// ExportFrom assembles the full config document (repository + storage +
// globals from cfg, server section from the loaded config file) and returns it
// as YAML usable directly as config.yaml.
func ExportFrom(cfg *Config) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("config not initialized")
	}
	doc := ExportConfig{
		Repository:       cfg.Repository,
		Storage:          cfg.Storage,
		GitHubToken:      cfg.GitHubToken,
		ConcurrencyNum:   cfg.ConcurrencyNum,
		ReleaseSizeLimit: cfg.ReleaseSizeLimit,
		ReleaseNumLimit:  cfg.ReleaseNumLimit,
		RetryMaxCount:    cfg.RetryMaxCount,
		RetryBaseDelay:   DurationString(cfg.RetryBaseDelay),
		Server:           GetServerSection(),
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("failed to marshal config: %w", err)
	}
	return string(out), nil
}

// Export returns the config document for the package-global config instance.
func Export() (string, error) {
	return ExportFrom(GetIns())
}

// Reload re-reads the config file into the package global. Unlike Init it never
// exits the process: on any error the previous in-memory config is kept and the
// error returned (the running server must survive a bad config file).
func Reload() error {
	if vp == nil {
		return fmt.Errorf("config not initialized")
	}
	// Re-point viper at the current Path: Init pinned vp to the path it saw at
	// startup, and Reload must honor a Path changed since (production keeps it
	// constant; tests point it at a fresh file per case).
	vp.SetConfigFile(Path)
	if err := vp.ReadInConfig(); err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}
	var next Config
	if err := vp.Unmarshal(&next); err != nil {
		return fmt.Errorf("failed to unmarshal config file: %w", err)
	}
	seedDefaults(&next)
	if err := validateIdentity(&next); err != nil {
		return err
	}
	ins = &next
	return nil
}
```

- [ ] **Step 5: Delegate `server.GetServerConfig` to the config package**

Replace the entire body of `internal/server/config.go` (the `ServerConfig` type and `GetServerConfig`) with:

```go
package server

import (
	"github.com/wnarutou/gitrieve/internal/config"
)

// GetServerConfig returns the `server:` section of the config file, applying
// defaults for any unset field. Delegated to the config package so export and
// import read the same source of truth.
func GetServerConfig() config.ServerSection {
	return config.GetServerSection()
}
```

This is a return-type change from `*ServerConfig` to `config.ServerSection`; all call sites use field access (`serverCfg.DbPath`, `cfg.Host`, …), which is unchanged.

- [ ] **Step 6: Promote the yaml dependency to direct**

Run: `go mod tidy` (yaml.v3 is already in go.mod as `// indirect`; importing it promotes it to a direct dependency).

- [ ] **Step 7: Run the tests**

Run: `go test ./internal/config/... ./internal/server/...`
Expected: PASS (the existing `internal/server/config_test.go` `TestGetServerConfig*` tests still pass against the value return type).

- [ ] **Step 8: Commit**

```bash
git add internal/config/config.go internal/config/importexport.go internal/config/importexport_test.go internal/server/config.go go.mod go.sum
git commit -m "feat(config): server section, duration string, export & reload primitives"
```

---

### Task 2: Config package — ParseImport + ValidateImport

**Files:**
- Modify: `internal/config/importexport.go`
- Test: `internal/config/importexport_test.go`

**Interfaces:**
- Consumes: Task 1's `ExportConfig`, `DurationString`, `ServerSection`, `typedef.Repository.Key()/EffectiveURL()/GetType()`.
- Produces (later tasks rely on these):
  - `func ParseImport(yamlStr string) (*ExportConfig, error)`
  - `func ValidateImport(doc *ExportConfig) []string`

- [ ] **Step 1: Write the failing tests**

Append to `internal/config/importexport_test.go`:

```go
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

	// Duplicate normalized URL across entries is rejected.
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run 'TestParseImport|TestValidateImport'`
Expected: FAIL — `ParseImport`, `ValidateImport` undefined.

- [ ] **Step 3: Add `seedExportDefaults`, `ParseImport`, `ValidateImport` to `importexport.go`**

Append to `internal/config/importexport.go`:

```go
// seedExportDefaults fills zero-valued global options in an imported document
// with the same defaults Init applies (see seedDefaults), so an import that
// omits them behaves identically to a config file that omits them.
func seedExportDefaults(doc *ExportConfig) {
	if doc.RetryMaxCount <= 0 {
		doc.RetryMaxCount = 3
	}
	if time.Duration(doc.RetryBaseDelay) <= 0 {
		doc.RetryBaseDelay = DurationString(5 * time.Second)
	}
	if doc.ConcurrencyNum == 0 {
		doc.ConcurrencyNum = 3
	}
	if doc.ReleaseNumLimit == 0 {
		doc.ReleaseNumLimit = 3
	}
	if doc.ReleaseSizeLimit == 0 {
		doc.ReleaseSizeLimit = 300000000
	}
	// The server section is never hot-applied, but an absent/partial section
	// must not force empty host/port/dbPath onto the config on apply.
	if doc.Server.Host == "" {
		doc.Server.Host = "0.0.0.0"
	}
	if doc.Server.Port == "" {
		doc.Server.Port = "8080"
	}
	if doc.Server.DbPath == "" {
		doc.Server.DbPath = "gitrieve.db"
	}
}

// ParseImport parses an imported YAML config document into its typed form. It
// seeds defaults for unset global options (mirroring Init), applies server
// defaults for a missing/partial server section, and synthesizes concrete URLs
// for user/org entries so their identity keys resolve. It does not validate
// identities — ValidateImport collects every violation for the caller.
func ParseImport(yamlStr string) (*ExportConfig, error) {
	var doc ExportConfig
	if err := yaml.Unmarshal([]byte(yamlStr), &doc); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	for i := range doc.Repository {
		doc.Repository[i].URL = doc.Repository[i].EffectiveURL()
	}
	seedExportDefaults(&doc)
	return &doc, nil
}

// ValidateImport checks an imported document for the rules that apply before
// any import: every repository needs a usable identity (non-empty URL or
// orgName), no two imported repositories share a normalized URL, and storage
// names are non-empty and unique. Returns every violation so the caller can
// surface them all at once.
func ValidateImport(doc *ExportConfig) []string {
	var errs []string
	seen := map[string]string{}
	for _, repo := range doc.Repository {
		if repo.Key() == "" {
			errs = append(errs, fmt.Sprintf("repository %q (type %q) has an empty URL and no orgName", repo.Name, repo.GetType()))
			continue
		}
		if prev, ok := seen[repo.Key()]; ok {
			errs = append(errs, fmt.Sprintf("repository %q duplicates URL %q of repository %q", repo.Name, repo.Key(), prev))
		} else {
			seen[repo.Key()] = repo.Name
		}
	}
	seenStorage := map[string]bool{}
	for _, st := range doc.Storage {
		if st.Name == "" {
			errs = append(errs, "storage entry has an empty name")
			continue
		}
		if seenStorage[st.Name] {
			errs = append(errs, fmt.Sprintf("storage %q is duplicated", st.Name))
		}
		seenStorage[st.Name] = true
	}
	return errs
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/config/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/importexport.go internal/config/importexport_test.go
git commit -m "feat(config): import parsing & validation primitives"
```

---

### Task 3: Server — export + import/preview endpoints with diff

**Files:**
- Modify: `internal/server/types.go`
- Create: `internal/server/config_api.go`
- Modify: `internal/server/export_test.go`
- Test: `internal/server/config_api_test.go`

**Interfaces:**
- Consumes: Task 1's `config.ExportFrom`, `config.GetServerSection`, `config.ServerSection`; Task 2's `config.ParseImport`, `config.ValidateImport`; `a.config`, `a.executor` (API struct fields); `typedef.Repository`, `typedef.MultiStorage`.
- Produces (later tasks rely on these):
  - Types: `ImportPreviewRequest`, `FieldChange`, `RepoEntry`, `StorageEntry`, `RepoDiff`, `StorageDiff`, `CountSummary`, `ChangedCount`, `ImportSummary`, `ImportPreviewData`, `ImportErrorData`.
  - `func (a *API) ExportConfig(c *gin.Context)`
  - `func (a *API) PreviewImport(c *gin.Context)`
  - `func (s *TestServer) Cfg() *config.Config` (test hook) and `NewConfigTestServer(cfg *config.Config, testDB *db.DB) *TestServer`.

- [ ] **Step 1: Write the failing tests**

Create `internal/server/config_api_test.go` (package `server_test`, same package as `config_test.go` — reuse its `loadTempConfig` helper):

```go
package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/db"
	server "github.com/wnarutou/gitrieve/internal/server"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

func getJSON(t *testing.T, s *server.TestServer, method, path, body string) (int, map[string]interface{}) {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, bytes.NewBufferString(body))
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return w.Code, resp
}

func TestExportConfigYAML(t *testing.T) {
	loadTempConfig(t, "server:\n  host: 127.0.0.1\n  port: \"8081\"\n")
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()
	cfg := &config.Config{
		Repository:     []typedef.Repository{{Name: "test-repo", URL: "github.com/test/repo"}},
		Storage:        []typedef.MultiStorage{{Storage: typedef.Storage{Name: "local", Type: "file", Path: "/tmp"}}},
		GitHubToken:    "tok",
		RetryBaseDelay: 5 * time.Second,
	}
	s := server.NewConfigTestServer(cfg, testDB)

	code, resp := getJSON(t, s, http.MethodGet, "/api/config/export", "")
	require.Equal(t, 200, code)
	data := resp["data"].(map[string]interface{})
	yamlStr := data["yaml"].(string)
	require.Contains(t, yamlStr, "repository:")
	require.Contains(t, yamlStr, "url: github.com/test/repo")
	require.Contains(t, yamlStr, "githubToken: tok")
	require.Contains(t, yamlStr, "retryBaseDelay: 5s")
	require.Contains(t, yamlStr, "server:")
	require.Contains(t, yamlStr, "host: 127.0.0.1")
}

func TestPreviewImportValidationErrors(t *testing.T) {
	loadTempConfig(t, "")
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()
	s := server.NewConfigTestServer(&config.Config{
		Repository: []typedef.Repository{{Name: "test-repo", URL: "github.com/test/repo"}},
	}, testDB)

	// Invalid YAML -> 400.
	code, _ := getJSON(t, s, http.MethodPost, "/api/config/import/preview", `{"config":"repository: [unclosed\n"}`)
	require.Equal(t, 400, code)

	// Identityless repository -> 400 with a list of violations.
	code, resp := getJSON(t, s, http.MethodPost, "/api/config/import/preview", `{"config":"repository:\n  - name: orphan\n"}`)
	require.Equal(t, 400, code)
	errors := resp["data"].(map[string]interface{})["errors"].([]interface{})
	require.Len(t, errors, 1)
	require.Contains(t, errors[0], "orphan")
}

func TestPreviewImportDiff(t *testing.T) {
	loadTempConfig(t, "")
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()
	cfg := &config.Config{
		Repository: []typedef.Repository{
			{Name: "mod-test", URL: "github.com/test/mod", Cron: "@daily"},
			{Name: "del-test", URL: "github.com/test/del"},
		},
		Storage: []typedef.MultiStorage{
			{Storage: typedef.Storage{Name: "st1", Type: "file", Path: "/tmp/one"}},
		},
		// Globals already at the seeded defaults except GitHubToken and
		// RetryBaseDelay, so the globals diff is exactly those two fields.
		GitHubToken:      "tok-existing",
		ConcurrencyNum:   3,
		ReleaseSizeLimit: 300000000,
		ReleaseNumLimit:  3,
		RetryMaxCount:    3,
	}
	s := server.NewConfigTestServer(cfg, testDB)

	importYAML := `repository:
  - name: mod-test
    url: github.com/test/mod
  - name: add-test
    url: github.com/test/added
storage:
  - name: st1
    type: file
    path: /tmp/two
githubToken: tok-import
retryBaseDelay: "5s"
server:
  port: "9090"
`
	body, _ := json.Marshal(map[string]string{"config": importYAML})
	code, resp := getJSON(t, s, http.MethodPost, "/api/config/import/preview", string(body))
	require.Equal(t, 200, code)
	data := resp["data"].(map[string]interface{})

	summary := data["summary"].(map[string]interface{})
	repos := summary["repositories"].(map[string]interface{})
	require.Equal(t, float64(1), repos["added"])
	require.Equal(t, float64(1), repos["deleted"])
	require.Equal(t, float64(1), repos["modified"])
	storages := summary["storages"].(map[string]interface{})
	require.Equal(t, float64(0), storages["added"])
	require.Equal(t, float64(0), storages["deleted"])
	require.Equal(t, float64(1), storages["modified"])
	require.Equal(t, float64(2), summary["globals"].(map[string]interface{})["changed"])
	require.Equal(t, float64(1), summary["server"].(map[string]interface{})["changed"])

	repoDiff := data["repositories"].(map[string]interface{})
	modified := repoDiff["modified"].([]interface{})
	require.Len(t, modified, 1)
	m := modified[0].(map[string]interface{})
	require.Equal(t, "github.com/test/mod", m["key"])
	changes := m["changes"].([]interface{})
	require.Len(t, changes, 1)
	c := changes[0].(map[string]interface{})
	require.Equal(t, "cron", c["field"])
	require.Equal(t, "@daily", c["existing"])
	require.Equal(t, "", c["imported"])

	deleted := repoDiff["deleted"].([]interface{})
	require.Len(t, deleted, 1)
	require.Equal(t, "github.com/test/del", deleted[0].(map[string]interface{})["key"])

	added := repoDiff["added"].([]interface{})
	require.Len(t, added, 1)
	require.Equal(t, "github.com/test/added", added[0].(map[string]interface{})["key"])

	stDiff := data["storages"].(map[string]interface{})
	stModified := stDiff["modified"].([]interface{})
	require.Len(t, stModified, 1)
	sm := stModified[0].(map[string]interface{})
	require.Equal(t, "st1", sm["name"])
	smChanges := sm["changes"].([]interface{})
	require.Len(t, smChanges, 1)
	require.Equal(t, "path", smChanges[0].(map[string]interface{})["field"])

	// githubToken is masked, never echoed in plaintext.
	globals := data["globals"].([]interface{})
	var tokenChange map[string]interface{}
	for _, g := range globals {
		if g.(map[string]interface{})["field"] == "githubToken" {
			tokenChange = g.(map[string]interface{})
		}
	}
	require.NotNil(t, tokenChange)
	require.Equal(t, "***", tokenChange["existing"])
	require.Equal(t, "***", tokenChange["imported"])

	// retryBaseDelay is compared in its string form ("0s" vs "5s"), not as
	// raw nanoseconds.
	var delayChange map[string]interface{}
	for _, g := range globals {
		if g.(map[string]interface{})["field"] == "retryBaseDelay" {
			delayChange = g.(map[string]interface{})
		}
	}
	require.NotNil(t, delayChange)
	require.Equal(t, "0s", delayChange["existing"])
	require.Equal(t, "5s", delayChange["imported"])

	// Server section changed -> warning present.
	warnings := data["warnings"].([]interface{})
	require.Len(t, warnings, 1)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/server/ -run 'TestExportConfig|TestPreviewImport'`
Expected: FAIL — types/handlers undefined.

- [ ] **Step 3: Add the preview/export types to `internal/server/types.go`**

Append to `internal/server/types.go`:

```go
type ImportPreviewRequest struct {
	Config string `json:"config"`
}

type FieldChange struct {
	Field    string      `json:"field"`
	Existing interface{} `json:"existing"`
	Imported interface{} `json:"imported"`
}

type RepoEntry struct {
	Key     string        `json:"key"`
	Name    string        `json:"name"`
	URL     string        `json:"url"`
	Changes []FieldChange `json:"changes,omitempty"`
}

type StorageEntry struct {
	Name    string        `json:"name"`
	Type    string        `json:"type,omitempty"`
	Changes []FieldChange `json:"changes,omitempty"`
}

type RepoDiff struct {
	Added    []RepoEntry `json:"added"`
	Deleted  []RepoEntry `json:"deleted"`
	Modified []RepoEntry `json:"modified"`
}

type StorageDiff struct {
	Added    []StorageEntry `json:"added"`
	Deleted  []StorageEntry `json:"deleted"`
	Modified []StorageEntry `json:"modified"`
}

type CountSummary struct {
	Added    int `json:"added"`
	Deleted  int `json:"deleted"`
	Modified int `json:"modified"`
}

type ChangedCount struct {
	Changed int `json:"changed"`
}

type ImportSummary struct {
	Repositories CountSummary `json:"repositories"`
	Storages     CountSummary `json:"storages"`
	Globals      ChangedCount `json:"globals"`
	Server       ChangedCount `json:"server"`
}

type ImportPreviewData struct {
	Summary      ImportSummary `json:"summary"`
	Repositories RepoDiff      `json:"repositories"`
	Storages     StorageDiff   `json:"storages"`
	Globals      []FieldChange `json:"globals"`
	Server       []FieldChange `json:"server"`
	Warnings     []string      `json:"warnings"`
}

type ImportErrorData struct {
	Errors []string `json:"errors"`
}
```

- [ ] **Step 4: Create `internal/server/config_api.go` with diff helpers + the two handlers**

```go
package server

import (
	"net/http"
	"reflect"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

// secretFields are config values never echoed in plaintext in a diff.
var secretFields = map[string]bool{
	"githubToken":     true,
	"authToken":       true,
	"secretAccessKey": true,
}

// maskSecret returns "***" for a non-empty secret value, the value otherwise.
func maskSecret(field string, v interface{}) interface{} {
	if s, ok := v.(string); ok && secretFields[field] && s != "" {
		return "***"
	}
	return v
}

var repoFields = []struct {
	name string
	get  func(typedef.Repository) interface{}
}{
	{"name", func(r typedef.Repository) interface{} { return r.Name }},
	{"url", func(r typedef.Repository) interface{} { return r.EffectiveURL() }},
	{"cron", func(r typedef.Repository) interface{} { return r.Cron }},
	{"storage", func(r typedef.Repository) interface{} { return r.Storage }},
	{"useCache", func(r typedef.Repository) interface{} { return r.UseCache }},
	{"type", func(r typedef.Repository) interface{} { return r.GetType() }},
	{"orgName", func(r typedef.Repository) interface{} { return r.OrgName }},
	{"allBranches", func(r typedef.Repository) interface{} { return r.AllBranches }},
	{"depth", func(r typedef.Repository) interface{} { return r.Depth }},
	{"downloadReleases", func(r typedef.Repository) interface{} { return r.DownloadReleases }},
	{"downloadIssues", func(r typedef.Repository) interface{} { return r.DownloadIssues }},
	{"downloadWiki", func(r typedef.Repository) interface{} { return r.DownloadWiki }},
	{"downloadDiscussion", func(r typedef.Repository) interface{} { return r.DownloadDiscussion }},
}

var storageFields = []struct {
	name string
	get  func(typedef.MultiStorage) interface{}
}{
	{"name", func(s typedef.MultiStorage) interface{} { return s.Name }},
	{"type", func(s typedef.MultiStorage) interface{} { return s.Type }},
	{"path", func(s typedef.MultiStorage) interface{} { return s.Path }},
	{"endpoint", func(s typedef.MultiStorage) interface{} { return s.Endpoint }},
	{"bucket", func(s typedef.MultiStorage) interface{} { return s.Bucket }},
	{"region", func(s typedef.MultiStorage) interface{} { return s.Region }},
	{"accessKeyID", func(s typedef.MultiStorage) interface{} { return s.AccessKeyID }},
	{"secretAccessKey", func(s typedef.MultiStorage) interface{} { return s.SecretAccessKey }},
}

// stringSetEq compares two string slices as unordered sets (used for the
// storage-backend references, where order is not meaningful).
func stringSetEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]bool, len(a))
	for _, s := range a {
		m[s] = true
	}
	for _, s := range b {
		if !m[s] {
			return false
		}
	}
	return true
}

func valuesEqual(a, b interface{}) bool {
	if as, ok := a.([]string); ok {
		bs, ok2 := b.([]string)
		if !ok2 {
			return false
		}
		return stringSetEq(as, bs)
	}
	return reflect.DeepEqual(a, b)
}

func repoFieldChanges(existing, imported typedef.Repository) []FieldChange {
	var out []FieldChange
	for _, f := range repoFields {
		a, b := f.get(existing), f.get(imported)
		if valuesEqual(a, b) {
			continue
		}
		out = append(out, FieldChange{Field: f.name, Existing: a, Imported: b})
	}
	return out
}

func storageFieldChanges(existing, imported typedef.MultiStorage) []FieldChange {
	var out []FieldChange
	for _, f := range storageFields {
		a, b := f.get(existing), f.get(imported)
		if valuesEqual(a, b) {
			continue
		}
		out = append(out, FieldChange{Field: f.name, Existing: maskSecret(f.name, a), Imported: maskSecret(f.name, b)})
	}
	return out
}

// diffRepositories classifies every imported repository as added / modified /
// absent (identical ones are omitted) and every current repository as deleted.
// Only a subset of a repository's fields drives "modified"; the classification
// is by identity key (normalized URL).
func diffRepositories(current, imported []typedef.Repository) RepoDiff {
	curByKey := map[string]typedef.Repository{}
	for _, r := range current {
		if r.Key() != "" {
			curByKey[r.Key()] = r
		}
	}
	impByKey := map[string]typedef.Repository{}
	for _, r := range imported {
		impByKey[r.Key()] = r
	}

	var diff RepoDiff
	for _, imp := range imported {
		entry := RepoEntry{Key: imp.Key(), Name: imp.Name, URL: imp.EffectiveURL()}
		cur, exists := curByKey[imp.Key()]
		if !exists {
			diff.Added = append(diff.Added, entry)
			continue
		}
		if changes := repoFieldChanges(cur, imp); len(changes) > 0 {
			entry.Changes = changes
			diff.Modified = append(diff.Modified, entry)
		}
	}
	keys := make([]string, 0, len(curByKey))
	for k := range curByKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, ok := impByKey[k]; !ok {
			r := curByKey[k]
			diff.Deleted = append(diff.Deleted, RepoEntry{Key: r.Key(), Name: r.Name, URL: r.EffectiveURL()})
		}
	}
	return diff
}

func diffStorages(current, imported []typedef.MultiStorage) StorageDiff {
	curByName := map[string]typedef.MultiStorage{}
	for _, s := range current {
		curByName[s.Name] = s
	}
	impByName := map[string]typedef.MultiStorage{}
	for _, s := range imported {
		impByName[s.Name] = s
	}

	var diff StorageDiff
	for _, imp := range imported {
		entry := StorageEntry{Name: imp.Name, Type: imp.Type}
		cur, exists := curByName[imp.Name]
		if !exists {
			diff.Added = append(diff.Added, entry)
			continue
		}
		if changes := storageFieldChanges(cur, imp); len(changes) > 0 {
			entry.Changes = changes
			diff.Modified = append(diff.Modified, entry)
		}
	}
	names := make([]string, 0, len(curByName))
	for n := range curByName {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if _, ok := impByName[n]; !ok {
			s := curByName[n]
			diff.Deleted = append(diff.Deleted, StorageEntry{Name: s.Name, Type: s.Type})
		}
	}
	return diff
}

// diffGlobals compares the current config's global options with the imported
// document's, reporting only changed fields.
func diffGlobals(cur *config.Config, imp *config.ExportConfig) []FieldChange {
	pairs := []struct {
		field string
		cur   interface{}
		imp   interface{}
	}{
		{"githubToken", cur.GitHubToken, imp.GitHubToken},
		{"cocurrencyNum", cur.ConcurrencyNum, imp.ConcurrencyNum},
		{"releaseSizeLimit", cur.ReleaseSizeLimit, imp.ReleaseSizeLimit},
		{"releaseNumLimit", cur.ReleaseNumLimit, imp.ReleaseNumLimit},
		{"retryMaxCount", cur.RetryMaxCount, imp.RetryMaxCount},
		{"retryBaseDelay", cur.RetryBaseDelay.String(), time.Duration(imp.RetryBaseDelay).String()},
	}
	var out []FieldChange
	for _, p := range pairs {
		if reflect.DeepEqual(p.cur, p.imp) {
			continue
		}
		out = append(out, FieldChange{Field: p.field, Existing: maskSecret(p.field, p.cur), Imported: maskSecret(p.field, p.imp)})
	}
	return out
}

func diffServer(cur, imp config.ServerSection) []FieldChange {
	pairs := []struct {
		field string
		cur   interface{}
		imp   interface{}
	}{
		{"host", cur.Host, imp.Host},
		{"port", cur.Port, imp.Port},
		{"authEnabled", cur.AuthEnabled, imp.AuthEnabled},
		{"authToken", cur.AuthToken, imp.AuthToken},
		{"dbPath", cur.DbPath, imp.DbPath},
	}
	var out []FieldChange
	for _, p := range pairs {
		if reflect.DeepEqual(p.cur, p.imp) {
			continue
		}
		out = append(out, FieldChange{Field: p.field, Existing: maskSecret(p.field, p.cur), Imported: maskSecret(p.field, p.imp)})
	}
	return out
}

// buildWarnings explains the operational consequences of the changes being
// previewed. The server section is never hot-applied, and changing
// authEnabled/authToken can lock the operator out of the web UI.
func buildWarnings(serverChanges []FieldChange) []string {
	if len(serverChanges) == 0 {
		return nil
	}
	for _, c := range serverChanges {
		if c.Field == "authEnabled" || c.Field == "authToken" {
			return []string{"server.authEnabled / server.authToken 已变更：该改动不会热生效，需重启 server；若忘记令牌，重启后可能无法访问 Web UI。"}
		}
	}
	return []string{"server 段已变更：该改动不会热生效，需重启 server 后生效。"}
}

// ExportConfig returns the full current config as YAML, usable directly as
// config.yaml (secrets in plaintext — the export is the source of truth).
func (a *API) ExportConfig(c *gin.Context) {
	yamlStr, err := config.ExportFrom(a.config)
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{Code: 500, Message: "导出配置失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, Response{Code: 200, Data: gin.H{"yaml": yamlStr}})
}

// PreviewImport parses and validates the submitted config and computes a diff
// against the current config. It never mutates state.
func (a *API) PreviewImport(c *gin.Context) {
	var req ImportPreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, Response{Code: 400, Message: "Invalid request: " + err.Error()})
		return
	}
	doc, err := config.ParseImport(req.Config)
	if err != nil {
		c.JSON(http.StatusBadRequest, Response{Code: 400, Message: err.Error()})
		return
	}
	if errs := config.ValidateImport(doc); len(errs) > 0 {
		c.JSON(http.StatusBadRequest, Response{Code: 400, Message: "导入配置无效", Data: ImportErrorData{Errors: errs}})
		return
	}

	curServer := config.GetServerSection()
	repos := diffRepositories(a.config.Repository, doc.Repository)
	storages := diffStorages(a.config.Storage, doc.Storage)
	globals := diffGlobals(a.config, doc)
	serverChanges := diffServer(curServer, doc.Server)

	data := ImportPreviewData{
		Summary: ImportSummary{
			Repositories: CountSummary{Added: len(repos.Added), Deleted: len(repos.Deleted), Modified: len(repos.Modified)},
			Storages:     CountSummary{Added: len(storages.Added), Deleted: len(storages.Deleted), Modified: len(storages.Modified)},
			Globals:      ChangedCount{Changed: len(globals)},
			Server:       ChangedCount{Changed: len(serverChanges)},
		},
		Repositories: repos,
		Storages:     storages,
		Globals:      globals,
		Server:       serverChanges,
		Warnings:     buildWarnings(serverChanges),
	}
	c.JSON(http.StatusOK, Response{Code: 200, Data: data})
}
```

- [ ] **Step 5: Add the `api` field to `TestServer` + `Cfg()` + `NewConfigTestServer`**

In `internal/server/export_test.go`, change the `TestServer` struct to hold the API and add the test hook and config-route constructor:

```go
type TestServer struct {
	router *gin.Engine
	api    *API
}

// Cfg returns the config instance the API currently holds. Used by the
// config-reload tests to observe the in-memory config after a reload.
func (s *TestServer) Cfg() *config.Config {
	if s.api == nil {
		return nil
	}
	return s.api.config
}

// NewConfigTestServer creates a test server with the config export + preview
// routes registered, using a fresh executor backed by testDB. Task 4 adds the
// apply + reload routes to the same constructor.
func NewConfigTestServer(cfg *config.Config, testDB *db.DB) *TestServer {
	log := logger.NewLogger(testDB)
	exec := executor.NewExecutor(log, testDB, cfg)
	api := NewAPI(cfg, testDB, exec)
	s := &TestServer{router: gin.Default(), api: api}
	s.router.GET("/api/config/export", api.ExportConfig)
	s.router.POST("/api/config/import/preview", api.PreviewImport)
	return s
}
```

(The existing constructors `NewTestServer` / `NewRepoTestServer` / `NewStorageTestServer` leave `api` nil — `Cfg()` handles that.)

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/server/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/server/types.go internal/server/config_api.go internal/server/export_test.go internal/server/config_api_test.go
git commit -m "feat(server): config export + import preview with per-entry diff"
```

---

### Task 4: Server — apply + reload endpoints, executor setter, routes

**Files:**
- Modify: `internal/server/types.go`
- Modify: `internal/server/config_api.go`
- Modify: `internal/config/importexport.go` (add `SetServerField`)
- Modify: `internal/executor/executor.go`
- Modify: `cmd/server/server.go`
- Modify: `internal/server/export_test.go`
- Test: `internal/server/config_api_test.go`, `internal/executor/executor_test.go`

**Interfaces:**
- Consumes: Task 1-3 types/helpers (`config.ParseImport`, `config.ValidateImport`, `config.GetServerSection`, `config.ExportConfig`, `diffRepositories`, `diffStorages`, `a.config`, `a.executor`).
- Produces:
  - `type ImportRequest`, `type ImportResult`
  - `func (a *API) ApplyImport(c *gin.Context)`, `func (a *API) applyImport(doc *config.ExportConfig, req *ImportRequest) ImportResult`, `func (a *API) ReloadConfig(c *gin.Context)`
  - `func SetServerField(field string, value interface{}) error` (config package)
  - `func (e *Executor) RefreshConfig(cfg *config.Config)`

- [ ] **Step 1: Write the failing tests**

Append to `internal/server/config_api_test.go`:

```go
func TestApplyImportDefaultChoices(t *testing.T) {
	loadTempConfig(t, "")
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()
	cfg := &config.Config{
		Repository: []typedef.Repository{
			{Name: "mod-test", URL: "github.com/test/mod", Cron: "@daily"},
			{Name: "del-test", URL: "github.com/test/del"},
		},
		Storage: []typedef.MultiStorage{
			{Storage: typedef.Storage{Name: "st1", Type: "file", Path: "/tmp/one"}},
		},
	}
	s := server.NewConfigTestServer(cfg, testDB)

	importYAML := `repository:
  - name: mod-test
    url: github.com/test/mod
  - name: add-test
    url: github.com/test/added
storage:
  - name: st1
    type: file
    path: /tmp/two
`
	body, _ := json.Marshal(map[string]string{"config": importYAML})
	code, resp := getJSON(t, s, http.MethodPost, "/api/config/import", string(body))
	require.Equal(t, 200, code)
	data := resp["data"].(map[string]interface{})
	require.Equal(t, float64(1), data["repositories_added"])
	require.Equal(t, float64(1), data["repositories_updated"])
	require.Equal(t, float64(0), data["repositories_deleted"])
	require.Equal(t, float64(0), data["storages_added"])
	require.Equal(t, float64(1), data["storages_updated"])
	require.Equal(t, float64(0), data["storages_deleted"])

	// Defaults: added+modified applied, deleted kept.
	repos := s.Cfg().Repository
	require.Len(t, repos, 3)
	byName := map[string]typedef.Repository{}
	for _, r := range repos {
		byName[r.Name] = r
	}
	require.Equal(t, "", byName["mod-test"].Cron) // modified -> imported wins
	_, delKept := byName["del-test"]
	require.True(t, delKept) // deleted -> kept
	_, addApplied := byName["add-test"]
	require.True(t, addApplied) // added -> imported
	require.Equal(t, "/tmp/two", s.Cfg().Storage[0].Path)
}

func TestApplyImportWithChoices(t *testing.T) {
	loadTempConfig(t, "")
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()
	cfg := &config.Config{
		Repository: []typedef.Repository{
			{Name: "mod-test", URL: "github.com/test/mod", Cron: "@daily"},
			{Name: "del-test", URL: "github.com/test/del"},
		},
		Storage: []typedef.MultiStorage{
			{Storage: typedef.Storage{Name: "st1", Type: "file", Path: "/tmp/one"}},
		},
	}
	s := server.NewConfigTestServer(cfg, testDB)

	importYAML := `repository:
  - name: mod-test
    url: github.com/test/mod
  - name: add-test
    url: github.com/test/added
storage:
  - name: st1
    type: file
    path: /tmp/two
`
	payload := map[string]interface{}{
		"config": importYAML,
		"choices": map[string]interface{}{
			"repository_choices":   map[string]string{"github.com/test/mod": "existing"},
			"repository_deletions": []string{"github.com/test/del"},
			"storage_choices":      map[string]string{"st1": "existing"},
		},
	}
	body, _ := json.Marshal(payload)
	code, resp := getJSON(t, s, http.MethodPost, "/api/config/import", string(body))
	require.Equal(t, 200, code)
	data := resp["data"].(map[string]interface{})
	require.Equal(t, float64(1), data["repositories_added"])
	require.Equal(t, float64(0), data["repositories_updated"])
	require.Equal(t, float64(1), data["repositories_deleted"])
	require.Equal(t, float64(0), data["storages_updated"])

	repos := s.Cfg().Repository
	require.Len(t, repos, 2)
	byName := map[string]typedef.Repository{}
	for _, r := range repos {
		byName[r.Name] = r
	}
	require.Equal(t, "@daily", byName["mod-test"].Cron) // choice "existing" wins
	_, delGone := byName["del-test"]
	require.False(t, delGone)                       // deletion applied
	_, addApplied := byName["add-test"]
	require.True(t, addApplied)                     // added always imported
	require.Equal(t, "/tmp/one", s.Cfg().Storage[0].Path) // choice "existing" wins
}

func TestReloadConfig(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	writeFile(t, path, "repository:\n  - name: one\n    url: github.com/one/repo\n")
	config.Path = path
	config.Init()

	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()
	s := server.NewConfigTestServer(config.GetIns(), testDB)
	require.Equal(t, "one", s.Cfg().Repository[0].Name)

	writeFile(t, path, "repository:\n  - name: two\n    url: github.com/two/repo\n")
	code, _ := getJSON(t, s, http.MethodPost, "/api/config/reload", "")
	require.Equal(t, 200, code)
	require.Equal(t, "two", s.Cfg().Repository[0].Name)
}

func TestReloadConfigKeepsOldOnError(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	writeFile(t, path, "repository:\n  - name: one\n    url: github.com/one/repo\n")
	config.Path = path
	config.Init()

	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()
	s := server.NewConfigTestServer(config.GetIns(), testDB)

	writeFile(t, path, "repository: [broken\n")
	code, resp := getJSON(t, s, http.MethodPost, "/api/config/reload", "")
	require.Equal(t, 400, code)
	require.Contains(t, resp["message"].(string), "重载配置失败")
	require.Equal(t, "one", s.Cfg().Repository[0].Name)
}
```

Add a tiny helper at the top of `config_api_test.go`:

```go
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}
```

Add `"os"` to the test file imports.

Append to `internal/executor/executor_test.go`:

```go
func TestRefreshConfigRepointsExecutor(t *testing.T) {
	exec, _ := newTestExecutor(t)

	exec.RefreshConfig(&config.Config{Repository: []typedef.Repository{
		{Name: "other", URL: "github.com/other/repo"},
	}})

	// The old key no longer resolves against the executor's config.
	_, err := exec.ExecuteJob("github.com/test/repo")
	require.Error(t, err)

	// The new key resolves.
	jobIDs, err := exec.ExecuteJob("github.com/other/repo")
	require.NoError(t, err)
	require.Len(t, jobIDs, 1)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/server/ -run 'TestApplyImport|TestReloadConfig' ./internal/executor/ -run TestRefreshConfig`
Expected: FAIL — `ApplyImport`, `ReloadConfig`, `RefreshConfig` undefined.

- [ ] **Step 3: Add the apply types to `internal/server/types.go`**

Append:

```go
type ImportRequest struct {
	Config              string            `json:"config"`
	RepositoryDeletions []string          `json:"repository_deletions"`
	RepositoryChoices   map[string]string `json:"repository_choices"`
	StorageDeletions    []string          `json:"storage_deletions"`
	StorageChoices      map[string]string `json:"storage_choices"`
	GlobalChoices       map[string]string `json:"global_choices"`
	ServerChoices       map[string]string `json:"server_choices"`
}

type ImportResult struct {
	RepositoriesAdded    int `json:"repositories_added"`
	RepositoriesUpdated  int `json:"repositories_updated"`
	RepositoriesDeleted  int `json:"repositories_deleted"`
	StoragesAdded        int `json:"storages_added"`
	StoragesUpdated      int `json:"storages_updated"`
	StoragesDeleted      int `json:"storages_deleted"`
	GlobalsUpdated       int `json:"globals_updated"`
	ServerUpdated        int `json:"server_updated"`
}
```

- [ ] **Step 4: Add `SetServerField` to `internal/config/importexport.go`**

Append:

```go
// SetServerField persists one `server:` field to the loaded viper instance so
// an imported server section is written to config.yaml. The running server
// never re-reads the server section, so this only takes effect after a restart.
// Returns an error when the config was never initialized.
func SetServerField(field string, value interface{}) error {
	if vp == nil {
		return fmt.Errorf("config not initialized")
	}
	vp.Set("server."+field, value)
	return nil
}
```

- [ ] **Step 5: Add `RefreshConfig` to `internal/executor/executor.go`**

Add directly after `NewExecutor`:

```go
// RefreshConfig repoints the executor at a new config instance. Called by the
// server's config-reload endpoint after the config file is re-read; the
// executor reads e.cfg dynamically on every ExecuteJob, so this is safe to
// call while jobs are running.
func (e *Executor) RefreshConfig(cfg *config.Config) {
	e.cfg = cfg
}
```

- [ ] **Step 6: Add `ApplyImport`, `applyImport`, `ReloadConfig` to `internal/server/config_api.go`**

Append to `internal/server/config_api.go`:

```go
// ApplyImport applies a previously-previewed import. Choices select, per entry,
// whether the imported or the existing value wins; entries without a choice use
// the documented defaults (added/modified/globals/server -> imported, deleted ->
// keep). Mutates a.config in memory, then persists via config.Save(); the
// server section is written to viper (never hot-applied).
func (a *API) ApplyImport(c *gin.Context) {
	var req ImportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, Response{Code: 400, Message: "Invalid request: " + err.Error()})
		return
	}
	doc, err := config.ParseImport(req.Config)
	if err != nil {
		c.JSON(http.StatusBadRequest, Response{Code: 400, Message: err.Error()})
		return
	}
	if errs := config.ValidateImport(doc); len(errs) > 0 {
		c.JSON(http.StatusBadRequest, Response{Code: 400, Message: "导入配置无效", Data: ImportErrorData{Errors: errs}})
		return
	}

	result := a.applyImport(doc, &req)

	msg := ""
	if err := config.Save(); err != nil {
		msg = "配置已应用（内存）但未能持久化: " + err.Error()
	}
	c.JSON(http.StatusOK, Response{Code: 200, Data: result, Message: msg})
}

func (a *API) applyImport(doc *config.ExportConfig, req *ImportRequest) ImportResult {
	var result ImportResult
	choice := func(m map[string]string, key string, def string) string {
		if v, ok := m[key]; ok {
			return v
		}
		return def
	}

	// Recompute the classification so choices apply only to the classes the
	// user actually saw in the preview (modified / deleted).
	repoDiff := diffRepositories(a.config.Repository, doc.Repository)
	modifiedRepos := map[string]bool{}
	deletedRepos := map[string]bool{}
	for _, e := range repoDiff.Modified {
		modifiedRepos[e.Key] = true
	}
	for _, e := range repoDiff.Deleted {
		deletedRepos[e.Key] = true
	}
	stDiff := diffStorages(a.config.Storage, doc.Storage)
	modifiedStorages := map[string]bool{}
	deletedStorages := map[string]bool{}
	for _, e := range stDiff.Modified {
		modifiedStorages[e.Name] = true
	}
	for _, e := range stDiff.Deleted {
		deletedStorages[e.Name] = true
	}

	deleteRepo := map[string]bool{}
	for _, k := range req.RepositoryDeletions {
		deleteRepo[k] = true
	}
	deleteStorage := map[string]bool{}
	for _, n := range req.StorageDeletions {
		deleteStorage[n] = true
	}

	// ---- repositories: drop deleted, then merge imported ----
	kept := make([]typedef.Repository, 0, len(a.config.Repository))
	for _, r := range a.config.Repository {
		if deletedRepos[r.Key()] && deleteRepo[r.Key()] {
			result.RepositoriesDeleted++
			continue
		}
		kept = append(kept, r)
	}
	idxByKey := map[string]int{}
	for i, r := range kept {
		if r.Key() != "" {
			idxByKey[r.Key()] = i
		}
	}
	for _, imp := range doc.Repository {
		imp.URL = imp.EffectiveURL() // 用户/组织条目以合成 URL 落库，保证身份
		i, ok := idxByKey[imp.Key()]
		if !ok {
			kept = append(kept, imp)
			idxByKey[imp.Key()] = len(kept) - 1
			result.RepositoriesAdded++
			continue
		}
		if !modifiedRepos[imp.Key()] {
			continue // unchanged: existing wins silently
		}
		if choice(req.RepositoryChoices, imp.Key(), "imported") == "imported" {
			kept[i] = imp
			result.RepositoriesUpdated++
		}
	}
	a.config.Repository = kept

	// ---- storages: same pattern by name ----
	keptSt := make([]typedef.MultiStorage, 0, len(a.config.Storage))
	for _, st := range a.config.Storage {
		if deletedStorages[st.Name] && deleteStorage[st.Name] {
			result.StoragesDeleted++
			continue
		}
		keptSt = append(keptSt, st)
	}
	idxByName := map[string]int{}
	for i, st := range keptSt {
		idxByName[st.Name] = i
	}
	for _, imp := range doc.Storage {
		i, ok := idxByName[imp.Name]
		if !ok {
			keptSt = append(keptSt, imp)
			idxByName[imp.Name] = len(keptSt) - 1
			result.StoragesAdded++
			continue
		}
		if !modifiedStorages[imp.Name] {
			continue
		}
		if choice(req.StorageChoices, imp.Name, "imported") == "imported" {
			keptSt[i] = imp
			result.StoragesUpdated++
		}
	}
	a.config.Storage = keptSt

	// ---- globals ----
	if choice(req.GlobalChoices, "githubToken", "imported") == "imported" && a.config.GitHubToken != doc.GitHubToken {
		a.config.GitHubToken = doc.GitHubToken
		result.GlobalsUpdated++
	}
	if choice(req.GlobalChoices, "cocurrencyNum", "imported") == "imported" && a.config.ConcurrencyNum != doc.ConcurrencyNum {
		a.config.ConcurrencyNum = doc.ConcurrencyNum
		result.GlobalsUpdated++
	}
	if choice(req.GlobalChoices, "releaseSizeLimit", "imported") == "imported" && a.config.ReleaseSizeLimit != doc.ReleaseSizeLimit {
		a.config.ReleaseSizeLimit = doc.ReleaseSizeLimit
		result.GlobalsUpdated++
	}
	if choice(req.GlobalChoices, "releaseNumLimit", "imported") == "imported" && a.config.ReleaseNumLimit != doc.ReleaseNumLimit {
		a.config.ReleaseNumLimit = doc.ReleaseNumLimit
		result.GlobalsUpdated++
	}
	if choice(req.GlobalChoices, "retryMaxCount", "imported") == "imported" && a.config.RetryMaxCount != doc.RetryMaxCount {
		a.config.RetryMaxCount = doc.RetryMaxCount
		result.GlobalsUpdated++
	}
	if choice(req.GlobalChoices, "retryBaseDelay", "imported") == "imported" {
		d := time.Duration(doc.RetryBaseDelay)
		if a.config.RetryBaseDelay != d {
			a.config.RetryBaseDelay = d
			result.GlobalsUpdated++
		}
	}

	// ---- server section: persist chosen fields; never hot-applied ----
	curServer := config.GetServerSection()
	if choice(req.ServerChoices, "host", "imported") == "imported" && curServer.Host != doc.Server.Host {
		_ = config.SetServerField("host", doc.Server.Host)
		result.ServerUpdated++
	}
	if choice(req.ServerChoices, "port", "imported") == "imported" && curServer.Port != doc.Server.Port {
		_ = config.SetServerField("port", doc.Server.Port)
		result.ServerUpdated++
	}
	if choice(req.ServerChoices, "authEnabled", "imported") == "imported" && curServer.AuthEnabled != doc.Server.AuthEnabled {
		_ = config.SetServerField("authEnabled", doc.Server.AuthEnabled)
		result.ServerUpdated++
	}
	if choice(req.ServerChoices, "authToken", "imported") == "imported" && curServer.AuthToken != doc.Server.AuthToken {
		_ = config.SetServerField("authToken", doc.Server.AuthToken)
		result.ServerUpdated++
	}
	if choice(req.ServerChoices, "dbPath", "imported") == "imported" && curServer.DbPath != doc.Server.DbPath {
		_ = config.SetServerField("dbPath", doc.Server.DbPath)
		result.ServerUpdated++
	}

	return result
}

// ReloadConfig re-reads config.yaml from disk and repoints the API and executor
// at the fresh config instance. The `server:` section is NOT hot-applied (a
// restart is required); daemon cron schedules are also not rescheduled.
func (a *API) ReloadConfig(c *gin.Context) {
	if err := config.Reload(); err != nil {
		c.JSON(http.StatusBadRequest, Response{Code: 400, Message: "重载配置失败: " + err.Error()})
		return
	}
	a.config = config.GetIns()
	if a.executor != nil {
		a.executor.RefreshConfig(config.GetIns())
	}
	c.JSON(http.StatusOK, Response{Code: 200, Data: gin.H{}})
}
```

- [ ] **Step 7: Register the two new routes in `NewConfigTestServer` and the 4 routes in the real server**

In `internal/server/export_test.go`, extend `NewConfigTestServer` (add these two lines after the preview route):

```go
	s.router.POST("/api/config/import", api.ApplyImport)
	s.router.POST("/api/config/reload", api.ReloadConfig)
```

In `cmd/server/server.go`, inside `setupRoutes`, after the storage routes (line ~108):

```go
	apiGroup.GET("/api/config/export", api.ExportConfig)
	apiGroup.POST("/api/config/import/preview", api.PreviewImport)
	apiGroup.POST("/api/config/import", api.ApplyImport)
	apiGroup.POST("/api/config/reload", api.ReloadConfig)
```

- [ ] **Step 8: Document the Config endpoints in `docs/api.md`**

Insert a new `## Config` section after the `## System` section (before the final "### Web UI and static assets" paragraph near the end of the file):

```markdown
## Config

### Export config

Returns the full current configuration as YAML — `repository`, `storage`, all
global options, and the `server:` section — formatted so the output can be used
directly as `config.yaml`. Secrets (`githubToken`, `secretAccessKey`,
`authToken`) are exported in plaintext; treat the file accordingly.

| Method | Path | Description |
|---|---|---|
| GET | `/api/config/export` | Export the full config as YAML |

**Response**

```json
{ "code": 200, "data": { "yaml": "repository:\n  - name: …" }, "message": "" }
```

### Preview an import

Parses, validates, and diffs a submitted YAML config against the current one.
Does **not** mutate any state. The diff is precise per repository / storage /
field: entries that are unchanged are omitted. Secrets in the diff are masked
as `***`.

| Method | Path | Description |
|---|---|---|
| POST | `/api/config/import/preview` | Validate and diff an imported config |

**Request body**

```json
{ "config": "repository:\n  - name: …\n" }
```

**Response** (`200`)

```json
{
  "code": 200,
  "data": {
    "summary": {
      "repositories": { "added": 1, "deleted": 1, "modified": 2 },
      "storages":     { "added": 0, "deleted": 0, "modified": 1 },
      "globals":      { "changed": 2 },
      "server":       { "changed": 1 }
    },
    "repositories": {
      "added":    [ { "key": "github.com/x/y", "name": "y", "url": "github.com/x/y" } ],
      "deleted":  [ { "key": "github.com/a/b", "name": "b", "url": "github.com/a/b" } ],
      "modified": [ { "key": "github.com/m/n", "name": "n", "url": "github.com/m/n",
                      "changes": [ { "field": "cron", "existing": "@daily", "imported": "" } ] } ]
    },
    "storages": {
      "added": [], "deleted": [],
      "modified": [ { "name": "local", "type": "file",
                      "changes": [ { "field": "path", "existing": "/tmp/one", "imported": "/tmp/two" } ] } ]
    },
    "globals": [ { "field": "githubToken", "existing": "***", "imported": "***" } ],
    "server":  [ { "field": "port", "existing": "8080", "imported": "9090" } ],
    "warnings": ["server 段已变更：该改动不会热生效，需重启 server 后生效。"]
  },
  "message": ""
}
```

**Errors** (`400`): invalid YAML or validation violations. Validation errors
carry the full list:

```json
{ "code": 400, "data": { "errors": ["repository \"orphan\" …"] }, "message": "导入配置无效" }
```

### Apply an import

Applies a previously-previewed import. `choices` select, per entry, whether the
imported or the existing value wins; entries without a choice use the defaults
(added/modified/globals/server → imported, deleted → keep). Choices for
entries the diff did not classify as changed/modified are ignored.
`repository_deletions` / `storage_deletions` only take effect for entries the
diff classified as deleted.

The `server:` section is **never hot-applied** — accepted changes are written to
`config.yaml` and take effect on the next server restart. A failure to persist
returns `200` with a `message` noting the change is in memory only.

| Method | Path | Description |
|---|---|---|
| POST | `/api/config/import` | Apply an import with per-entry choices |

**Request body**

```json
{
  "config": "repository:\n  - name: …\n",
  "choices": {
    "repository_deletions": ["github.com/a/b"],
    "repository_choices":   { "github.com/m/n": "imported" },
    "storage_deletions":    [],
    "storage_choices":      { "local": "existing" },
    "global_choices":       { "githubToken": "imported" },
    "server_choices":       { "port": "existing" }
  }
}
```

**Response** (`200`)

```json
{ "code": 200,
  "data": {
    "repositories_added": 1, "repositories_updated": 1, "repositories_deleted": 1,
    "storages_added": 0,     "storages_updated": 0,     "storages_deleted": 0,
    "globals_updated": 1,    "server_updated": 0
  },
  "message": "" }
```

### Reload config from disk

Re-reads `config.yaml` from disk and repoints the running server (API and
executor) at the fresh config. The `server:` section is not hot-applied (a
restart is required) and daemon cron schedules are not rescheduled. On any
error the previous config stays active and a `400` is returned.

| Method | Path | Description |
|---|---|---|
| POST | `/api/config/reload` | Reload config.yaml at runtime |

**Response** (`200`)

```json
{ "code": 200, "data": {}, "message": "" }
```

---

```

- [ ] **Step 9: Run the tests**

Run: `go test ./internal/server/... ./internal/executor/... ./internal/config/...`
Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add internal/server/types.go internal/server/config_api.go internal/server/export_test.go internal/server/config_api_test.go internal/config/importexport.go internal/executor/executor.go internal/executor/executor_test.go cmd/server/server.go docs/api.md
git commit -m "feat(server): config apply/reload endpoints, executor refresh, route wiring"
```

---

### Task 5: Web UI — Config tab

**Files:**
- Modify: `web/templates/index.html`
- Modify: `web/static/js/main.js`
- Modify: `web/static/css/main.css`
- Verify: manual browser check (no JS test framework exists in this project)

**Interfaces:**
- Consumes: the four endpoints (`GET /api/config/export`, `POST /api/config/import/preview`, `POST /api/config/import`, `POST /api/config/reload`) and the preview/apply JSON shapes from Tasks 3-4. `api()`, `$`, `$$`, `esc`, `toast`, `state`, `renderApp` (existing helpers in `main.js`).

- [ ] **Step 1: Add the Config nav link**

In `web/templates/index.html`, after the Storage link (line 15):

```html
            <a href="#/config" data-route="config">Config</a>
```

- [ ] **Step 2: Add the route + helper markup in `main.js`**

In `renderApp` (line 96-98), add the route:

```js
    else if (route === 'config') renderConfig();
```

Extend the `api()` error path (line 42-44) so preview validation errors (a `data.errors` array) surface on the thrown error:

```js
    if (!resp.ok || (body && typeof body.code === 'number' && body.code >= 400)) {
        const err = new Error((body && body.message) || ('HTTP ' + resp.status));
        if (body && body.data && Array.isArray(body.data.errors)) err.errors = body.data.errors;
        throw err;
    }
```

- [ ] **Step 3: Add the Config page rendering + handlers**

Append to `web/static/js/main.js` (after `renderStorage`, before the `DOMContentLoaded` block). Note: `repoKey(r)` is `r.URL || ''`, and the preview entries use `key`/`name`/`url` per the API:

```js
/* ---------------- Config: export / import / reload ---------------- */

async function renderConfig() {
    $('#app').innerHTML = `
        <div class="page-header"><h2>Configuration</h2></div>
        <div class="panel"><div class="panel-pad">
            <h3>导出配置</h3>
            <textarea id="config-export" class="config-textarea" readonly placeholder="点击“刷新”加载当前配置…"></textarea>
            <div class="toolbar-group" style="margin-top:8px;">
                <button id="btn-config-copy" class="btn btn-sm">复制</button>
                <button id="btn-config-download" class="btn btn-sm">下载 config.yaml</button>
                <button id="btn-config-export-refresh" class="btn btn-sm">刷新</button>
            </div>
        </div></div>
        <div class="panel"><div class="panel-pad">
            <h3>导入配置（合并）</h3>
            <textarea id="config-import" class="config-textarea" placeholder="粘贴 config.yaml 内容，或选择文件…"></textarea>
            <div class="toolbar-group" style="margin-top:8px;">
                <button id="btn-config-file" class="btn btn-sm">选择文件</button>
                <input type="file" id="config-file" accept=".yaml,.yml,.txt" hidden>
                <button id="btn-config-preview" class="btn btn-sm btn-primary">预览差异</button>
            </div>
            <div id="config-preview"></div>
        </div></div>
        <div class="panel"><div class="panel-pad">
            <h3>配置操作</h3>
            <button id="btn-config-reload" class="btn">刷新配置（从磁盘重载 config.yaml）</button>
            <p class="muted" style="margin-top:8px;">server 段（host/port/authEnabled/dbPath）改动需重启 server 生效；daemon 排程需重启 daemon。修改 authEnabled/authToken 后若忘记令牌，可能无法访问本界面。</p>
        </div></div>`;

    loadExport();
    $('#btn-config-copy').addEventListener('click', copyExport);
    $('#btn-config-download').addEventListener('click', downloadExport);
    $('#btn-config-export-refresh').addEventListener('click', loadExport);
    $('#btn-config-file').addEventListener('click', () => $('#config-file').click());
    $('#config-file').addEventListener('change', (ev) => {
        const f = ev.target.files[0];
        if (!f) return;
        const reader = new FileReader();
        reader.onload = () => { $('#config-import').value = reader.result; };
        reader.readAsText(f);
    });
    $('#btn-config-preview').addEventListener('click', previewImport);
    $('#btn-config-reload').addEventListener('click', reloadConfig);
}

async function loadExport() {
    try {
        const data = await api('/api/config/export');
        $('#config-export').value = (data && data.yaml) || '';
    } catch (e) {
        toast('导出配置失败: ' + e.message, true);
    }
}

function copyExport() {
    const text = $('#config-export').value;
    if (!text) return;
    navigator.clipboard.writeText(text).then(
        () => toast('配置已复制'),
        () => toast('复制失败', true));
}

function downloadExport() {
    const text = $('#config-export').value;
    if (!text) return;
    const blob = new Blob([text], { type: 'text/yaml' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = 'config.yaml';
    a.click();
    URL.revokeObjectURL(a.href);
}

async function previewImport() {
    const config = $('#config-import').value.trim();
    if (!config) { toast('请粘贴或选择配置内容', true); return; }
    try {
        const data = await api('/api/config/import/preview', { method: 'POST', body: JSON.stringify({ config }) });
        renderPreview(data);
    } catch (e) {
        if (e.errors && e.errors.length) toast('导入配置无效：' + e.errors.join('；'), true);
        else toast(e.message, true);
        $('#config-preview').innerHTML = '';
    }
}

function fmt(v) {
    if (v === null || v === undefined) return '';
    if (typeof v === 'boolean') return v ? 'true' : 'false';
    if (Array.isArray(v)) return v.join(', ') || '(空)';
    return String(v);
}

function entryLabel(e) {
    return '<strong>' + esc(e.name) + '</strong>' +
        (e.url ? ' <span class="muted">' + esc(e.url) + '</span>' : '');
}

function changeCell(changes) {
    if (!changes || !changes.length) return '<span class="muted">—</span>';
    return changes.map(c => `<code>${esc(c.field)}</code>: ${esc(fmt(c.existing))} → ${esc(fmt(c.imported))}`).join('<br>');
}

function chip(key, label, n) {
    if (!n) return '';
    return `<button type="button" class="btn btn-sm diff-chip" data-diff="${key}">${label} ${n}</button>`;
}

function renderPreview(data) {
    const p = $('#config-preview');
    const s = data.summary || {};
    const warns = (data.warnings || []).map(w => `<div class="alert warn">${esc(w)}</div>`).join('');
    const r = s.repositories || {}, st = s.storages || {}, g = s.globals || {}, sv = s.server || {};

    p.innerHTML = warns + `
        <div class="diff-summary">
            ${chip('repos-added', '仓库 新增', r.added)}
            ${chip('repos-deleted', '仓库 删除', r.deleted)}
            ${chip('repos-modified', '仓库 修改', r.modified)}
            ${chip('storages-added', '存储 新增', st.added)}
            ${chip('storages-deleted', '存储 删除', st.deleted)}
            ${chip('storages-modified', '存储 修改', st.modified)}
            ${chip('globals', '全局项 变更', g.changed)}
            ${chip('server', 'server 段 变更', sv.changed)}
        </div>
        <div id="diff-repos-added" class="diff-section hidden"></div>
        <div id="diff-repos-deleted" class="diff-section hidden"></div>
        <div id="diff-repos-modified" class="diff-section hidden"></div>
        <div id="diff-storages-added" class="diff-section hidden"></div>
        <div id="diff-storages-deleted" class="diff-section hidden"></div>
        <div id="diff-storages-modified" class="diff-section hidden"></div>
        <div id="diff-globals" class="diff-section hidden"></div>
        <div id="diff-server" class="diff-section hidden"></div>
        <div class="toolbar-group" style="margin-top:12px;">
            <button id="btn-config-apply" class="btn btn-primary">应用导入</button>
        </div>`;

    renderAdded('#diff-repos-added', data.repositories.added);
    renderDeleted('#diff-repos-deleted', data.repositories.deleted, 'repo');
    renderModified('#diff-repos-modified', data.repositories.modified, 'repo');
    renderAdded('#diff-storages-added', data.storages.added);
    renderDeleted('#diff-storages-deleted', data.storages.deleted, 'storage');
    renderModified('#diff-storages-modified', data.storages.modified, 'storage');
    renderFieldChoices('#diff-globals', data.globals, 'global', '全局项');
    renderFieldChoices('#diff-server', data.server, 'server', 'server 段');

    $$('.diff-chip').forEach(ch => ch.addEventListener('click', () => {
        const sec = $('#diff-' + ch.dataset.diff);
        if (sec) sec.classList.toggle('hidden');
    }));
    $('#btn-config-apply').addEventListener('click', applyImport);
}

function renderAdded(sel, entries) {
    const box = $(sel);
    if (!entries || !entries.length) return;
    box.innerHTML = `<div class="diff-header"><strong>新增（${entries.length}，将采用导入）</strong></div>
        <div class="table-wrap"><table class="table"><tbody>
        ${entries.map(e => `<tr><td>${entryLabel(e)}</td></tr>`).join('')}
        </tbody></table></div>`;
    box.classList.remove('hidden');
}

function renderDeleted(sel, entries, kind) {
    const box = $(sel);
    if (!entries || !entries.length) return;
    const rows = entries.map(e => `
        <tr data-key="${esc(e.key || e.name)}">
            <td>${entryLabel(e)}</td>
            <td class="muted">导入配置中不存在</td>
            <td>
                <label class="radio"><input type="radio" name="${kind}-del-${esc(e.key || e.name)}" value="delete"> 删除</label>
                <label class="radio"><input type="radio" name="${kind}-del-${esc(e.key || e.name)}" value="keep" checked> 保留</label>
            </td>
        </tr>`).join('');
    box.innerHTML = `<div class="diff-header"><strong>删除（${entries.length}，默认保留）</strong>
        <button type="button" class="btn btn-sm" data-bulk-del="delete">全部删除</button>
        <button type="button" class="btn btn-sm" data-bulk-del="keep">全部保留</button></div>
        <div class="table-wrap"><table class="table"><thead><tr><th>条目</th><th>说明</th><th>选择</th></tr></thead><tbody>${rows}</tbody></table></div>`;
    box.classList.remove('hidden');
    $$('[data-bulk-del]', box).forEach(b => b.addEventListener('click', () => {
        const val = b.dataset.bulkDel;
        $$('input[type=radio]', box).forEach(r => { r.checked = (r.value === val); });
    }));
}

function renderModified(sel, entries, kind) {
    const box = $(sel);
    if (!entries || !entries.length) return;
    const rows = entries.map(e => `
        <tr data-key="${esc(e.key || e.name)}">
            <td>${entryLabel(e)}</td>
            <td>${changeCell(e.changes)}</td>
            <td>
                <label class="radio"><input type="radio" name="${kind}-${esc(e.key || e.name)}" value="imported" checked> 采用导入</label>
                <label class="radio"><input type="radio" name="${kind}-${esc(e.key || e.name)}" value="existing"> 保留现有</label>
            </td>
        </tr>`).join('');
    box.innerHTML = `<div class="diff-header"><strong>修改（${entries.length}，默认采用导入）</strong>
        <button type="button" class="btn btn-sm" data-bulk="imported">全部采用导入</button>
        <button type="button" class="btn btn-sm" data-bulk="existing">全部保留</button></div>
        <div class="table-wrap"><table class="table"><thead><tr><th>条目</th><th>差异</th><th>选择</th></tr></thead><tbody>${rows}</tbody></table></div>`;
    box.classList.remove('hidden');
    $$('[data-bulk]', box).forEach(b => b.addEventListener('click', () => {
        const val = b.dataset.bulk;
        $$('input[type=radio]', box).forEach(r => { r.checked = (r.value === val); });
    }));
}

function renderFieldChoices(sel, changes, kind, title) {
    const box = $(sel);
    if (!changes || !changes.length) return;
    const rows = changes.map(c => `
        <tr data-field="${esc(c.field)}">
            <td><code>${esc(c.field)}</code></td>
            <td class="muted">${esc(fmt(c.existing))}</td>
            <td class="muted">${esc(fmt(c.imported))}</td>
            <td>
                <label class="radio"><input type="radio" name="${kind}-${esc(c.field)}" value="imported" checked> 采用导入</label>
                <label class="radio"><input type="radio" name="${kind}-${esc(c.field)}" value="existing"> 保留现有</label>
            </td>
        </tr>`).join('');
    box.innerHTML = `<div class="diff-header"><strong>${title}（变更 ${changes.length}，默认采用导入）</strong></div>
        <div class="table-wrap"><table class="table"><thead><tr><th>字段</th><th>现有</th><th>导入</th><th>选择</th></tr></thead><tbody>${rows}</tbody></table></div>`;
    box.classList.remove('hidden');
}

async function applyImport() {
    const config = $('#config-import').value.trim();
    if (!config) return;
    const choices = {
        repository_deletions: [],
        repository_choices: {},
        storage_deletions: [],
        storage_choices: {},
        global_choices: {},
        server_choices: {}
    };
    const collect = (sel, map, deleteArr) => {
        $$(sel + ' tr[data-key]').forEach(tr => {
            const input = $$('input[type=radio]:checked', tr)[0];
            if (!input) return;
            const key = tr.dataset.key;
            if (deleteArr && input.value === 'delete') deleteArr.push(key);
            else if (!deleteArr) map[key] = input.value;
        });
    };
    collect('#diff-repos-modified', choices.repository_choices, null);
    collect('#diff-repos-deleted', null, choices.repository_deletions);
    collect('#diff-storages-modified', choices.storage_choices, null);
    collect('#diff-storages-deleted', null, choices.storage_deletions);
    $$('#diff-globals tr[data-field]').forEach(tr => {
        const input = $$('input[type=radio]:checked', tr)[0];
        if (input) choices.global_choices[tr.dataset.field] = input.value;
    });
    $$('#diff-server tr[data-field]').forEach(tr => {
        const input = $$('input[type=radio]:checked', tr)[0];
        if (input) choices.server_choices[tr.dataset.field] = input.value;
    });
    try {
        const data = await api('/api/config/import', {
            method: 'POST',
            body: JSON.stringify({ config, choices })
        });
        const d = data || {};
        toast('导入完成：仓库 +' + (d.repositories_added || 0) + '/改 ' + (d.repositories_updated || 0) +
              '/删 ' + (d.repositories_deleted || 0) + '；存储 +' + (d.storages_added || 0) +
              '/改 ' + (d.storages_updated || 0) + '/删 ' + (d.storages_deleted || 0));
        renderApp();
    } catch (e) {
        if (e.errors && e.errors.length) toast('导入失败：' + e.errors.join('；'), true);
        else toast('导入失败: ' + e.message, true);
    }
}

async function reloadConfig() {
    try {
        await api('/api/config/reload', { method: 'POST' });
        toast('配置已从磁盘重载');
        renderApp();
    } catch (e) {
        toast('重载失败: ' + e.message, true);
    }
}
```

- [ ] **Step 4: Add the Config-page CSS**

Append to `web/static/css/main.css`:

```css
/* Config tab */
.hidden { display: none !important; }
.panel-pad { padding: 16px; }
.panel-pad h3 { margin: 0 0 10px; }
.config-textarea {
    width: 100%;
    min-height: 220px;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    font-size: 12px;
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 12px;
    background: var(--panel);
    resize: vertical;
    box-sizing: border-box;
}
.diff-summary { display: flex; flex-wrap: wrap; gap: 8px; margin: 12px 0; }
.diff-chip { cursor: pointer; }
.diff-chip:hover { background: #f3f4f6; }
.diff-section { margin: 10px 0; padding: 12px; border: 1px solid var(--border); border-radius: 8px; background: var(--panel); }
.diff-header { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
.diff-header strong { flex: 1; }
.radio { display: inline-flex; align-items: center; gap: 4px; margin-right: 12px; font-size: 13px; color: var(--muted); }
.alert.warn { padding: 10px 12px; border-radius: 8px; background: #fef3c7; color: #92400e; margin-bottom: 12px; }
```

- [ ] **Step 5: Manual verification**

Run: `go build -o gitrieve main.go && ./gitrieve server`
Then in a browser:
1. Open the web UI, click the **Config** tab. The nav highlights "Config"; the page shows three panels.
2. Click **刷新** — the export textarea fills with the full YAML (repository + storage + globals + `server:`), `retryBaseDelay: 5s`, and plaintext secrets.
3. Click **复制** — clipboard toast; **下载 config.yaml** downloads a file.
4. Paste the exported YAML into the import textarea with an edit (e.g. change a repo's cron, add a repo). Click **预览差异**. Summary chips show counts; warnings appear if the server section changed. Click a chip to expand the section; unchanged items are not shown. Modified rows show `字段: 现有 → 导入` with 采用导入/保留现有 radios; deleted rows default to 保留 with a 全部删除 button.
5. Toggle some radios (including 全部采用导入 / 全部保留), click **应用导入**. The toast reports the apply counts; the Repositories page reflects the merge.
6. Use **选择文件** to pick a config.yaml and confirm the import textarea fills.
7. Edit config.yaml on disk, then click **刷新配置** in the operations panel — toast "配置已从磁盘重载"; the Repositories page reflects the file.
8. Break config.yaml (invalid YAML) and click **刷新配置** — an error toast appears and the previous config stays active (server still up).

- [ ] **Step 6: Commit**

```bash
git add web/templates/index.html web/static/js/main.js web/static/css/main.css
git commit -m "feat(ui): config tab with export/import/reload"
```
```
