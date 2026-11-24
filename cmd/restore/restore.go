package restore

import (
	"context"
	"os"

	"github.com/spf13/cobra"
	"github.com/wnarutou/gitrieve/internal/cache"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/lock"
	"github.com/wnarutou/gitrieve/internal/repository"
	"github.com/wnarutou/gitrieve/internal/scm"
	"github.com/wnarutou/gitrieve/internal/typedef"
	"github.com/wnarutou/gitrieve/internal/ui"
)

var Cmd = &cobra.Command{
	Use:   "restore",
	Short: "restore local caches from configured storage backups",
	Run:   runRestore,
	Args:  cobra.ExactArgs(1),
}

func runRestore(cmd *cobra.Command, args []string) {
	ctx := context.Background()
	currentDir, err := os.Getwd()
	if err != nil {
		ui.Errorf("Error getting current directory: %s", err)
		return
	}

	storageMap := config.GetStorageMap()
	for _, repo := range repository.GetRepositories(args[0]) {
		if !repo.UseCache {
			ui.Printf("%s does not use a persistent cache; nothing to restore", repo.URL)
			continue
		}
		r, err := scm.NewRepository(repo.URL)
		if err != nil {
			ui.Errorf("Invalid repository %s: %s", repo.URL, err)
			continue
		}
		storages := make([]typedef.MultiStorage, 0, len(repo.Storage))
		for _, name := range repo.Storage {
			s, ok := storageMap[name]
			if !ok {
				ui.Errorf("Storage %s not found in config", name)
				continue
			}
			storages = append(storages, s)
		}

		for _, component := range cacheComponents(repo) {
			restoreComponent(ctx, currentDir, r, repo, component, storages)
		}
		if repo.DownloadReleases {
			ui.Printf("%s release assets are stored directly in configured storage; no local cache restore is required", repo.URL)
		}
	}
}

func restoreComponent(
	ctx context.Context,
	currentDir string,
	r *scm.Repository,
	repo typedef.Repository,
	component cache.Component,
	storages []typedef.MultiStorage,
) {
	unlock, err := lock.Acquire(ctx, r, lockComponent(component), currentDir)
	if err != nil {
		ui.Errorf("Failed to lock %s cache for %s: %s", component, repo.URL, err)
		return
	}
	defer unlock()

	result, err := cache.RestoreComponent(ctx, currentDir, r, component, storages)
	if err != nil {
		ui.Errorf("Failed to restore %s cache for %s: %s", component, repo.URL, err)
		return
	}
	switch result.Status {
	case cache.StatusRestored:
		ui.Printf("Restored %s cache for %s from storage %s", component, repo.URL, result.StorageName)
	case cache.StatusAlreadyPresent:
		ui.Printf("%s cache for %s is already present", component, repo.URL)
	case cache.StatusNotFound:
		ui.Printf("No %s backup found for %s", component, repo.URL)
	}
}

func cacheComponents(repo typedef.Repository) []cache.Component {
	components := []cache.Component{cache.ComponentCode}
	if repo.DownloadWiki {
		components = append(components, cache.ComponentWiki)
	}
	if repo.DownloadIssues {
		components = append(components, cache.ComponentIssues)
	}
	if repo.DownloadDiscussion {
		components = append(components, cache.ComponentDiscussion)
	}
	return components
}

func lockComponent(component cache.Component) string {
	if component == cache.ComponentIssues {
		return "issue"
	}
	return string(component)
}
