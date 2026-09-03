package executor

import (
	"context"

	"github.com/wnarutou/gitrieve/internal/db"
	"github.com/wnarutou/gitrieve/internal/discussion"
	"github.com/wnarutou/gitrieve/internal/issue"
	"github.com/wnarutou/gitrieve/internal/release"
	"github.com/wnarutou/gitrieve/internal/repository"
	"github.com/wnarutou/gitrieve/internal/typedef"
	"github.com/wnarutou/gitrieve/internal/wiki"
)

type SyncFunc func(context.Context, typedef.Repository, []typedef.MultiStorage) error

type Runners struct {
	Code       SyncFunc
	Release    SyncFunc
	Issue      SyncFunc
	Wiki       SyncFunc
	Discussion SyncFunc
}

type component struct {
	name db.ComponentName
	run  SyncFunc
}

func defaultRunners() Runners {
	return Runners{
		Code: func(ctx context.Context, repo typedef.Repository, storages []typedef.MultiStorage) error {
			return repository.Sync(ctx, repo, false, storages)
		},
		Release:    release.DownloadAllAssets,
		Issue:      issue.Sync,
		Wiki:       wiki.Sync,
		Discussion: discussion.Sync,
	}
}

func componentPlan(repo typedef.Repository, runners Runners) []component {
	components := []component{{name: db.ComponentCode, run: runners.Code}}
	if repo.DownloadReleases {
		components = append(components, component{name: db.ComponentRelease, run: runners.Release})
	}
	if repo.DownloadIssues {
		components = append(components, component{name: db.ComponentIssue, run: runners.Issue})
	}
	if repo.DownloadWiki {
		components = append(components, component{name: db.ComponentWiki, run: runners.Wiki})
	}
	if repo.DownloadDiscussion {
		components = append(components, component{name: db.ComponentDiscussion, run: runners.Discussion})
	}
	return components
}
