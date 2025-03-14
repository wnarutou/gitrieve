package elegy

import (
	"github.com/leslieleung/reaper/internal/config"
	"github.com/leslieleung/reaper/internal/rip"
	"github.com/leslieleung/reaper/internal/typedef"
	"github.com/leslieleung/reaper/internal/ui"
	"github.com/leslieleung/reaper/internal/wiki"
	"github.com/spf13/cobra"
)

var Cmd = &cobra.Command{
	Use:   "elegy",
	Short: "elegy immediately downloads all wiki of a repo",
	Run:   runElegy,
	Args:  cobra.ExactArgs(1),
}

var storageName string

func runElegy(cmd *cobra.Command, args []string) {
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
		for _, storage := range repo.Storage {
			if s, ok := storageMap[storage]; !ok {
				ui.Errorf("Storage %s not found in config", storage)
				continue
			} else {
				storages = append(storages, s)
			}
		}
		ui.Printf("Running %s", repo.Name)
		if err := wiki.Sync(repo, storages); err != nil {
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
