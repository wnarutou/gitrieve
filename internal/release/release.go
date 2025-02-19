package release

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"

	gh "github.com/google/go-github/v56/github"
	"github.com/leslieleung/reaper/internal/config"
	"github.com/leslieleung/reaper/internal/scm"
	"github.com/leslieleung/reaper/internal/scm/github"
	"github.com/leslieleung/reaper/internal/storage"
	"github.com/leslieleung/reaper/internal/typedef"
	"github.com/leslieleung/reaper/internal/ui"
)

// Define a slice
type ByPublishedAt []*gh.RepositoryRelease

// Implement the Len method of sort.Interface interface
func (r ByPublishedAt) Len() int { return len(r) }

// Implement the Less method of sort.Interface interface
func (r ByPublishedAt) Less(i, j int) bool { return r[i].PublishedAt.After(r[j].PublishedAt.Time) }

// Implement the Swap method of the sort.Interface interface
func (r ByPublishedAt) Swap(i, j int) { r[i], r[j] = r[j], r[i] }

func DownloadAllAssets(repo typedef.Repository, storages []typedef.MultiStorage) error {
	releaseNumLimit := config.GetReleaseNumLimit()
	releaseSizeLimit := config.GetReleaseSizeLimit()
	r, err := scm.NewRepository(repo.URL)
	if err != nil {
		return err
	}
	c, err := github.New()
	if err != nil {
		return err
	}
	// get all releases
	releases, err := c.GetReleases(r.Owner, r.Name)
	if err != nil {
		return err
	}
	sort.Sort(ByPublishedAt(releases))
	if releaseNumLimit >= 0 {
		if len(releases) < releaseNumLimit {
			releaseNumLimit = len(releases)
		}
		releases = releases[:releaseNumLimit]
	}
	allReleaseSize := 0
	var reserveTagName []string
	for _, release := range releases {
		if releaseSizeLimit >= 0 {
			if allReleaseSize >= releaseSizeLimit {
				ui.Printf("The size %s limit has been reached, no more downloading", releaseSizeLimit)
				break
			}
		}
		// get all assets
		assets, err := c.GetReleaseAssets(r.Owner, r.Name, release.GetID())
		if err != nil {
			return err
		}
		reserveTagName = append(reserveTagName, release.GetTagName())
		for _, asset := range assets {
			if asset.GetState() != "uploaded" {
				continue
			}
			allReleaseSize = allReleaseSize + *asset.Size
			filename := fmt.Sprintf("%s/%s", release.GetTagName(), asset.GetName())
			var needDownloadStorage []typedef.MultiStorage
			for _, s := range storages {
				backend, err := storage.GetStorage(s)
				if err != nil {
					return err
				}
				var objectMetaInfo []storage.ObjectMetaInfo
				if s.Type == storage.FileStorage {
					if filepath.IsAbs(s.Path) {
						objectMetaInfo, err = backend.ListObjectMetaInfo(path.Join(s.Path, r.Host, r.Owner, r.Name, "release", filename))
						if err != nil {
							needDownloadStorage = append(needDownloadStorage, s)
							continue
						}
					} else {
						currentDir, err := os.Getwd()
						if err != nil {
							return err
						}
						objectMetaInfo, err = backend.ListObjectMetaInfo(path.Join(currentDir, s.Path, r.Host, r.Owner, r.Name, "release", filename))
						if err != nil {
							needDownloadStorage = append(needDownloadStorage, s)
							continue
						}
					}
				} else {
					objectMetaInfo, err = backend.ListObjectMetaInfo(path.Join(s.Path, r.Host, r.Owner, r.Name, "release", filename))
					if err != nil {
						needDownloadStorage = append(needDownloadStorage, s)
						continue
					}
				}

				if objectMetaInfo[0].Size != int64(asset.GetSize()) {
					needDownloadStorage = append(needDownloadStorage, s)
					continue
				}
			}
			if len(needDownloadStorage) == 0 {
				continue
			}
			// download asset
			ui.Printf("Downloading %s asset %s", *release.TagName, asset.GetName())
			rc, err := c.DownloadAsset(r.Owner, r.Name, asset.GetID())
			if err != nil {
				return err
			}
			// put rc to file
			data, err := io.ReadAll(rc)
			if err != nil {
				return err
			}
			for _, s := range needDownloadStorage {
				backend, err := storage.GetStorage(s)
				if err != nil {
					return err
				}

				if s.Type == storage.FileStorage {
					if filepath.IsAbs(s.Path) {
						err = backend.PutObject(path.Join(s.Path, r.Host, r.Owner, r.Name, "release", filename), data)
						if err != nil {
							return err
						}
					} else {
						currentDir, err := os.Getwd()
						if err != nil {
							return err
						}
						err = backend.PutObject(path.Join(currentDir, s.Path, r.Host, r.Owner, r.Name, "release", filename), data)
						if err != nil {
							return err
						}
					}
				} else {
					err = backend.PutObject(path.Join(s.Path, r.Host, r.Owner, r.Name, "release", filename), data)
					if err != nil {
						return err
					}
				}
			}
		}
	}
	for _, s := range storages {
		backend, err := storage.GetStorage(s)
		if err != nil {
			return err
		}

		var objectMetaInfo []storage.ObjectMetaInfo
		if s.Type == storage.FileStorage {
			if filepath.IsAbs(s.Path) {
				objectMetaInfo, err = backend.ListObjectMetaInfo(path.Join(s.Path, r.Host, r.Owner, r.Name, "release"))
				if err != nil {
					continue
				}
			} else {
				currentDir, err := os.Getwd()
				if err != nil {
					return err
				}
				objectMetaInfo, err = backend.ListObjectMetaInfo(path.Join(currentDir, s.Path, r.Host, r.Owner, r.Name, "release"))
				if err != nil {
					continue
				}
			}
		} else {
			objectMetaInfo, err = backend.ListObjectMetaInfo(path.Join(s.Path, r.Host, r.Owner, r.Name, "release"))
			if err != nil {
				continue
			}
		}

		for _, dirInfo := range objectMetaInfo {
			dirName := filepath.Base(dirInfo.Path)
			found := false
			for _, tagName := range reserveTagName {
				if tagName == dirName {
					found = true
					break
				}
			}
			if !found {
				err = backend.DeleteObject(dirInfo.Path)
				if err != nil {
					return err
				}
				ui.Printf("Deleted directory %s", dirInfo.Path)
			}
		}
	}
	return nil
}
