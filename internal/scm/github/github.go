package github

import (
	"context"
	"io"
	"net/http"
	"net/url"

	"github.com/google/go-github/v56/github"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/githubapi"
	"github.com/wnarutou/gitrieve/internal/retry"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

type Client struct {
	c *github.Client
}

func New() (*Client, error) {
	return NewWithContext(context.Background())
}

func NewWithContext(ctx context.Context) (*Client, error) {
	c := github.NewClient(nil)
	if token := config.GetExecutionConfig(ctx).GitHubToken; token != "" {
		c = c.WithAuthToken(token)
	}
	return &Client{c: c}, nil
}

func (c *Client) GetRepos(name string, accountType string) ([]string, error) {
	return c.GetReposContext(context.Background(), name, accountType)
}

func (c *Client) GetReposContext(ctx context.Context, name string, accountType string) ([]string, error) {
	var (
		list []*github.Repository
		err  error
	)
	err = retry.Do(ctx, config.GetRetryConfigContext(ctx), func() error {
		permit, acquireErr := githubapi.Acquire(ctx, "core")
		if acquireErr != nil {
			return acquireErr
		}
		var response *github.Response
		var apiErr error
		if accountType == typedef.TypeOrg {
			list, response, apiErr = c.c.Repositories.ListByOrg(ctx, name, nil)
		} else {
			list, response, apiErr = c.c.Repositories.List(ctx, name, nil)
		}
		permit.Done(githubapi.ObserveREST(response, apiErr))
		return apiErr
	})
	if err != nil {
		return nil, err
	}
	repos := make([]string, 0)
	for _, repo := range list {
		htmlURL := repo.GetHTMLURL()
		URL, err := url.Parse(htmlURL)
		if err != nil {
			return nil, err
		}
		repos = append(repos, URL.Hostname()+URL.Path)
	}
	return repos, nil
}

func (c *Client) GetReleases(ctx context.Context, owner, repo string) ([]*github.RepositoryRelease, error) {
	var (
		list []*github.RepositoryRelease
		err  error
	)
	err = retry.Do(ctx, config.GetRetryConfigContext(ctx), func() error {
		permit, acquireErr := githubapi.Acquire(ctx, "core")
		if acquireErr != nil {
			return acquireErr
		}
		var apiErr error
		var resp *github.Response
		list, resp, apiErr = c.c.Repositories.ListReleases(ctx, owner, repo, nil)
		permit.Done(githubapi.ObserveREST(resp, apiErr))
		return apiErr
	})
	if err != nil {
		return nil, err
	}
	return list, nil
}

func (c *Client) GetReleaseAssets(ctx context.Context, owner, repo string, id int64) ([]*github.ReleaseAsset, error) {
	var (
		list []*github.ReleaseAsset
		err  error
	)
	err = retry.Do(ctx, config.GetRetryConfigContext(ctx), func() error {
		permit, acquireErr := githubapi.Acquire(ctx, "core")
		if acquireErr != nil {
			return acquireErr
		}
		var apiErr error
		var resp *github.Response
		list, resp, apiErr = c.c.Repositories.ListReleaseAssets(ctx, owner, repo, id, nil)
		permit.Done(githubapi.ObserveREST(resp, apiErr))
		return apiErr
	})
	if err != nil {
		return nil, err
	}
	return list, nil
}

func (c *Client) DownloadAsset(ctx context.Context, owner, repo string, id int64) (io.ReadCloser, error) {
	var rc io.ReadCloser
	err := retry.Do(ctx, config.GetRetryConfigContext(ctx), func() error {
		permit, acquireErr := githubapi.Acquire(ctx, "core")
		if acquireErr != nil {
			return acquireErr
		}
		var apiErr error
		rc, _, apiErr = c.c.Repositories.DownloadReleaseAsset(ctx, owner, repo, id, http.DefaultClient)
		permit.Done(githubapi.ObserveError(apiErr))
		return apiErr
	})
	if err != nil {
		return nil, err
	}
	return rc, nil
}
