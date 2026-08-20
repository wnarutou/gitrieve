package config

import (
	"fmt"
	"time"

	"github.com/spf13/viper"
	"github.com/wnarutou/gitrieve/internal/retry"
	"github.com/wnarutou/gitrieve/internal/typedef"
	"github.com/wnarutou/gitrieve/internal/ui"
)

type Config struct {
	Repository       []typedef.Repository   `yaml:"repository"`
	Storage          []typedef.MultiStorage `yaml:"storage"`
	GitHubToken      string                 `yaml:"githubToken"`
	ConcurrencyNum   uint                   `yaml:"cocurrencyNum" mapstructure:"cocurrencyNum"`
	ReleaseSizeLimit int                    `yaml:"releaseSizeLimit"`
	ReleaseNumLimit  int                    `yaml:"releaseNumLimit"`
	RetryMaxCount    int                    `yaml:"retryMaxCount"`
	RetryBaseDelay   time.Duration          `yaml:"retryBaseDelay"`
}

var Path string

var vp *viper.Viper
var ins *Config

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

func GetIns() *Config {
	return ins
}

// GetViper returns the viper instance that loaded the config file. It is nil
// until Init has run (registered via cobra.OnInitialize, so it runs before any
// command executes). Exposed so packages that read config sections outside the
// top-level Config struct (e.g. the `server:` settings) read from the same
// loaded instance rather than the empty global viper singleton.
func GetViper() *viper.Viper {
	return vp
}

func GetStorageMap() map[string]typedef.MultiStorage {
	storageMap := make(map[string]typedef.MultiStorage)
	for _, storage := range ins.Storage {
		storageMap[storage.Name] = storage
	}
	return storageMap
}

// GetReleaseNumLimit returns the max number of releases to keep. Init seeds it
// to 3 when the config value is zero; a negative value means "no limit". It is
// read-only (no lazy mutation) so it is safe under concurrent workers.
func GetReleaseNumLimit() int {
	return ins.ReleaseNumLimit
}

// GetReleaseSizeLimit returns the max total release size to keep. Init seeds it
// to 300000000 when the config value is zero; a negative value means "no
// limit". It is read-only so it is safe under concurrent workers.
func GetReleaseSizeLimit() int {
	return ins.ReleaseSizeLimit
}

// GetConcurrencyNum returns the max number of concurrent scheduler jobs. Init
// seeds it to 3 when the config value is zero. It is read-only so it is safe
// under concurrent workers.
func GetConcurrencyNum() uint {
	return ins.ConcurrencyNum
}

// GetRetryMaxCount returns the configured max retries per API call. Init seeds
// it to 3 when the config value is zero or negative, so this getter is
// read-only.
func GetRetryMaxCount() int {
	return ins.RetryMaxCount
}

// GetRetryBaseDelay returns the exponential-backoff base delay. Init seeds it
// to 5 seconds when the config value is zero or negative, so this getter is
// read-only.
func GetRetryBaseDelay() time.Duration {
	return ins.RetryBaseDelay
}

// GetRetryConfig assembles the retry configuration used by every GitHub API
// call site in the issue/discussion/release syncs.
func GetRetryConfig() retry.Config {
	return retry.Config{
		MaxRetries: GetRetryMaxCount(),
		BaseDelay:  GetRetryBaseDelay(),
	}
}

// validateIdentity ensures every repository entry has a usable identity (a
// non-empty URL, or orgName for user/org types). The repository identity is the
// normalized URL; an entry without one can never be matched or executed.
// Returns an error rather than exiting so it is unit-testable; Init surfaces
// it via ui.ErrorfExit.
func validateIdentity(cfg *Config) error {
	for _, repo := range cfg.Repository {
		if repo.Key() == "" {
			return fmt.Errorf("repository %q (type %q) has an empty URL and no orgName; every repository needs a URL identity",
				repo.Name, repo.GetType())
		}
	}
	return nil
}

// Save persists the current in-memory config back to the config file via viper.
func Save() error {
	if vp == nil {
		return fmt.Errorf("config not initialized")
	}
	// Update the viper config with current ins values
	vp.Set("repository", ins.Repository)
	vp.Set("storage", ins.Storage)
	vp.Set("githubToken", ins.GitHubToken)
	vp.Set("cocurrencyNum", ins.ConcurrencyNum)
	vp.Set("releaseSizeLimit", ins.ReleaseSizeLimit)
	vp.Set("releaseNumLimit", ins.ReleaseNumLimit)
	vp.Set("retryMaxCount", ins.RetryMaxCount)
	vp.Set("retryBaseDelay", ins.RetryBaseDelay)
	return vp.WriteConfig()
}
