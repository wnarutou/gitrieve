package wiki

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	gh "github.com/google/go-github/v56/github"
	"github.com/google/uuid"
	"github.com/leslieleung/reaper/internal/config"
	"github.com/leslieleung/reaper/internal/scm"
	"github.com/leslieleung/reaper/internal/storage"
	"github.com/leslieleung/reaper/internal/typedef"
	"github.com/leslieleung/reaper/internal/ui"
	"github.com/mholt/archiver/v4"
)

func Sync(repo typedef.Repository, storages []typedef.MultiStorage) error {
	isUpdated := false
	useCache := repo.UseCache
	// get current directory
	currentDir, _ := os.Getwd()

	var workingDir string
	if useCache {
		workingDir = path.Join(currentDir, ".reaper")
	} else {
		id := uuid.New().String()
		workingDir = path.Join(currentDir, ".reaper", id)
	}

	// create a working directory if not exist
	err := storage.CreateDirIfNotExist(workingDir)
	if err != nil {
		ui.Errorf("Error creating working directory, %s", err)
		return err
	}

	// get the repo name from the URL
	r, err := scm.NewRepository(repo.URL)
	if err != nil {
		return err
	}
	repoName := r.Name
	if repoName == "." || repo.Name == "/" {
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

	wikiDir := path.Join(workingDir, r.Host, r.Owner, repoName, "wiki")
	err = storage.CreateDirIfNotExist(wikiDir)
	if err != nil {
		ui.Errorf("Error creating wiki directory, %s", err)
		return err
	}

	// Get all wiki files in the wikiDir directory
	files, err := os.ReadDir(wikiDir)
	if err != nil {
		ui.Errorf("Error reading wiki directory: %s", err)
		return err
	}

	var lastUpdate time.Time
	if len(files) == 0 {
		lastUpdate = time.Unix(0, 0)
		ui.Printf("No wiki pages downloaded yet, need to download all pages")
	} else {
		// Traverse all wiki files to get the latest update time
		var updateTime time.Time
		for _, file := range files {
			if !strings.HasSuffix(file.Name(), ".md") {
				continue
			}

			content, err := os.ReadFile(path.Join(wikiDir, file.Name()))
			if err != nil {
				ui.Errorf("Error reading wiki file: %s", err)
				return err
			}

			lines := strings.Split(string(content), "\n")
			for _, line := range lines {
				if strings.HasPrefix(line, "- Updated Time: ") {
					timeStr := strings.TrimPrefix(line, "- Updated Time: ")
					timeStr = strings.TrimSpace(timeStr)
					updateTime, err = time.Parse("2006-01-02 15:04:05", timeStr)
					if err != nil {
						continue
					}
					break
				}
			}

			if updateTime.After(lastUpdate) {
				lastUpdate = updateTime
			}
		}
		ui.Printf("The latest update time among all wiki pages is: %s", lastUpdate)
	}

	pages, _, err := client.Repositories.GetWikiPages(context.Background(), r.Owner, r.Name)
	if err != nil {
		ui.Errorf("Error fetching wiki pages, %s", err)
		return err
	}

	for _, page := range pages {
		// Get page content
		content, _, err := client.Repositories.GetWikiPage(context.Background(), r.Owner, r.Name, page.GetPageName())
		if err != nil {
			ui.Errorf("Error fetching wiki page %s, %s", page.GetPageName(), err)
			continue
		}

		updateTime, err := time.Parse(time.RFC3339, page.GetUpdatedAt().Format(time.RFC3339))
		if err != nil {
			ui.Errorf("Error parsing update time for page %s, %s", page.GetPageName(), err)
			continue
		}

		// Only process pages that have been updated since last sync
		if updateTime.After(lastUpdate) {
			isUpdated = true
			// Create wiki file
			wikiFileName := fmt.Sprintf("%s.md", page.GetPageName())
			wikiFilePath := path.Join(wikiDir, wikiFileName)

			// Generate markdown content
			var fileContent string
			fileContent += fmt.Sprintf("# %s\n\n", page.GetTitle())
			fileContent += "## Page Information\n\n"
			fileContent += fmt.Sprintf("- Created Time: %s\n", page.GetCreatedAt().Format("2006-01-02 15:04:05"))
			fileContent += fmt.Sprintf("- Updated Time: %s\n", page.GetUpdatedAt().Format("2006-01-02 15:04:05"))
			fileContent += fmt.Sprintf("- SHA: %s\n\n", page.GetSHA())
			fileContent += "## Content\n\n"
			fileContent += content.GetContent()

			// Write to file
			err = os.WriteFile(wikiFilePath, []byte(fileContent), 0644)
			if err != nil {
				ui.Errorf("Error writing wiki file %s, %s", wikiFilePath, err)
				return err
			}
			ui.Printf("Success writing wiki page %s to file %s", page.GetPageName(), wikiFilePath)
		}
	}

	if isUpdated {
		// Change directory to the parent directory of the repo
		err = os.Chdir(path.Dir(wikiDir))
		if err != nil {
			ui.Errorf("Error changing directory, %s", err)
		}

		files, err := archiver.FilesFromDisk(nil, map[string]string{
			"wiki": "wiki",
		})
		if err != nil {
			ui.Errorf("Error reading files, %s", err)
			return err
		}

		base := "wiki.tar.gz"
		archive := &bytes.Buffer{}

		format := archiver.CompressedArchive{
			Compression: archiver.Gz{},
			Archival:    archiver.Tar{},
		}
		err = format.Archive(context.Background(), archive, files)
		if err != nil {
			ui.Errorf("Error creating archive, %s", err)
			return err
		}

		// change to current dir
		err = os.Chdir(currentDir)
		if err != nil {
			ui.Errorf("Error changing directory, %s", err)
		}

		// Handle storages
		for _, s := range storages {
			backend, err := storage.GetStorage(s)
			if err != nil {
				ui.Errorf("Error getting backend, %s", err)
				return err
			}
			err = backend.PutObject(path.Join(s.Path, r.Host, r.Owner, r.Name, base), archive.Bytes())
			if err != nil {
				ui.Errorf("Error storing file, %s", err)
				return err
			}
			ui.Printf("File %s stored", path.Join(s.Path, r.Host, r.Owner, r.Name, base))
		}
	} else {
		ui.Printf("All wiki pages are up to date, no need to update")
	}

	// Cleanup
	if !useCache {
		err = os.RemoveAll(wikiDir)
		if err != nil {
			ui.Errorf("Error cleaning up working directory, %s", err)
			return err
		}
		ui.Printf("Cleanup completed for directory: %s", wikiDir)
	}
	return nil
}
