package config

import (
	"fmt"
	"github.com/spf13/viper"
	"github.com/wnarutou/gitrieve/internal/typedef"
	"github.com/wnarutou/gitrieve/internal/ui"
)

type Config struct {
	Repository       []typedef.Repository   `yaml:"repository"`
	Storage          []typedef.MultiStorage `yaml:"storage"`
	GitHubToken      string                 `yaml:"githubToken"`
	ConcurrencyNum   uint                   `yaml:"cocurrencyNum"`
	ReleaseSizeLimit int                    `yaml:"releaseSizeLimit"`
	ReleaseNumLimit  int                    `yaml:"releaseNumLimit"`
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

func GetReleaseNumLimit() int {
	if ins.ReleaseNumLimit == 0 {
		// Keep the last three releases by default
		// Less than 0 means no limit
		// But it also needs to obey ReleaseSizeLimit
		ins.ReleaseNumLimit = 3
	}
	return ins.ReleaseNumLimit
}

func GetReleaseSizeLimit() int {
	if ins.ReleaseSizeLimit == 0 {
		// Keep the maximum 300MB releases by default
		// If the latest release is larger than 300MB, keep the latest release
		// If the total size of all releases is less than 300MB, keep all releases
		// Less than 0 means no limit
		// But it also needs to obey ReleaseNumLimit
		ins.ReleaseSizeLimit = 300000000
	}
	return ins.ReleaseSizeLimit
}

func GetConcurrencyNum() uint {
	// Retrieve the concurrency number from configuration. This determines the maximum number
	// of concurrent jobs the scheduler will run. If not configured (i.e., zero),
	// default to 3 concurrent jobs to ensure stable performance.
	if ins.ConcurrencyNum == 0 {
		ins.ConcurrencyNum = 3
	}
	return ins.ConcurrencyNum
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
	return vp.WriteConfig()
}
