package server

import (
	"github.com/spf13/viper"
)

type ServerConfig struct {
	Port     string `mapstructure:"port"`
	Host     string `mapstructure:"host"`
	AuthEnabled bool `mapstructure:"authEnabled"`
	AuthToken string `mapstructure:"authToken"`
}

func GetServerConfig() *ServerConfig {
	cfg := &ServerConfig{}
	viper.SetDefault("server.port", "8080")
	viper.SetDefault("server.host", "localhost")
	viper.SetDefault("server.authEnabled", false)
	viper.SetDefault("server.authToken", "")

	if err := viper.Unmarshal(cfg); err != nil {
		panic(err)
	}

	return cfg
}