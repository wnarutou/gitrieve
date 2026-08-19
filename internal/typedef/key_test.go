package typedef

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"github.com/wnarutou/gitrieve", "github.com/wnarutou/gitrieve"},
		{"https://GITHUB.com/wnarutou/gitrieve/", "github.com/wnarutou/gitrieve"},
		{"http://github.com/wnarutou/gitrieve.git", "github.com/wnarutou/gitrieve"},
		{"https://www.github.com/wnarutou/gitrieve", "github.com/wnarutou/gitrieve"},
		{"www.github.com/wnarutou/gitrieve", "github.com/wnarutou/gitrieve"},
		{"https://github.com/wnarutou/gitrieve#readme", "github.com/wnarutou/gitrieve"},
		{"https://gitlab.com/Wnarutou/proj.git/", "gitlab.com/wnarutou/proj"},
		{"HTTPS://GitHub.com/Foo/Bar.Git", "github.com/foo/bar"},
		{"   https://github.com/foo/bar  ", "github.com/foo/bar"},
		{"", ""},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, NormalizeURL(c.in), "NormalizeURL(%q)", c.in)
	}
}

func TestEffectiveURL(t *testing.T) {
	cases := []struct {
		repo Repository
		want string
	}{
		{Repository{Type: TypeRepo, URL: "github.com/a/b"}, "github.com/a/b"},
		{Repository{Type: TypeOrg, OrgName: "acme"}, "https://github.com/acme"},
		{Repository{Type: TypeUser, OrgName: "alice"}, "https://github.com/alice"},
		// 显式 URL 优先于合成
		{Repository{Type: TypeOrg, URL: "gitlab.com/acme/org", OrgName: "acme"}, "gitlab.com/acme/org"},
		// orgName 为空 → 无有效 URL
		{Repository{Type: TypeOrg}, ""},
		// 非 user/org 且无 URL → 无有效 URL
		{Repository{Type: TypeRepo}, ""},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, c.repo.EffectiveURL(), "EffectiveURL(%+v)", c.repo)
	}
}

func TestKeyAndMatches(t *testing.T) {
	repo := Repository{Name: "x", URL: "https://GitHub.com/Foo/Bar.git"}
	assert.Equal(t, "github.com/foo/bar", repo.Key())

	assert.True(t, repo.Matches("github.com/foo/bar"))
	assert.True(t, repo.Matches("https://github.com/foo/bar"))
	assert.True(t, repo.Matches("HTTPS://GITHUB.COM/FOO/BAR.GIT"))
	assert.True(t, repo.Matches("github.com/foo/bar/"))
	assert.False(t, repo.Matches("github.com/other/bar"))
	assert.False(t, repo.Matches(""))

	// 空身份条目谁也不匹配
	empty := Repository{Name: "orphan"}
	assert.Equal(t, "", empty.Key())
	assert.False(t, empty.Matches("github.com/foo/bar"))
	assert.False(t, empty.Matches(""))
}
