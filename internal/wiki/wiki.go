package wiki

import (
	"context"

	gh "github.com/google/go-github/v56/github"
	"github.com/leslieleung/reaper/internal/config"
	"github.com/leslieleung/reaper/internal/rip"
	"github.com/leslieleung/reaper/internal/scm"
	"github.com/leslieleung/reaper/internal/typedef"
	"github.com/leslieleung/reaper/internal/ui"
)

func Sync(repo typedef.Repository, storages []typedef.MultiStorage) error {
	// get the repo name from the URL
	r, err := scm.NewRepository(repo.URL)
	if err != nil {
		return err
	}
	repoName := r.Name
	if repoName == "." || repo.Name == "/" {
		ui.Errorf("Invalid repository name")
		return err
	}

	cfg := config.GetIns()
	client := gh.NewClient(nil).WithAuthToken(cfg.GitHubToken)

	gitrepo, _, err := client.Repositories.Get(context.Background(), r.Owner, r.Name)
	if err != nil {
		ui.Errorf("Get repository %s fail", repo.URL)
		return err
	}

	if !gitrepo.GetHasWiki() {
		ui.Errorf("repository %s has no wiki", repo.URL)
	}

	ui.Printf("Running %s's wiki", repo.Name)
	if err := rip.Rip(repo, true, storages); err != nil {
		ui.Errorf("Error running %s's wiki, %s", repo.Name, err)
		return err
	}
	return nil
}
