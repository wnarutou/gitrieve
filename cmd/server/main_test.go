package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

// TestMain points `server.dbPath` at a temp directory so the real database
// initialized inside setupRoutes does not pollute the package directory (and
// tests stay independent of the working directory).
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gitrieve-server-test")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	viper.Set("server.dbPath", filepath.Join(dir, "test.db"))
	os.Exit(m.Run())
}
