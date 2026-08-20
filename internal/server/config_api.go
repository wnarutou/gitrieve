package server

import (
	"net/http"
	"reflect"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

// secretFields are config values never echoed in plaintext in a diff.
var secretFields = map[string]bool{
	"githubToken":     true,
	"authToken":       true,
	"secretAccessKey": true,
}

// maskSecret returns "***" for a non-empty secret value, the value otherwise.
func maskSecret(field string, v interface{}) interface{} {
	if s, ok := v.(string); ok && secretFields[field] && s != "" {
		return "***"
	}
	return v
}

var repoFields = []struct {
	name string
	get  func(typedef.Repository) interface{}
}{
	{"name", func(r typedef.Repository) interface{} { return r.Name }},
	{"url", func(r typedef.Repository) interface{} { return r.EffectiveURL() }},
	{"cron", func(r typedef.Repository) interface{} { return r.Cron }},
	{"storage", func(r typedef.Repository) interface{} { return r.Storage }},
	{"useCache", func(r typedef.Repository) interface{} { return r.UseCache }},
	{"type", func(r typedef.Repository) interface{} { return r.GetType() }},
	{"orgName", func(r typedef.Repository) interface{} { return r.OrgName }},
	{"allBranches", func(r typedef.Repository) interface{} { return r.AllBranches }},
	{"depth", func(r typedef.Repository) interface{} { return r.Depth }},
	{"downloadReleases", func(r typedef.Repository) interface{} { return r.DownloadReleases }},
	{"downloadIssues", func(r typedef.Repository) interface{} { return r.DownloadIssues }},
	{"downloadWiki", func(r typedef.Repository) interface{} { return r.DownloadWiki }},
	{"downloadDiscussion", func(r typedef.Repository) interface{} { return r.DownloadDiscussion }},
}

var storageFields = []struct {
	name string
	get  func(typedef.MultiStorage) interface{}
}{
	{"name", func(s typedef.MultiStorage) interface{} { return s.Name }},
	{"type", func(s typedef.MultiStorage) interface{} { return s.Type }},
	{"path", func(s typedef.MultiStorage) interface{} { return s.Path }},
	{"endpoint", func(s typedef.MultiStorage) interface{} { return s.Endpoint }},
	{"bucket", func(s typedef.MultiStorage) interface{} { return s.Bucket }},
	{"region", func(s typedef.MultiStorage) interface{} { return s.Region }},
	{"accessKeyID", func(s typedef.MultiStorage) interface{} { return s.AccessKeyID }},
	{"secretAccessKey", func(s typedef.MultiStorage) interface{} { return s.SecretAccessKey }},
}

// stringSetEq compares two string slices as unordered sets (used for the
// storage-backend references, where order is not meaningful).
func stringSetEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]bool, len(a))
	for _, s := range a {
		m[s] = true
	}
	for _, s := range b {
		if !m[s] {
			return false
		}
	}
	return true
}

func valuesEqual(a, b interface{}) bool {
	if as, ok := a.([]string); ok {
		bs, ok2 := b.([]string)
		if !ok2 {
			return false
		}
		return stringSetEq(as, bs)
	}
	return reflect.DeepEqual(a, b)
}

func repoFieldChanges(existing, imported typedef.Repository) []FieldChange {
	var out []FieldChange
	for _, f := range repoFields {
		a, b := f.get(existing), f.get(imported)
		if valuesEqual(a, b) {
			continue
		}
		out = append(out, FieldChange{Field: f.name, Existing: a, Imported: b})
	}
	return out
}

func storageFieldChanges(existing, imported typedef.MultiStorage) []FieldChange {
	var out []FieldChange
	for _, f := range storageFields {
		a, b := f.get(existing), f.get(imported)
		if valuesEqual(a, b) {
			continue
		}
		out = append(out, FieldChange{Field: f.name, Existing: maskSecret(f.name, a), Imported: maskSecret(f.name, b)})
	}
	return out
}

// diffRepositories classifies every imported repository as added / modified /
// absent (identical ones are omitted) and every current repository as deleted.
// Only a subset of a repository's fields drives "modified"; the classification
// is by identity key (normalized URL).
func diffRepositories(current, imported []typedef.Repository) RepoDiff {
	curByKey := map[string]typedef.Repository{}
	for _, r := range current {
		if r.Key() != "" {
			curByKey[r.Key()] = r
		}
	}
	impByKey := map[string]typedef.Repository{}
	for _, r := range imported {
		impByKey[r.Key()] = r
	}

	var diff RepoDiff
	for _, imp := range imported {
		entry := RepoEntry{Key: imp.Key(), Name: imp.Name, URL: imp.EffectiveURL()}
		cur, exists := curByKey[imp.Key()]
		if !exists {
			diff.Added = append(diff.Added, entry)
			continue
		}
		if changes := repoFieldChanges(cur, imp); len(changes) > 0 {
			entry.Changes = changes
			diff.Modified = append(diff.Modified, entry)
		}
	}
	keys := make([]string, 0, len(curByKey))
	for k := range curByKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, ok := impByKey[k]; !ok {
			r := curByKey[k]
			diff.Deleted = append(diff.Deleted, RepoEntry{Key: r.Key(), Name: r.Name, URL: r.EffectiveURL()})
		}
	}
	return diff
}

