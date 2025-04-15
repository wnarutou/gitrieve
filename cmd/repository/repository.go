package repository

import (
	"github.com/spf13/cobra"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/repository"
	"github.com/wnarutou/gitrieve/internal/typedef"
	"github.com/wnarutou/gitrieve/internal/ui"
)

var Cmd = &cobra.Command{
	Use:   "repository",
	Short: "repository immediately runs a repository sync",
	Run:   runRepository,
	Args:  cobra.ExactArgs(1),
}

func runRepository(cmd *cobra.Command, args []string) {
	repoName := args[0]
	storageMap := config.GetStorageMap()
	// find repo in config
	for _, repo := range repository.GetRepositories(repoName) {
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
