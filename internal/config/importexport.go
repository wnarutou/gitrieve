package config

import (
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/viper"
	"github.com/wnarutou/gitrieve/internal/typedef"
	"gopkg.in/yaml.v3"
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