func diffStorages(current, imported []typedef.MultiStorage) StorageDiff {
	curByName := map[string]typedef.MultiStorage{}
	for _, s := range current {
		curByName[s.Name] = s
	}
	impByName := map[string]typedef.MultiStorage{}
	for _, s := range imported {
		impByName[s.Name] = s
	}

	var diff StorageDiff
	for _, imp := range imported {
		entry := StorageEntry{Name: imp.Name, Type: imp.Type}
		cur, exists := curByName[imp.Name]
		if !exists {
			diff.Added = append(diff.Added, entry)
			continue
		}
		if changes := storageFieldChanges(cur, imp); len(changes) > 0 {
			entry.Changes = changes
			diff.Modified = append(diff.Modified, entry)
		}
	}
	names := make([]string, 0, len(curByName))
	for n := range curByName {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if _, ok := impByName[n]; !ok {
			s := curByName[n]
			diff.Deleted = append(diff.Deleted, StorageEntry{Name: s.Name, Type: s.Type})
		}
	}
	return diff
}

// diffGlobals compares the current config's global options with the imported
// document's, reporting only changed fields.
func diffGlobals(cur *config.Config, imp *config.ExportConfig) []FieldChange {
	pairs := []struct {
		field string
		cur   interface{}
		imp   interface{}
	}{
		{"githubToken", cur.GitHubToken, imp.GitHubToken},
		{"cocurrencyNum", cur.ConcurrencyNum, imp.ConcurrencyNum},
		{"releaseSizeLimit", cur.ReleaseSizeLimit, imp.ReleaseSizeLimit},
		{"releaseNumLimit", cur.ReleaseNumLimit, imp.ReleaseNumLimit},
		{"retryMaxCount", cur.RetryMaxCount, imp.RetryMaxCount},
		{"retryBaseDelay", cur.RetryBaseDelay.String(), time.Duration(imp.RetryBaseDelay).String()},
	}
	var out []FieldChange
	for _, p := range pairs {
		if reflect.DeepEqual(p.cur, p.imp) {
			continue
		}
		out = append(out, FieldChange{Field: p.field, Existing: maskSecret(p.field, p.cur), Imported: maskSecret(p.field, p.imp)})
	}
	return out
}

func diffServer(cur, imp config.ServerSection) []FieldChange {
	pairs := []struct {
		field string
		cur   interface{}
		imp   interface{}
	}{
		{"host", cur.Host, imp.Host},
		{"port", cur.Port, imp.Port},
		{"authEnabled", cur.AuthEnabled, imp.AuthEnabled},
		{"authToken", cur.AuthToken, imp.AuthToken},
		{"dbPath", cur.DbPath, imp.DbPath},
	}
	var out []FieldChange
	for _, p := range pairs {
		if reflect.DeepEqual(p.cur, p.imp) {
			continue
		}
		out = append(out, FieldChange{Field: p.field, Existing: maskSecret(p.field, p.cur), Imported: maskSecret(p.field, p.imp)})
	}
	return out
}

// buildWarnings explains the operational consequences of the changes being
// previewed. The server section is never hot-applied, and changing
// authEnabled/authToken can lock the operator out of the web UI.
func buildWarnings(serverChanges []FieldChange) []string {
	if len(serverChanges) == 0 {
		return nil
	}
	for _, c := range serverChanges {
		if c.Field == "authEnabled" || c.Field == "authToken" {
			return []string{"server.authEnabled / server.authToken 已变更：该改动不会热生效，需重启 server；若忘记令牌，重启后可能无法访问 Web UI。"}
		}
	}
	return []string{"server 段已变更：该改动不会热生效，需重启 server 后生效。"}
}

// ExportConfig returns the full current config as YAML, usable directly as
// config.yaml (secrets in plaintext — the export is the source of truth).
func (a *API) ExportConfig(c *gin.Context) {
	yamlStr, err := config.ExportFrom(a.config)
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{Code: 500, Message: "导出配置失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, Response{Code: 200, Data: gin.H{"yaml": yamlStr}})
}

// PreviewImport parses and validates the submitted config and computes a diff
// against the current config. It never mutates state.
func (a *API) PreviewImport(c *gin.Context) {
	var req ImportPreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, Response{Code: 400, Message: "Invalid request: " + err.Error()})
		return
	}
	doc, err := config.ParseImport(req.Config)
	if err != nil {
		c.JSON(http.StatusBadRequest, Response{Code: 400, Message: err.Error()})
		return
	}
	if errs := config.ValidateImport(doc); len(errs) > 0 {
		c.JSON(http.StatusBadRequest, Response{Code: 400, Message: "导入配置无效", Data: ImportErrorData{Errors: errs}})
		return
	}

	curServer := config.GetServerSection()
	repos := diffRepositories(a.config.Repository, doc.Repository)
	storages := diffStorages(a.config.Storage, doc.Storage)
	globals := diffGlobals(a.config, doc)
	serverChanges := diffServer(curServer, doc.Server)

	data := ImportPreviewData{
		Summary: ImportSummary{
			Repositories: CountSummary{Added: len(repos.Added), Deleted: len(repos.Deleted), Modified: len(repos.Modified)},
			Storages:     CountSummary{Added: len(storages.Added), Deleted: len(storages.Deleted), Modified: len(storages.Modified)},
			Globals:      ChangedCount{Changed: len(globals)},
			Server:       ChangedCount{Changed: len(serverChanges)},
		},
		Repositories: repos,
		Storages:     storages,
		Globals:      globals,
		Server:       serverChanges,
		Warnings:     buildWarnings(serverChanges),
	}
	c.JSON(http.StatusOK, Response{Code: 200, Data: data})
}
