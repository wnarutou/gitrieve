package discussion

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/leslieleung/reaper/internal/config"
	"github.com/leslieleung/reaper/internal/scm"
	"github.com/leslieleung/reaper/internal/storage"
	"github.com/leslieleung/reaper/internal/typedef"
	"github.com/leslieleung/reaper/internal/ui"
	"github.com/mholt/archiver/v4"
	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"
)

// 定义存储查询结果的结构体
type ReplyData struct {
	DatabaseId int64 `json:"databaseId"`
	Author     struct {
		Login string `json:"login"`
	} `json:"author"`
	Body         string    `json:"body"`
	CreatedAt    time.Time `json:"createdAt"`
	LastEditedAt time.Time `json:"lastEditedAt"`
	IsAnswer     bool      `json:"isAnswer"`
}

type CommentData struct {
	DatabaseId int64 `json:"databaseId"`
	Author     struct {
		Login string `json:"login"`
	} `json:"author"`
	Body         string      `json:"body"`
	CreatedAt    time.Time   `json:"createdAt"`
	LastEditedAt time.Time   `json:"lastEditedAt"`
	IsAnswer     bool        `json:"isAnswer"`
	Replies      []ReplyData `json:"replies"`
}

type DiscussionData struct {
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	Body      string    `json:"body"`
	Title     string    `json:"title"`
	Number    int       `json:"number"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Category  struct {
		Name string `json:"name"`
	} `json:"category"`
	Comments []CommentData `json:"comments"`
}

type RepositoryData struct {
	Discussions []DiscussionData `json:"discussions"`
}

type discussionCommentsRepliesQuery struct {
	Repository struct {
		Discussions struct {
			Nodes []struct {
				Author struct {
					Login string
				}
				Body      string
				Title     string
				Number    int
				CreatedAt time.Time
				UpdatedAt time.Time
				Category  struct {
					Name string
				}
				Comments struct {
					Nodes []struct {
						DatabaseId int64
						Author     struct {
							Login string
						}
						Body         string
						CreatedAt    time.Time
						LastEditedAt time.Time
						IsAnswer     bool
						Replies      struct {
							Nodes []struct {
								DatabaseId int64
								Author     struct {
									Login string
								}
								Body         string
								CreatedAt    time.Time
								LastEditedAt time.Time
								IsAnswer     bool
							}
							PageInfo struct {
								HasNextPage bool
								EndCursor   githubv4.String
							}
						} `graphql:"replies(first: $replyCount, after: $replyCursor)"`
					}
					PageInfo struct {
						HasNextPage bool
						EndCursor   githubv4.String
					}
				} `graphql:"comments(first: $commentCount, after: $commentCursor)"`
			}
			PageInfo struct {
				HasNextPage bool
				EndCursor   githubv4.String
			}
		} `graphql:"discussions(first: $discussionCount, after: $discussionCursor, orderBy: {field: UPDATED_AT, direction: DESC})"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

func Sync(repo typedef.Repository, storages []typedef.MultiStorage) error {
	isUpdated := false
	useCache := repo.UseCache
	currentDir, err := os.Getwd() // 修复：处理 os.Getwd() 错误
	if err != nil {
		ui.Errorf("Error getting current directory: %s", err)
		return err
	}

	var workingDir string
	if useCache {
		workingDir = path.Join(currentDir, ".reaper")
	} else {
		id := uuid.New().String()
		workingDir = path.Join(currentDir, ".reaper", id)
	}

	err = storage.CreateDirIfNotExist(workingDir)
	if err != nil {
		ui.Errorf("Error creating working directory, %s", err)
		return err
	}

	r, err := scm.NewRepository(repo.URL)
	if err != nil {
		return err
	}
	repoName := r.Name
	if repoName == "." || repo.Name == "/" {
		ui.Errorf("Invalid repository name")
		return err
	}

	gitDir := path.Join(workingDir, r.Host, r.Owner, repoName, "discussion")
	err = storage.CreateDirIfNotExist(gitDir)
	if err != nil {
		ui.Errorf("Error creating working directory, %s", err)
		return err
	}

	files, err := os.ReadDir(gitDir)
	if err != nil {
		ui.Errorf("Error reading discussion directory: %s", err)
		return err
	}

	var lastUpdate time.Time
	if len(files) == 0 {
		lastUpdate = time.Unix(0, 0)
		ui.Printf("No discussion downloaded yet, need to download all discussions")
	} else {
		var updateTime time.Time
		for _, file := range files {
			if !strings.HasSuffix(file.Name(), ".md") {
				continue
			}

			content, err := os.ReadFile(path.Join(gitDir, file.Name()))
			if err != nil {
				ui.Errorf("Error reading discussion file: %s", err)
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
		ui.Printf("The latest update time among all discussions is: %s", lastUpdate)
	}

	cfg := config.GetIns()
	src := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: cfg.GitHubToken},
	)
	httpClient := oauth2.NewClient(context.Background(), src)
	client := githubv4.NewClient(httpClient)

	variables := map[string]interface{}{
		"owner":            githubv4.String(r.Owner),
		"name":             githubv4.String(r.Name),
		"discussionCount":  githubv4.Int(10),        // 每次查询 10 个 discussions
		"discussionCursor": (*githubv4.String)(nil), // 初始游标
		"commentCount":     githubv4.Int(10),        // 每次查询 10 个 comments
		"commentCursor":    (*githubv4.String)(nil), // 初始游标
		"replyCount":       githubv4.Int(10),        // 每次查询 10 个 replies
		"replyCursor":      (*githubv4.String)(nil), // 初始游标
	}

	for {
		var query discussionCommentsRepliesQuery
		err := client.Query(context.Background(), &query, variables)
		if err != nil {
			ui.Errorf("Error fetching discussions: %s", err)
			return err
		}

		for _, discussion := range query.Repository.Discussions.Nodes {
			if discussion.UpdatedAt.Before(lastUpdate) {
				continue
			}
			isUpdated = true

			for {
				for _, comment := range discussion.Comments.Nodes {
					for {
						comment.Replies.Nodes = append(comment.Replies.Nodes, comment.Replies.Nodes...)

						if !comment.Replies.PageInfo.HasNextPage {
							break
						}
						variables["replyCursor"] = comment.Replies.PageInfo.EndCursor
						err := client.Query(context.Background(), &query, variables)
						if err != nil {
							ui.Errorf("Error fetching discussions: %s", err)
							return err
						}
					}
					discussion.Comments.Nodes = append(discussion.Comments.Nodes, comment)
				}

				if !discussion.Comments.PageInfo.HasNextPage {
					break
				}
				variables["commentCursor"] = discussion.Comments.PageInfo.EndCursor
				err := client.Query(context.Background(), &query, variables)
				if err != nil {
					ui.Errorf("Error fetching discussions: %s", err)
					return err
				}

			}

			discussionFileName := fmt.Sprintf("%d.md", discussion.Number)
			discussionFilePath := path.Join(gitDir, discussionFileName)

			var content string
			content += fmt.Sprintf("# Discussion: %s\n\n", discussion.Title)
			content += "## Basic Information\n\n"
			content += fmt.Sprintf("- Created Time: %s\n", discussion.CreatedAt.Format("2006-01-02 15:04:05"))
			content += fmt.Sprintf("- Updated Time: %s\n", discussion.UpdatedAt.Format("2006-01-02 15:04:05"))
			content += fmt.Sprintf("- Category: %s\n", discussion.Category.Name)
			content += fmt.Sprintf("- Author: %s\n", discussion.Author.Login)
			content += fmt.Sprintf("- Comment Count: %d\n\n", len(discussion.Comments.Nodes))

			content += "## Content\n\n"
			content += "```\n\n"
			content += discussion.Body + "\n\n"
			content += "```\n\n"

			if len(discussion.Comments.Nodes) > 0 {
				content += "## Comments\n\n"
				for _, comment := range discussion.Comments.Nodes {
					content += fmt.Sprintf("### Comment #%d\n\n", comment.DatabaseId)
					content += "```\n\n"
					content += comment.Body + "\n\n"
					content += "```\n\n"
					content += fmt.Sprintf("- Author: %s\n", comment.Author.Login)
					content += fmt.Sprintf("- Created Time: %s\n", comment.CreatedAt.Format("2006-01-02 15:04:05"))
					content += fmt.Sprintf("- Updated Time: %s\n\n", comment.LastEditedAt.Format("2006-01-02 15:04:05"))
					content += "---\n\n"

					for _, reply := range comment.Replies.Nodes {
						content += fmt.Sprintf("#### Reply #%d\n\n", reply.DatabaseId)
						content += "```\n\n"
						content += reply.Body + "\n\n"
						content += "```\n\n"
						content += fmt.Sprintf("- Author: %s\n", reply.Author.Login)
						content += fmt.Sprintf("- Created Time: %s\n", reply.CreatedAt.Format("2006-01-02 15:04:05"))
						content += fmt.Sprintf("- Updated Time: %s\n\n", reply.LastEditedAt.Format("2006-01-02 15:04:05"))
						content += "---\n\n"
					}
				}
			}

			err = os.WriteFile(discussionFilePath, []byte(content), 0644)
			if err != nil {
				ui.Errorf("Error writing discussion file %s: %s", discussionFilePath, err)
				return err
			}
			ui.Printf("Success writing discussion %s to file %s", discussion.Title, discussionFilePath)
		}

		if !query.Repository.Discussions.PageInfo.HasNextPage {
			break
		}
		variables["discussionCursor"] = query.Repository.Discussions.PageInfo.EndCursor
		err = client.Query(context.Background(), &query, variables)
		if err != nil {
			ui.Errorf("Error fetching discussions: %s", err)
			return err
		}
	}

	if isUpdated {
		err = os.Chdir(path.Dir(gitDir))
		if err != nil {
			ui.Errorf("Error changing directory: %s", err)
		}

		files, err := archiver.FilesFromDisk(nil, map[string]string{
			"discussion": "discussion",
		})
		if err != nil {
			ui.Errorf("Error reading files: %s", err)
			return err
		}

		base := "discussions.tar.gz"
		archive := &bytes.Buffer{}

		format := archiver.CompressedArchive{
			Compression: archiver.Gz{},
			Archival:    archiver.Tar{},
		}
		err = format.Archive(context.Background(), archive, files)
		if err != nil {
			ui.Errorf("Error creating archive: %s", err)
			return err
		}

		err = os.Chdir(currentDir)
		if err != nil {
			ui.Errorf("Error changing directory: %s", err)
		}

		for _, s := range storages {
			backend, err := storage.GetStorage(s)
			if err != nil {
				ui.Errorf("Error getting backend: %s", err)
				return err
			}
			err = backend.PutObject(path.Join(s.Path, r.Host, r.Owner, r.Name, base), archive.Bytes())
			if err != nil {
				ui.Errorf("Error storing file: %s", err)
				return err
			}
			ui.Printf("File %s stored", path.Join(s.Path, r.Host, r.Owner, r.Name, base))
		}
	} else {
		ui.Printf("All is up to date, no need to restore")
	}

	if !useCache {
		err = os.RemoveAll(gitDir)
		if err != nil {
			ui.Errorf("Error cleaning up working directory: %s", err)
			return err
		}
		ui.Printf("Cleanup completed for directory: %s", gitDir)
	}
	return nil
}
