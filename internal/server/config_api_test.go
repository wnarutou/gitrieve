package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/db"
	server "github.com/wnarutou/gitrieve/internal/server"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

func getJSON(t *testing.T, s *server.TestServer, method, path, body string) (int, map[string]interface{}) {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, bytes.NewBufferString(body))
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return w.Code, resp
}

func TestExportConfigYAML(t *testing.T) {
	loadTempConfig(t, "server:\n  host: 127.0.0.1\n  port: \"8081\"\n")
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()
	cfg := &config.Config{
		Repository:     []typedef.Repository{{Name: "test-repo", URL: "github.com/test/repo"}},
		Storage:        []typedef.MultiStorage{{Storage: typedef.Storage{Name: "local", Type: "file", Path: "/tmp"}}},
		GitHubToken:    "tok",
		RetryBaseDelay: 5 * time.Second,
	}
	s := server.NewConfigTestServer(cfg, testDB)

	code, resp := getJSON(t, s, http.MethodGet, "/api/config/export", "")
	require.Equal(t, 200, code)
	data := resp["data"].(map[string]interface{})
	yamlStr := data["yaml"].(string)
	require.Contains(t, yamlStr, "repository:")
	require.Contains(t, yamlStr, "url: github.com/test/repo")
	require.Contains(t, yamlStr, "githubToken: tok")
	require.Contains(t, yamlStr, "retryBaseDelay: 5s")
	require.Contains(t, yamlStr, "server:")
	require.Contains(t, yamlStr, "host: 127.0.0.1")
}

func TestPreviewImportValidationErrors(t *testing.T) {
	loadTempConfig(t, "")
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()
	cfg := &config.Config{
		Repository: []typedef.Repository{{Name: "test-repo", URL: "github.com/test/repo"}},
	}
	s := server.NewConfigTestServer(cfg, testDB)

	// Invalid YAML -> 400.
	code, _ := getJSON(t, s, http.MethodPost, "/api/config/import/preview", `{"config":"repository: [unclosed\n"}`)
	require.Equal(t, 400, code)

	// Identityless repository -> 400 with a list of violations.
	code, resp := getJSON(t, s, http.MethodPost, "/api/config/import/preview", `{"config":"repository:\n  - name: orphan\n"}`)
	require.Equal(t, 400, code)
	errors := resp["data"].(map[string]interface{})["errors"].([]interface{})
	require.Len(t, errors, 1)
	require.Contains(t, errors[0], "orphan")
}

func TestPreviewImportDiff(t *testing.T) {
	loadTempConfig(t, "")
	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	defer testDB.Close()
	cfg := &config.Config{
		Repository: []typedef.Repository{
			{Name: "mod-test", URL: "github.com/test/mod", Cron: "@daily"},
			{Name: "del-test", URL: "github.com/test/del"},
		},
		Storage: []typedef.MultiStorage{
			{Storage: typedef.Storage{Name: "st1", Type: "file", Path: "/tmp/one"}},
		},
		// Globals already at the seeded defaults except GitHubToken and
		// RetryBaseDelay, so the globals diff is exactly those two fields.
		GitHubToken:      "tok-existing",
		ConcurrencyNum:   3,
		ReleaseSizeLimit: 300000000,
		ReleaseNumLimit:  3,
		RetryMaxCount:    3,
	}
	s := server.NewConfigTestServer(cfg, testDB)

	importYAML := `repository:
  - name: mod-test
    url: github.com/test/mod
  - name: add-test
    url: github.com/test/added
storage:
  - name: st1
    type: file
    path: /tmp/two
githubToken: tok-import
retryBaseDelay: "5s"
server:
  port: "9090"
`
	body, _ := json.Marshal(map[string]string{"config": importYAML})
	code, resp := getJSON(t, s, http.MethodPost, "/api/config/import/preview", string(body))
	require.Equal(t, 200, code)
	data := resp["data"].(map[string]interface{})

	summary := data["summary"].(map[string]interface{})
	repos := summary["repositories"].(map[string]interface{})
	require.Equal(t, float64(1), repos["added"])
	require.Equal(t, float64(1), repos["deleted"])
	require.Equal(t, float64(1), repos["modified"])
	storages := summary["storages"].(map[string]interface{})
	require.Equal(t, float64(0), storages["added"])
	require.Equal(t, float64(0), storages["deleted"])
	require.Equal(t, float64(1), storages["modified"])
	require.Equal(t, float64(2), summary["globals"].(map[string]interface{})["changed"])
	require.Equal(t, float64(1), summary["server"].(map[string]interface{})["changed"])

	repoDiff := data["repositories"].(map[string]interface{})
	modified := repoDiff["modified"].([]interface{})
	require.Len(t, modified, 1)
	m := modified[0].(map[string]interface{})
	require.Equal(t, "github.com/test/mod", m["key"])
	changes := m["changes"].([]interface{})
	require.Len(t, changes, 1)
	c := changes[0].(map[string]interface{})
	require.Equal(t, "cron", c["field"])
	require.Equal(t, "@daily", c["existing"])
	require.Equal(t, "", c["imported"])

	deleted := repoDiff["deleted"].([]interface{})
	require.Len(t, deleted, 1)
	require.Equal(t, "github.com/test/del", deleted[0].(map[string]interface{})["key"])

	added := repoDiff["added"].([]interface{})
	require.Len(t, added, 1)
	require.Equal(t, "github.com/test/added", added[0].(map[string]interface{})["key"])

	stDiff := data["storages"].(map[string]interface{})
	stModified := stDiff["modified"].([]interface{})
	require.Len(t, stModified, 1)
	sm := stModified[0].(map[string]interface{})
	require.Equal(t, "st1", sm["name"])
	smChanges := sm["changes"].([]interface{})
	require.Len(t, smChanges, 1)
	require.Equal(t, "path", smChanges[0].(map[string]interface{})["field"])

	// githubToken is masked, never echoed in plaintext.
	globals := data["globals"].([]interface{})
	var tokenChange map[string]interface{}
	for _, g := range globals {
		if g.(map[string]interface{})["field"] == "githubToken" {
			tokenChange = g.(map[string]interface{})
		}
	}
	require.NotNil(t, tokenChange)
	require.Equal(t, "***", tokenChange["existing"])
	require.Equal(t, "***", tokenChange["imported"])

	// retryBaseDelay is compared in its string form ("0s" vs "5s"), not as
	// raw nanoseconds.
	var delayChange map[string]interface{}
	for _, g := range globals {
		if g.(map[string]interface{})["field"] == "retryBaseDelay" {
			delayChange = g.(map[string]interface{})
		}
	}
	require.NotNil(t, delayChange)
	require.Equal(t, "0s", delayChange["existing"])
	require.Equal(t, "5s", delayChange["imported"])

	// Server section changed -> warning present.
	warnings := data["warnings"].([]interface{})
	require.Len(t, warnings, 1)
}
