package run

import (
	"github.com/spf13/cobra"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/repository"
	"github.com/wnarutou/gitrieve/internal/typedef"
	"github.com/wnarutou/gitrieve/internal/ui"
)

var Cmd = &cobra.Command{
	Use:   "run",
	Short: "run runs all repositories defined in config",
	Run:   runRun,
}

func runRun(cmd *cobra.Command, args []string) {
	storageMap := config.GetStorageMap()

	for _, repo := range repository.GetRepositories("") {
		storages := make([]typedef.MultiStorage, 0)
		for _, storage := range repo.Storage {
			if s, ok := storageMap[storage]; !ok {
				ui.Errorf("Storage %s not found in config", storage)
				continue
			} else {
				storages = append(storages, s)
			}
		}
		ui.Printf("Running %s", repo.Name)
		if err := repository.Sync(repo, false, storages); err != nil {
			ui.Errorf("Error running %s, %s", repo.Name, err)
			// move on to next repo
		}
	}
}
