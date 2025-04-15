package daemon

import (
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/spf13/cobra"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/discussion"
	"github.com/wnarutou/gitrieve/internal/issue"
	"github.com/wnarutou/gitrieve/internal/release"
	"github.com/wnarutou/gitrieve/internal/repository"
	"github.com/wnarutou/gitrieve/internal/typedef"
	"github.com/wnarutou/gitrieve/internal/ui"
	"github.com/wnarutou/gitrieve/internal/wiki"
)

var Cmd = &cobra.Command{
	Use:   "daemon",
	Short: "daemon runs as a daemon to monitor git repositories",
	Run:   runDaemon,
}

func runDaemon(cmd *cobra.Command, args []string) {
	storageMap := config.GetStorageMap()

	concurrencyNum := config.GetConcurrencyNum()

	s, err := gocron.NewScheduler(
		gocron.WithLocation(time.Local),
		gocron.WithLimitConcurrentJobs(concurrencyNum, gocron.LimitModeWait),
	)
	if err != nil {
		ui.ErrorfExit("Error creating scheduler, %s", err)
	}

	for _, repo := range repository.GetRepositories("") {
		if repo.Cron == "" {
			continue
		}
		storages := make([]typedef.MultiStorage, 0)
		for _, storage := range repo.Storage {
			if s, ok := storageMap[storage]; !ok {
				continue
			} else {
				storages = append(storages, s)
			}
		}
		_, err := s.NewJob(
			gocron.CronJob(repo.Cron, false),
			gocron.NewTask(repository.Sync, repo, storages),
		)
		if err != nil {
			ui.Errorf("Error scheduling download codes of %s, %s", repo.Name, err)
		}
		if repo.DownloadReleases {
			_, err = s.NewJob(
				gocron.CronJob(repo.Cron, false),
				gocron.NewTask(release.DownloadAllAssets, repo, storages),
			)
			if err != nil {
				ui.Errorf("Error scheduling download releases of %s, %s", repo.Name, err)
			}
		}
		if repo.DownloadIssues {
			_, err = s.NewJob(
				gocron.CronJob(repo.Cron, false),
				gocron.NewTask(issue.Sync, repo, storages),
			)
			if err != nil {
				ui.Errorf("Error scheduling download issues of %s, %s", repo.Name, err)
			}
		}
		if repo.DownloadWiki {
			_, err = s.NewJob(
				gocron.CronJob(repo.Cron, false),
				gocron.NewTask(wiki.Sync, repo, storages),
			)
			if err != nil {
				ui.Errorf("Error scheduling download wiki of %s, %s", repo.Name, err)
			}
		}
		if repo.DownloadDiscussion {
			_, err = s.NewJob(
				gocron.CronJob(repo.Cron, false),
				gocron.NewTask(discussion.Sync, repo, storages),
			)
			if err != nil {
				ui.Errorf("Error scheduling download discussion of %s, %s", repo.Name, err)
			}
		}
		ui.Printf("Scheduled %s, cron: %s", repo.Name, repo.Cron)
	}
	ui.Printf("Starting daemon")
	s.Start()
}
