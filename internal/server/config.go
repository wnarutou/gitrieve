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
