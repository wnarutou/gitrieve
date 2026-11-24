package recovery

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/wnarutou/gitrieve/internal/cache"
	"github.com/wnarutou/gitrieve/internal/scm"
)

type ComponentBackup struct {
	Component cache.Component `json:"component"`
	Archive   string          `json:"archive"`
	Present   bool            `json:"present"`
}

type Repository struct {
	Key        string            `json:"key"`
	Host       string            `json:"host"`
	Owner      string            `json:"owner"`
	Name       string            `json:"name"`
	Components []ComponentBackup `json:"components"`
	Release    bool              `json:"release"`
}

type ScanResult struct {
	Root              string       `json:"root"`
	Repositories      []Repository `json:"repositories"`
	TotalRepositories int          `json:"total_repositories"`
	TotalComponents   int          `json:"total_components"`
	PresentComponents int          `json:"present_components"`
	Warnings          []string     `json:"warnings"`
}

// Scan discovers restorable repositories directly from a file-storage tree.
// It deliberately does not consult configured repositories or execution
// history, so a backup can be recovered into a fresh database.
func Scan(root, currentDir string) (ScanResult, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return ScanResult{}, fmt.Errorf("resolve backup root: %w", err)
	}
	info, err := os.Stat(absoluteRoot)
	if err != nil {
		return ScanResult{}, fmt.Errorf("open backup root %s: %w", absoluteRoot, err)
	}
	if !info.IsDir() {
		return ScanResult{}, fmt.Errorf("backup root %s is not a directory", absoluteRoot)
	}

	result := ScanResult{
		Root:         absoluteRoot,
		Repositories: make([]Repository, 0),
		Warnings:     make([]string, 0),
	}
	hosts, err := readDirectories(absoluteRoot)
	if err != nil {
		return ScanResult{}, err
	}
	for _, host := range hosts {
		owners, err := readDirectories(filepath.Join(absoluteRoot, host))
		if err != nil {
			result.Warnings = append(result.Warnings, err.Error())
			continue
		}
		for _, owner := range owners {
			names, err := readDirectories(filepath.Join(absoluteRoot, host, owner))
			if err != nil {
				result.Warnings = append(result.Warnings, err.Error())
				continue
			}
			for _, name := range names {
				repoDir := filepath.Join(absoluteRoot, host, owner, name)
				discovered, warning := scanRepository(repoDir, host, owner, name, currentDir)
				if warning != "" {
					result.Warnings = append(result.Warnings, warning)
				}
				if discovered == nil {
					continue
				}
				result.Repositories = append(result.Repositories, *discovered)
				result.TotalComponents += len(discovered.Components)
				for _, component := range discovered.Components {
					if component.Present {
						result.PresentComponents++
					}
				}
			}
		}
	}

	sort.Slice(result.Repositories, func(i, j int) bool {
		return result.Repositories[i].Key < result.Repositories[j].Key
	})
	result.TotalRepositories = len(result.Repositories)
	return result, nil
}

func readDirectories(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read backup directory %s: %w", root, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

func scanRepository(repoDir, host, owner, name, currentDir string) (*Repository, string) {
	components := []struct {
		component cache.Component
		archive   string
	}{
		{component: cache.ComponentCode, archive: name + ".tar.gz"},
		{component: cache.ComponentWiki, archive: name + "_wiki.tar.gz"},
		{component: cache.ComponentIssues, archive: "issues.tar.gz"},
		{component: cache.ComponentDiscussion, archive: "discussions.tar.gz"},
	}
	repository := &Repository{
		Key:        host + "/" + owner + "/" + name,
		Host:       host,
		Owner:      owner,
		Name:       name,
		Components: make([]ComponentBackup, 0, len(components)),
	}
	r := &scm.Repository{Host: host, Owner: owner, Name: name}
	for _, candidate := range components {
		info, err := os.Stat(filepath.Join(repoDir, candidate.archive))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Sprintf("inspect %s: %v", filepath.Join(repoDir, candidate.archive), err)
		}
		if info.IsDir() {
			continue
		}
		repository.Components = append(repository.Components, ComponentBackup{
			Component: candidate.component,
			Archive:   candidate.archive,
			Present:   cache.Present(currentDir, r, candidate.component),
		})
	}

	if release, err := os.Stat(filepath.Join(repoDir, "release")); err == nil && release.IsDir() {
		repository.Release = true
	}
	if len(repository.Components) == 0 {
		return nil, fmt.Sprintf("skipped %s: no restorable cache archives", repository.Key)
	}
	return repository, ""
}
