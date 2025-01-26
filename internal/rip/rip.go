package rip

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/uuid"
	"github.com/leslieleung/reaper/internal/storage"
	"github.com/leslieleung/reaper/internal/typedef"
	"github.com/leslieleung/reaper/internal/ui"
	"github.com/mholt/archiver/v4"

	"time"
)

func Rip(repo typedef.Repository, storages []typedef.MultiStorage) error {
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
	repoName := path.Base(repo.URL)
	// check if repo name is valid
	if repoName == "." || repo.Name == "/" {
		ui.Errorf("Invalid repository name")
		return err
	}
	gitDir := path.Join(workingDir, repoName)
	var exist bool
	// check if the repo already exists
	if _, err := os.Stat(path.Join(gitDir, ".git")); err == nil {
		exist = true
	}
	var gitRepo *git.Repository
	// clone the repo if it does not exist, otherwise pull
	if !exist {
		gitRepo, err = git.PlainClone(gitDir, false, &git.CloneOptions{
			URL:      "https://" + repo.URL,
			Progress: os.Stdout,
		})

		if err != nil {
			ui.Errorf("Error cloning repository, %s", err)
			return err
		}
		// 获取所有远程分支
		remote, err := gitRepo.Remote("origin")
		if err != nil {
			log.Fatalf("Failed to get remote: %v", err)
		}

		refs, err := remote.List(&git.ListOptions{})
		if err != nil {
			log.Fatalf("Failed to list references: %v", err)
		}

		// 遍历所有分支并检出
		for _, ref := range refs {
			if ref.Name().IsBranch() {
				branchName := ref.Name().Short()
				fmt.Printf("Checking out branch: %s\n", branchName)

				wt, err := gitRepo.Worktree()
				if err != nil {
					log.Fatalf("Failed to get worktree: %v", err)
				}

				err = wt.Checkout(&git.CheckoutOptions{
					Branch: plumbing.ReferenceName(fmt.Sprintf("refs/remotes/origin/%s", branchName)),
					Force:  true,
				})
				if err != nil {
					log.Printf("Failed to checkout branch %s: %v", branchName, err)
				}
			}
		}

		fmt.Println("All branches have been checked out.")

		ui.Printf("Repository %s cloned", repo.Name)
	} else {
		r, err := git.PlainOpen(gitDir)
		if err != nil {
			ui.Errorf("Error opening repository, %s", err)
			return err
		}
		w, err := r.Worktree()
		if err != nil {
			ui.Errorf("Error getting worktree, %s", err)
			return err
		}
		err = w.Pull(&git.PullOptions{
			RemoteName: "origin",
			Progress:   os.Stdout,
		})
		if err != nil {
			if errors.Is(err, git.NoErrAlreadyUpToDate) {
				ui.Printf("Repository %s already up to date", repo.Name)
				return nil
			}
			ui.Errorf("Error pulling repository, %s", err)
			return err
		}
		ui.Printf("Repository %s pulled", repo.Name)
	}

	// change directory to the parent directory of the repo
	err = os.Chdir(workingDir)
	if err != nil {
		ui.Errorf("Error changing directory, %s", err)
	}

	files, err := archiver.FilesFromDisk(nil, map[string]string{
		repoName: repo.Name,
	})
	if err != nil {
		ui.Errorf("Error reading files, %s", err)
		return err
	}

	now := time.Now().Format("20060102150405")
	base := repo.Name + "-" + now + ".tar.gz"
	// TODO store to a temporary file first if greater than certain size
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

	// handle storages
	for _, s := range storages {
		backend, err := storage.GetStorage(s)
		if err != nil {
			ui.Errorf("Error getting backend, %s", err)
			return err
		}
		err = backend.PutObject(path.Join(s.Path, base), archive.Bytes())
		if err != nil {
			ui.Errorf("Error storing file, %s", err)
			return err
		}
		ui.Printf("File %s stored", path.Join(s.Path, base))
	}

	// cleanup
	if !useCache {
		err = os.RemoveAll(workingDir)
		if err != nil {
			ui.Errorf("Error cleaning up working directory, %s", err)
			return err
		}
	}
	return nil
}
