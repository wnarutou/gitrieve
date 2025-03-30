package cmd

import (
	"fmt"

	"github.com/leslieleung/reaper/cmd/bury"
	"github.com/leslieleung/reaper/cmd/daemon"
	"github.com/leslieleung/reaper/cmd/elegy"
	"github.com/leslieleung/reaper/cmd/miss"
	"github.com/leslieleung/reaper/cmd/rip"
	"github.com/leslieleung/reaper/cmd/run"
	"github.com/leslieleung/reaper/cmd/wake"
	"github.com/leslieleung/reaper/internal/config"
	"github.com/leslieleung/reaper/internal/ui"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "reaper",
	Short: "Reaper is a tool to backup git repositories.",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		ui.ErrorfExit(fmt.Sprintf("Error executing command, %s", err))
	}
}

func init() {
	cobra.OnInitialize(config.Init)
	// commands
	rootCmd.AddCommand(rip.Cmd)
	rootCmd.AddCommand(run.Cmd)
	rootCmd.AddCommand(daemon.Cmd)
	rootCmd.AddCommand(bury.Cmd)
	rootCmd.AddCommand(wake.Cmd)
	rootCmd.AddCommand(elegy.Cmd)
	rootCmd.AddCommand(miss.Cmd)
	// flags
	rootCmd.PersistentFlags().StringVarP(&config.Path, "config", "c", "config.yaml", "config file path")
}
