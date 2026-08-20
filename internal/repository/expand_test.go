package repository

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

type fakeRepoLister struct {
	repos []string
	err   error
}

func (f *fakeRepoLister) GetRepos(name, accountType string) ([]string, error) {
	return f.repos, f.err
}

func TestExpand(t *testing.T) {
	old := newGithubClient
	t.Cleanup(func() { newGithubClient = old })
	newGithubClient = func() (repoLister, error) {
		return &fakeRepoLister{repos: []string{"github.com/acme/alpha", "github.com/acme/beta"}}, nil
	}

	t.Run("repo passthrough", func(t *testing.T) {
		repo := typedef.Repository{Name: "solo", URL: "github.com/a/solo", Type: typedef.TypeRepo}
		got := Expand(repo)
		require.Len(t, got, 1)
		assert.Equal(t, "solo", got[0].Name)
		assert.Equal(t, "github.com/a/solo", got[0].URL)
	})

	t.Run("org expands to concrete repos inheriting options", func(t *testing.T) {
		org := typedef.Repository{
			Name: "acme", Type: typedef.TypeOrg, OrgName: "acme",
			Cron: "0 2 * * *", AllBranches: true,
		}
		got := Expand(org)
		require.Len(t, got, 2)
		assert.Equal(t, "alpha", got[0].Name)
		assert.Equal(t, "github.com/acme/alpha", got[0].URL)
		assert.Equal(t, typedef.TypeRepo, got[0].GetType())
		assert.Equal(t, "0 2 * * *", got[0].Cron) // 继承父条目配置
		assert.True(t, got[0].AllBranches)
		assert.Equal(t, "beta", got[1].Name)
	})

	t.Run("invalid type yields nothing", func(t *testing.T) {
		bad := typedef.Repository{Name: "x", URL: "github.com/a/x", Type: "whatever"}
		assert.Empty(t, Expand(bad))
	})
}
