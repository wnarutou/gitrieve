package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/wnarutou/gitrieve/cmd/daemon"
	"github.com/wnarutou/gitrieve/cmd/discussion"
	"github.com/wnarutou/gitrieve/cmd/issue"
	"github.com/wnarutou/gitrieve/cmd/release"
	"github.com/wnarutou/gitrieve/cmd/repository"
	"github.com/wnarutou/gitrieve/cmd/run"
	"github.com/wnarutou/gitrieve/cmd/wiki"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/ui"
)

var rootCmd = &cobra.Command{
	Use:   "gitrieve",
	Short: "gitrieve is a tool to backup git repositories.",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		ui.ErrorfExit(fmt.Sprintf("Error executing command, %s", err))
	}
}

func init() {
	cobra.OnInitialize(config.Init)
	// commands
	rootCmd.AddCommand(repository.Cmd)
	rootCmd.AddCommand(run.Cmd)
	rootCmd.AddCommand(daemon.Cmd)
	rootCmd.AddCommand(release.Cmd)
	rootCmd.AddCommand(issue.Cmd)
	rootCmd.AddCommand(wiki.Cmd)
	rootCmd.AddCommand(discussion.Cmd)
	// flags
	rootCmd.PersistentFlags().StringVarP(&config.Path, "config", "c", "config.yaml", "config file path")
}
