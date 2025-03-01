package issue

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
	// check if repo name is valid
	if repoName == "." || repo.Name == "/" {
		ui.Errorf("Invalid repository name")
		return err
	}
	gitDir := path.Join(workingDir, r.Host, r.Owner, repoName, "issues")
	err = storage.CreateDirIfNotExist(gitDir)
	if err != nil {
		ui.Errorf("Error creating working directory, %s", err)
		return err
	}
	// Get all issue files in the gitDir directory
	files, err := os.ReadDir(gitDir)
	if err != nil {
		ui.Errorf("Error reading issue directory: %s", err)
		return err
	}

	var lastUpdate time.Time
	if len(files) == 0 {
		// If the directory is empty, set to Unix epoch time
		lastUpdate = time.Unix(0, 0)
		ui.Printf("No issues downloaded yet, need to download all issues")
	} else {
		// Traverse all issue files to get the latest update time
		var updateTime time.Time
		for _, file := range files {
			if !strings.HasSuffix(file.Name(), ".md") {
				continue
			}

			content, err := os.ReadFile(path.Join(gitDir, file.Name()))
			if err != nil {
				ui.Errorf("Error reading issue file: %s", err)
				return err
			}

			// Parse the markdown file content to get the update time
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
		ui.Printf("The latest update time among all issues is: %s", lastUpdate)
	}

	// Set query parameters to only get issues updated since the last sync
	opt := &gh.IssueListByRepoOptions{
		State:     "all",
		Since:     lastUpdate.Add(time.Second), // Add 1 nanosecond to make it greater than instead of greater than or equal to
		Sort:      "updated",
		Direction: "asc", // Sort by update time in descending order
		ListOptions: gh.ListOptions{
			PerPage: 100,
		},
	}

	cfg := config.GetIns()
	client := gh.NewClient(nil).WithAuthToken(cfg.GitHubToken)
	for {
		issues, resp, err := client.Issues.ListByRepo(context.Background(), r.Owner, r.Name, opt)
		if err != nil {
			ui.Errorf("Error fetching issues, %s", err)
			return err
		}
		ui.Printf("Fetching page %d, total %d issues", opt.Page, len(issues))

		// Verified that for each issue, if the issue or any comment under it is updated, the issue's update time will be updated
		// Traverse all issues
		for _, issue := range issues {
			isUpdated = true
			// Get all comments under the issue
			commentsOpt := &gh.IssueListCommentsOptions{
				ListOptions: gh.ListOptions{
					PerPage: 100,
				},
			}
			var allComments []*gh.IssueComment
			for {
				comments, resp, err := client.Issues.ListComments(context.Background(), r.Owner, r.Name, issue.GetNumber(), commentsOpt)
				if err != nil {
					ui.Errorf("Error fetching comments of issue %d, %s", issue.GetNumber(), err)
					return err
				}
				allComments = append(allComments, comments...)

				if resp.NextPage == 0 {
					break
				}
				commentsOpt.Page = resp.NextPage
			}

			// Create issue file
			issueFileName := fmt.Sprintf("#%d.md", issue.GetNumber())
			issueFilePath := path.Join(gitDir, issueFileName)

			// Generate markdown content
			var content string
			content += fmt.Sprintf("# Issue #%d: %s\n\n", issue.GetNumber(), issue.GetTitle())
			content += "## Basic Information\n\n"
			content += fmt.Sprintf("- Created Time: %s\n", issue.GetCreatedAt().Format("2006-01-02 15:04:05"))
			content += fmt.Sprintf("- Updated Time: %s\n", issue.GetUpdatedAt().Format("2006-01-02 15:04:05"))
			content += fmt.Sprintf("- State: %s\n", issue.GetState())
			content += fmt.Sprintf("- Author: %s\n", issue.GetUser().GetLogin())
			content += fmt.Sprintf("- Comment Count: %d\n\n", len(allComments))

			content += "## Content\n\n"
			content += issue.GetBody() + "\n\n"

			if len(allComments) > 0 {
				content += "## Comments\n\n"
				for _, comment := range allComments {
					content += fmt.Sprintf("### Comment #%d\n\n", comment.GetID())
					content += fmt.Sprintf("- Author: %s\n", comment.GetUser().GetLogin())
					content += fmt.Sprintf("- Created Time: %s\n", comment.GetCreatedAt().Format("2006-01-02 15:04:05"))
					content += fmt.Sprintf("- Updated Time: %s\n\n", comment.GetUpdatedAt().Format("2006-01-02 15:04:05"))
					content += fmt.Sprintf("- Content: \n\n")
					content += comment.GetBody() + "\n\n"
					content += "---\n\n"
				}
			}

			// Write to file
			err = os.WriteFile(issueFilePath, []byte(content), 0644)
			if err != nil {
				ui.Errorf("Error writing issue file %s, %s", issueFilePath, err)
				return err
			}
			ui.Printf("Success writing issue #%d to file %s", issue.GetNumber(), issueFilePath)
		}

		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}

	if isUpdated {
		// Change directory to the parent directory of the repo
		err = os.Chdir(path.Dir(gitDir))
		if err != nil {
			ui.Errorf("Error changing directory, %s", err)
		}

		files, err := archiver.FilesFromDisk(nil, map[string]string{
			"issues": "issues",
		})
		if err != nil {
			ui.Errorf("Error reading files, %s", err)
			return err
		}

		base := "issues.tar.gz"
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
		ui.Printf("All is up to date, no need to restore")
	}

	// Cleanup
	if !useCache {
		err = os.RemoveAll(gitDir)
		if err != nil {
			ui.Errorf("Error cleaning up working directory, %s", err)
			return err
		}
		ui.Printf("Cleanup completed for directory: %s", gitDir)
	}
	return nil
}
