package repository

import (
	"bytes"
	"context"
	"os"
	"path"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/uuid"
	"github.com/mholt/archiver/v4"
	internalconfig "github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/scm"
	"github.com/wnarutou/gitrieve/internal/scm/github"
	"github.com/wnarutou/gitrieve/internal/storage"
	"github.com/wnarutou/gitrieve/internal/typedef"
	"github.com/wnarutou/gitrieve/internal/ui"
)

func GetRepositories(name string) []typedef.Repository {
	repositories := make([]typedef.Repository, 0)
	if name != "" {
		// find repo in config
		for _, repository := range internalconfig.GetIns().Repository {
			if repository.Name == name {
				repositories = addRepo(repository, repositories)
			}
		}
		return repositories
	}
	for _, repository := range internalconfig.GetIns().Repository {
		repositories = addRepo(repository, repositories)
	}
	return repositories
}

func addRepo(repo typedef.Repository, ret []typedef.Repository) []typedef.Repository {
	switch repo.GetType() {
	case typedef.TypeRepo:
		ret = append(ret, repo)
	case typedef.TypeUser, typedef.TypeOrg:
		// get repos
		client, err := github.New()
		if err != nil {
			ui.Errorf("Error creating github client, %s", err)
			return ret
		}
		repos, err := client.GetRepos(repo.OrgName, repo.Type)
		if err != nil {
			ui.Errorf("Error getting user repos, %s", err)
			return ret
		}
		for _, r := range repos {
			ret = append(ret, typedef.Repository{
				Name:               path.Base(r),
				URL:                r,
				Cron:               repo.Cron,
				Storage:            repo.Storage,
				UseCache:           repo.UseCache,
				Type:               typedef.TypeRepo,
				AllBranches:        repo.AllBranches,
				Depth:              repo.Depth,
				DownloadReleases:   repo.DownloadReleases,
				DownloadIssues:     repo.DownloadIssues,
				DownloadWiki:       repo.DownloadWiki,
				DownloadDiscussion: repo.DownloadDiscussion,
			})
		}
	default:
		ui.Errorf("Invalid repository type %s", repo.Type)
	}
	return ret
}

func Sync(repo typedef.Repository, iswiki bool, storages []typedef.MultiStorage) error {
	useCache := repo.UseCache
	depth := repo.Depth
	allBranches := repo.AllBranches
	isUpdated := false
	// get current directory
	currentDir, _ := os.Getwd()

	var workingDir string
	if useCache {
		workingDir = path.Join(currentDir, ".gitrieve")
	} else {
		id := uuid.New().String()
		workingDir = path.Join(currentDir, ".gitrieve", id)
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
	var gitDir string
	var gitSuffix string
	var gitUrl string
	if iswiki {
		gitDir = path.Join(workingDir, r.Host, r.Owner, repoName, "wiki")
		gitSuffix = ".wiki.git"
		gitUrl = repo.URL + ".wiki"
	} else {
		gitDir = path.Join(workingDir, r.Host, r.Owner, repoName, "code")
		gitSuffix = ".git"
		gitUrl = repo.URL
	}
	var exist bool
	// check if the repo already exists
	if _, err := os.Stat(path.Join(gitDir, gitSuffix)); err == nil {
		exist = true
	}
	var gitRepo *git.Repository
	// clone the repo if it does not exist, otherwise pull
	if !exist {
		isUpdated = true
		_, err = git.PlainClone(gitDir, false, &git.CloneOptions{
			URL:      "https://" + gitUrl,
			Progress: os.Stdout,
			Depth:    depth,
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
		RefSpecs: []config.RefSpec{
			config.RefSpec("+refs/heads/*:refs/remotes/origin/*"),
		},
		Force: true,
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

	// get remote default branch
	var remoteDefaultBranchName string
	var remoteDefaultBranchRef plumbing.ReferenceName

	remote, err := gitRepo.Remote("origin")
	if err != nil {
		ui.Errorf("Error get remote, %s", err)
		return err
	}
	remoteRefs, err := remote.List(&git.ListOptions{})
	if err != nil {
		ui.Errorf("Error get remote references, %s", err)
	}
	for _, ref := range remoteRefs {
		if ref.Name() == "HEAD" {
			remoteDefaultBranchRef = ref.Target()
			remoteDefaultBranchName = remoteDefaultBranchRef.Short()
		}
	}

	// find all remote branches
	refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Name().IsRemote() {
			// get remote branch name
			remoteBranchName := ref.Name().Short()

			// if not pull all branches and this branch is not default then skip
			if !allBranches && (remoteDefaultBranchName != remoteBranchName) {
				return nil
			}

			// set local branch name
			localBranchName := remoteBranchName[len("origin/"):]

			// create local branch reference
			branchRef := plumbing.NewBranchReferenceName(localBranchName)

			// check local branch exist or not
			_, err := gitRepo.Reference(branchRef, false)

			// if local branch not exist
			if err == plumbing.ErrReferenceNotFound {
				isUpdated = true
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
				ui.Printf("local branch %s has been checked out. \n", localBranchName)

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

				ui.Printf("local branch %s has been set to track remote branch %s. \n", localBranchName, remoteBranchName)
			} else if err != nil {
				ui.Errorf("Error checking local branch %s existing, %s", localBranchName, err)
				return err
			} else {
				ui.Printf("local branch %s exists, skip creating. \n", localBranchName)
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
				Depth: depth,
			})
			if err == git.NoErrAlreadyUpToDate {
				ui.Printf("local branch %s already up to date. \n", localBranchName)
			} else if err != nil {
				ui.Errorf("Error pulling local branch %s, %s", localBranchName, err)
			} else {
				isUpdated = true
			}

			ui.Printf("local branch %s has successed pull from remote branch %s, already up to date. \n", localBranchName, remoteBranchName)
		}
		return nil
	})

	// switch to default branch
	err = w.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(remoteDefaultBranchName),
		// abandon the modify of local
		Force: true,
	})
	if err != nil {
		ui.Errorf("Error checkout default branch %s, %s", remoteDefaultBranchRef, err)
		return err
	}

	if isUpdated {
		// change directory to the parent directory of the repo
		err = os.Chdir(path.Dir(gitDir))
		if err != nil {
			ui.Errorf("Error changing directory, %s", err)
		}

		var sourceDir string
		var targetDir string
		if iswiki {
			sourceDir = "wiki"
			targetDir = r.Name + "_wiki"
		} else {
			sourceDir = "code"
			targetDir = r.Name
		}
		files, err := archiver.FilesFromDisk(nil, map[string]string{
			sourceDir: targetDir,
		})
		if err != nil {
			ui.Errorf("Error reading files, %s", err)
			return err
		}

		// For codes, there is no need to save the history.
		// The latest one is the full version and already contains all the history,
		// so it can be replaced directly.
		// now := time.Now().Format("20060102150405")
		base := targetDir + ".tar.gz"
		// TODO store to a temporary file first if greater than certain size,
		//      we can use isUpdated to support this feature temporality
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

		// handle storages
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
		ui.Printf("All is uptodate, no need to restore")
	}

	// cleanup
	if !useCache {
		err = os.RemoveAll(gitDir)
		if err != nil {
			ui.Errorf("Error cleaning up working directory, %s", err)
			return err
		}
	}
	return nil
}
