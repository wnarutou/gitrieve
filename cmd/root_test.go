package cmd

import (
	"testing"
	"github.com/spf13/cobra"
)

func TestServerCommandExists(t *testing.T) {
	var cmd *cobra.Command
	var found bool

	rootCmd.Execute()

	for _, c := range rootCmd.Commands() {
		if c.Use == "server" {
			found = true
			cmd = c
			break
		}
	}

	if !found {
		t.Fatal("server command not found")
	}

	if cmd.Short != "start web server" {
		t.Errorf("Expected short description 'start web server', got %s", cmd.Short)
	}
}