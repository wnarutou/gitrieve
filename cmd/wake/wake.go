package wake

import (
	"github.com/leslieleung/reaper/internal/config"
	"github.com/leslieleung/reaper/internal/issue"
	"github.com/leslieleung/reaper/internal/rip"
	"github.com/leslieleung/reaper/internal/typedef"
	"github.com/leslieleung/reaper/internal/ui"
	"github.com/spf13/cobra"
)

var Cmd = &cobra.Command{
	Use:   "wake",
	Short: "wake immediately downloads all issues of a repo",
	Run:   runWake,
	Args:  cobra.ExactArgs(1),
}

var storageName string

func runWake(cmd *cobra.Command, args []string) {
	repoName := args[0]

	storageMap := config.GetStorageMap()
	storages := make([]typedef.MultiStorage, 0)
	if storageName != "" {
		if s, ok := storageMap[storageName]; !ok {
			ui.Errorf("Storage %s not found in config", storageName)
			return
		} else {
			storages = append(storages, s)
		}
	} else {
		for _, storage := range storageMap {
			storages = append(storages, storage)
		}
	}

	for _, repo := range rip.GetRepositories(repoName) {
		ui.Printf("Running %s", repo.Name)
		if err := issue.Sync(repo, storages); err != nil {
			ui.Errorf("Error running %s, %s", repo.Name, err)
			// move on to next repo
		}
	}
	ui.Printf("Done")
}

func init() {
	Cmd.Flags().StringVarP(&storageName, "storage", "s", "",
		"storage to use, if not specified, all storages will be used")
}
