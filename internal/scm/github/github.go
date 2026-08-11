package github

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"sync"

	"github.com/google/go-github/v56/github"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/retry"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

type Client struct {
	c *github.Client
}

var once sync.Once
var client *Client

func New() (*Client, error) {
	once.Do(func() {
		cfg := config.GetIns()
		client = &Client{
			c: github.NewClient(nil).WithAuthToken(cfg.GitHubToken),
		}
		if cfg.GitHubToken != "" {
			client.c = client.c.WithAuthToken(cfg.GitHubToken)
		}
	})
	return client, nil
}

func (c *Client) GetRepos(name string, accountType string) ([]string, error) {
	var (
		list []*github.Repository
		err  error
	)
	if accountType == typedef.TypeOrg {
		list, _, err = c.c.Repositories.ListByOrg(context.Background(), name, nil)
	} else {
		list, _, err = c.c.Repositories.List(context.Background(), name, nil)
	}
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
	err = retry.Do(ctx, config.GetRetryConfig(), func() error {
		var apiErr error
		list, _, apiErr = c.c.Repositories.ListReleases(ctx, owner, repo, nil)
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
	err = retry.Do(ctx, config.GetRetryConfig(), func() error {
		var apiErr error
		list, _, apiErr = c.c.Repositories.ListReleaseAssets(ctx, owner, repo, id, nil)
		return apiErr
	})
	if err != nil {
		return nil, err
	}
	return list, nil
}

func (c *Client) DownloadAsset(ctx context.Context, owner, repo string, id int64) (io.ReadCloser, error) {
	var rc io.ReadCloser
	err := retry.Do(ctx, config.GetRetryConfig(), func() error {
		var apiErr error
		rc, _, apiErr = c.c.Repositories.DownloadReleaseAsset(ctx, owner, repo, id, http.DefaultClient)
		return apiErr
	})
	if err != nil {
		return nil, err
	}
	return rc, nil
}
