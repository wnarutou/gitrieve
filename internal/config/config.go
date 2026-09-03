package config

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/viper"
	"github.com/wnarutou/gitrieve/internal/githubapi"
	"github.com/wnarutou/gitrieve/internal/retry"
	"github.com/wnarutou/gitrieve/internal/typedef"
	"github.com/wnarutou/gitrieve/internal/ui"
)

const (
	DefaultSyncOverdueGrace   = 30 * time.Minute
	DefaultSyncStuckThreshold = 24 * time.Hour
)

type Config struct {
	Repository                  []typedef.Repository   `yaml:"repository"`
	Storage                     []typedef.MultiStorage `yaml:"storage"`
	GitHubToken                 string                 `yaml:"githubToken"`
	ConcurrencyNum              uint                   `yaml:"cocurrencyNum" mapstructure:"cocurrencyNum"`
	ReleaseSizeLimit            int                    `yaml:"releaseSizeLimit"`
	ReleaseNumLimit             int                    `yaml:"releaseNumLimit"`
	RetryMaxCount               int                    `yaml:"retryMaxCount"`
	RetryBaseDelay              time.Duration          `yaml:"retryBaseDelay"`
	SyncOverdueGrace            time.Duration          `yaml:"syncOverdueGrace" mapstructure:"syncOverdueGrace"`
	SyncStuckThreshold          time.Duration          `yaml:"syncStuckThreshold" mapstructure:"syncStuckThreshold"`
	GitHubAPIConcurrency        uint                   `yaml:"githubApiConcurrency" mapstructure:"githubApiConcurrency"`
	GitHubMinRequestInterval    time.Duration          `yaml:"githubMinRequestInterval" mapstructure:"githubMinRequestInterval"`
	GitHubLowRemainingThreshold int                    `yaml:"githubLowRemainingThreshold" mapstructure:"githubLowRemainingThreshold"`
	GitHubScheduleJitter        time.Duration          `yaml:"githubScheduleJitter" mapstructure:"githubScheduleJitter"`
}

var Path string

var vp *viper.Viper
var vpMu sync.Mutex
var ins atomic.Pointer[Config]

