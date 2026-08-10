package wiki

import (
	"context"
	"os"
	"os/signal"

	"github.com/spf13/cobra"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/repository"
	"github.com/wnarutou/gitrieve/internal/typedef"
	"github.com/wnarutou/gitrieve/internal/ui"
	"github.com/wnarutou/gitrieve/internal/wiki"
)

var Cmd = &cobra.Command{
	Use:   "wiki",
	Short: "wiki immediately downloads all wiki of a repo",
	Run:   runWiki,
	Args:  cobra.ExactArgs(1),
}

var storageName string

func runWiki(cmd *cobra.Command, args []string) {
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	for _, repo := range repository.GetRepositories(repoName) {
		if ctx.Err() != nil {
			ui.Printf("Cancelled")
			break
		}
		ui.Printf("Running %s", repo.Name)
		if err := wiki.Sync(ctx, repo, storages); err != nil {
			if ctx.Err() != nil {
				ui.Printf("Download cancelled")
				break
			}
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
