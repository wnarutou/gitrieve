package wiki

import (
	"context"

	gh "github.com/google/go-github/v56/github"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/repository"
	"github.com/wnarutou/gitrieve/internal/scm"
	"github.com/wnarutou/gitrieve/internal/typedef"
	"github.com/wnarutou/gitrieve/internal/ui"
)

func Sync(ctx context.Context, repo typedef.Repository, storages []typedef.MultiStorage) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// get the repo name from the URL
	r, err := scm.NewRepository(repo.URL)
	if err != nil {
		return err
	}
	repoName := r.Name
	if repoName == "." || repoName == "/" {
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
	if err := repository.Sync(ctx, repo, true, storages); err != nil {
		if ctx.Err() == nil {
			ui.Errorf("Error running %s's wiki, %s", repo.Name, err)
		}
		return err
	}
	return nil
}
