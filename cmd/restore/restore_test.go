package restore

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/cache"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

func TestCacheComponentsFollowEnabledMetadataOptions(t *testing.T) {
	repo := typedef.Repository{
		DownloadWiki:       true,
		DownloadIssues:     true,
		DownloadDiscussion: true,
		DownloadReleases:   true,
	}

	require.Equal(t, []cache.Component{
		cache.ComponentCode,
		cache.ComponentWiki,
		cache.ComponentIssues,
		cache.ComponentDiscussion,
	}, cacheComponents(repo))

	require.Equal(t, []cache.Component{cache.ComponentCode}, cacheComponents(typedef.Repository{}))
}

func TestCacheComponentLockKeysMatchSyncComponents(t *testing.T) {
	tests := map[cache.Component]string{
		cache.ComponentCode:       "code",
		cache.ComponentWiki:       "wiki",
		cache.ComponentIssues:     "issue",
		cache.ComponentDiscussion: "discussion",
	}
	for component, want := range tests {
		require.Equal(t, want, lockComponent(component), "component %s", component)
	}
}
