package server

import (
	"github.com/spf13/viper"
	"github.com/wnarutou/gitrieve/internal/config"
)

type ServerConfig struct {
	Port        string `mapstructure:"port"`
	Host        string `mapstructure:"host"`
	AuthEnabled bool   `mapstructure:"authEnabled"`
	AuthToken   string `mapstructure:"authToken"`
	DbPath      string `mapstructure:"dbPath"`
}

// GetServerConfig returns the `server:` section of the config file, applying
// defaults for any unset field. It reads from the viper instance that loaded
// the config file (see config.Init) so host/port/dbPath in config.yaml are
// honored; if Init has not run yet (e.g. in tests) it falls back to the global
// viper singleton so callers can still override settings directly.
func GetServerConfig() *ServerConfig {
	cfg := &ServerConfig{}

	v := config.GetViper()
	if v == nil {
		v = viper.GetViper()
	}

	// Defaults — set before UnmarshalKey so missing keys fall back to them.
	v.SetDefault("server.port", "8080")
	v.SetDefault("server.host", "localhost")
	v.SetDefault("server.authEnabled", false)
	v.SetDefault("server.authToken", "")
	v.SetDefault("server.dbPath", "gitrieve.db")

	// UnmarshalKey resolves the nested `server:` section into the flat struct;
	// a plain Unmarshal would not see any of these keys.
	if err := v.UnmarshalKey("server", cfg); err != nil {
		panic(err)
	}

	return cfg
}
