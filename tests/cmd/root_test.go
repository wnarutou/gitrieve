package cmd

import (
	"testing"
	"github.com/spf13/cobra"
	"github.com/wnarutou/gitrieve/cmd/server"
)

func TestServerCommandExists(t *testing.T) {
	// Create a new root cmd for testing since rootCmd is not exported
	rootCmd := &cobra.Command{
		Use:   "gitrieve",
		Short: "gitrieve is a tool to backup git repositories.",
	}

	// Add the server command
	rootCmd.AddCommand(server.Cmd)

	rootCmd.Execute()

	var cmd *cobra.Command
	var found bool

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