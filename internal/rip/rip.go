package rip

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
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
		_, err = git.PlainClone(gitDir, false, &git.CloneOptions{
			URL:      "https://" + repo.URL,
			Progress: os.Stdout,
		})

		if err != nil {
			ui.Errorf("Error cloning repository, %s", err)
			return err
		}
	}

	// open local repo
	gitRepo, err = git.PlainOpen(gitDir)
	if err != nil {
		ui.Errorf("Error opening repository, %s", err)
		return err
	}

	// fetch all remote branches
	err = gitRepo.Fetch(&git.FetchOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{config.RefSpec("+refs/heads/*:refs/remotes/origin/*")},
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		ui.Errorf("Error fetching remote branches, %s", err)
		return err
	}

	// get remote references
	refs, err := gitRepo.References()
	if err != nil {
		ui.Errorf("Error get remote references, %s", err)
		return err
	}

	// get worktree
	w, err := gitRepo.Worktree()
	if err != nil {
		ui.Errorf("Error get worktree, %s", err)
		return err
	}

	// find all remote branches
	refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Name().IsRemote() {
			// get remote branch name
			remoteBranchName := ref.Name().Short()

			// set local branch name
			localBranchName := remoteBranchName[len("origin/"):]

			// create local branch reference
			branchRef := plumbing.NewBranchReferenceName(localBranchName)

			// check local branch exist or not
			_, err := gitRepo.Reference(branchRef, false)

			// if local branch not exist
			if err == plumbing.ErrReferenceNotFound {
				// create local branch and switch to the new local branch
				err = w.Checkout(&git.CheckoutOptions{
					Branch: branchRef,
					Create: true,
					Force:  true,
					// create local branch basing on ref.Hash(), that means head of remote branch,
					//   to avoid content of this new branch is a copy of last local branch,
					//   which will cause non-fast-forward update.
					Hash: ref.Hash(),
				})
				if err != nil {
					ui.Errorf("Error checkout local branch %s, %s", localBranchName, err)
					return err
				}
				fmt.Printf("local branch %s has been checked out. \n", localBranchName)

				// set upstream branch of local branch to avoid the local branch
				//   without a tracking remote branch
				err = gitRepo.CreateBranch(&config.Branch{
					Name:   localBranchName,
					Remote: "origin",
					Merge:  branchRef,
				})
				if err != nil {
					ui.Errorf("Error setting %s's upstream branch , %s", localBranchName, err)
					return err
				}

				fmt.Printf("local branch %s has been set to track remote branch %s. \n", localBranchName, remoteBranchName)
			} else if err != nil {
				ui.Errorf("Error checking local branch %s existing, %s", localBranchName, err)
				return err
			} else {
				fmt.Printf("local branch %s exists, skip creating. \n", localBranchName)
			}

			// switch to local branch, only after that we can do pull
			err = w.Checkout(&git.CheckoutOptions{
				Branch: branchRef,
				// abandon the modify of local
				Force: true,
			})
			if err != nil {
				ui.Errorf("Error checkout local branch %s, %s", localBranchName, err)
				return err
			}

			// pull from upstream branch
			err = w.Pull(&git.PullOptions{
				RemoteName:    "origin",
				ReferenceName: branchRef,
				// pull all commits, not only the latest
				Depth: 0,
			})
			if err == git.NoErrAlreadyUpToDate {
				fmt.Printf("local branch %s already up to date. \n", localBranchName)
			} else if err != nil {
				ui.Errorf("Error pulling local branch %s, %s", localBranchName, err)
			}

			fmt.Printf("local branch %s has successed pull from remote branch %s, already up to date. \n", localBranchName, remoteBranchName)
		}
		return nil
	})

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
