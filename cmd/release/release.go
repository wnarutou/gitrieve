package release

import (
	"github.com/spf13/cobra"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/release"
	"github.com/wnarutou/gitrieve/internal/repository"
	"github.com/wnarutou/gitrieve/internal/typedef"
	"github.com/wnarutou/gitrieve/internal/ui"
)

var Cmd = &cobra.Command{
	Use:   "release",
	Short: "release immediately downloads all release assets of a repo",
	Run:   runRelease,
	Args:  cobra.ExactArgs(1),
}

var storageName string

func runRelease(cmd *cobra.Command, args []string) {
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

	for _, repo := range repository.GetRepositories(repoName) {
		ui.Printf("Running %s", repo.Name)
		if err := release.DownloadAllAssets(repo, storages); err != nil {
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