func Init() {
	nextViper := viper.New()
	nextViper.SetConfigFile(Path)
	err := nextViper.ReadInConfig()
	if err != nil {
		ui.ErrorfExit("Error reading config file, %s", err)
	}
	var next Config
	err = nextViper.Unmarshal(&next)
	if err != nil {
		ui.ErrorfExit("Error unmarshalling config file, %s", err)
	}
	seedDefaults(&next)
	if !nextViper.IsSet("githubScheduleJitter") {
		next.GitHubScheduleJitter = 30 * time.Second
	}
	// Every repository entry must have an identity before publication.
	if err := validateIdentity(&next); err != nil {
		ui.ErrorfExit("Invalid configuration: %s", err)
	}
	vpMu.Lock()
	vp = nextViper
	vpMu.Unlock()
	SetIns(&next)
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
	if cfg.SyncOverdueGrace <= 0 {
		cfg.SyncOverdueGrace = DefaultSyncOverdueGrace
	}
	if cfg.SyncStuckThreshold <= 0 {
		cfg.SyncStuckThreshold = DefaultSyncStuckThreshold
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
	if cfg.GitHubAPIConcurrency == 0 {
		cfg.GitHubAPIConcurrency = 2
	}
	if cfg.GitHubMinRequestInterval <= 0 {
		cfg.GitHubMinRequestInterval = 200 * time.Millisecond
	}
	if cfg.GitHubLowRemainingThreshold <= 0 {
		cfg.GitHubLowRemainingThreshold = 100
	}
	if cfg.GitHubScheduleJitter < 0 {
		cfg.GitHubScheduleJitter = 30 * time.Second
	}
}

// Clone returns a deep copy suitable for copy-on-write configuration updates.
func Clone(cfg *Config) *Config {
	if cfg == nil {
		return nil
	}
	cloned := *cfg
	cloned.Repository = make([]typedef.Repository, len(cfg.Repository))
	for index, repository := range cfg.Repository {
		repository.Storage = append([]string(nil), repository.Storage...)
		cloned.Repository[index] = repository
	}
	cloned.Storage = append([]typedef.MultiStorage(nil), cfg.Storage...)
	return &cloned
}

func GetIns() *Config {
	return Clone(ins.Load())
}

// SetIns replaces the package-global config instance. Used by apply/import to
// publish a fully-built replacement so concurrent readers (job goroutines)
// observe a complete old or complete new instance, never a torn one.
func SetIns(cfg *Config) {
	snapshot := Clone(cfg)
	ins.Store(snapshot)
	githubapi.Configure(gitHubAPIConfig(snapshot))
}

func currentConfig() *Config {
	cfg := ins.Load()
	if cfg == nil {
		return &Config{}
	}
	return cfg
}

// GetViper returns the viper instance that loaded the config file. It is nil
// until Init has run (registered via cobra.OnInitialize, so it runs before any
// command executes). Exposed so packages that read config sections outside the
// top-level Config struct (e.g. the `server:` settings) read from the same
// loaded instance rather than the empty global viper singleton.
func GetViper() *viper.Viper {
	vpMu.Lock()
	defer vpMu.Unlock()
	return vp
}

func GetStorageMap() map[string]typedef.MultiStorage {
	storageMap := make(map[string]typedef.MultiStorage)
	for _, storage := range currentConfig().Storage {
		storageMap[storage.Name] = storage
	}
	return storageMap
}

// GetReleaseNumLimit returns the max number of releases to keep. Init seeds it
// to 3 when the config value is zero; a negative value means "no limit". It is
// read-only (no lazy mutation) so it is safe under concurrent workers.
func GetReleaseNumLimit() int {
	return currentConfig().ReleaseNumLimit
}

// GetReleaseSizeLimit returns the max total release size to keep. Init seeds it
// to 300000000 when the config value is zero; a negative value means "no
// limit". It is read-only so it is safe under concurrent workers.
func GetReleaseSizeLimit() int {
	return currentConfig().ReleaseSizeLimit
}

// GetConcurrencyNum returns the max number of concurrent scheduler jobs. Init
// seeds it to 3 when the config value is zero. It is read-only so it is safe
// under concurrent workers.
func GetConcurrencyNum() uint {
	return currentConfig().ConcurrencyNum
}

// GetRetryMaxCount returns the configured max retries per API call. Init seeds
// it to 3 when the config value is zero or negative, so this getter is
// read-only.
func GetRetryMaxCount() int {
	return currentConfig().RetryMaxCount
}

// GetRetryBaseDelay returns the exponential-backoff base delay. Init seeds it
// to 5 seconds when the config value is zero or negative, so this getter is
// read-only.
func GetRetryBaseDelay() time.Duration {
	return currentConfig().RetryBaseDelay
}

func GetSyncOverdueGrace() time.Duration { return currentConfig().SyncOverdueGrace }

func GetSyncStuckThreshold() time.Duration { return currentConfig().SyncStuckThreshold }

func GetGitHubAPIConcurrency() uint { return currentConfig().GitHubAPIConcurrency }

func GetGitHubMinRequestInterval() time.Duration { return currentConfig().GitHubMinRequestInterval }

func GetGitHubLowRemainingThreshold() int { return currentConfig().GitHubLowRemainingThreshold }

func GetGitHubScheduleJitter() time.Duration { return currentConfig().GitHubScheduleJitter }

func GetGitHubAPIConfig() githubapi.Config {
	return gitHubAPIConfig(currentConfig())
}

func gitHubAPIConfig(cfg *Config) githubapi.Config {
	if cfg == nil {
		return githubapi.Config{}
	}
	return githubapi.Config{Concurrency: cfg.GitHubAPIConcurrency, MinRequestInterval: cfg.GitHubMinRequestInterval, LowRemainingThreshold: cfg.GitHubLowRemainingThreshold}
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
	vpMu.Lock()
	defer vpMu.Unlock()
	if vp == nil {
		return fmt.Errorf("config not initialized")
	}
	cfg := currentConfig()
	// Update the viper config with current ins values
	vp.Set("repository", cfg.Repository)
	vp.Set("storage", cfg.Storage)
	vp.Set("githubToken", cfg.GitHubToken)
	vp.Set("cocurrencyNum", cfg.ConcurrencyNum)
	vp.Set("releaseSizeLimit", cfg.ReleaseSizeLimit)
	vp.Set("releaseNumLimit", cfg.ReleaseNumLimit)
	vp.Set("retryMaxCount", cfg.RetryMaxCount)
	vp.Set("retryBaseDelay", cfg.RetryBaseDelay)
	vp.Set("syncOverdueGrace", cfg.SyncOverdueGrace)
	vp.Set("syncStuckThreshold", cfg.SyncStuckThreshold)
	vp.Set("githubApiConcurrency", cfg.GitHubAPIConcurrency)
	vp.Set("githubMinRequestInterval", cfg.GitHubMinRequestInterval)
	vp.Set("githubLowRemainingThreshold", cfg.GitHubLowRemainingThreshold)
	vp.Set("githubScheduleJitter", cfg.GitHubScheduleJitter)
	return vp.WriteConfig()
}
