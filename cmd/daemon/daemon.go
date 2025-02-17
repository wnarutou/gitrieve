package daemon

import (
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/leslieleung/reaper/internal/config"
	"github.com/leslieleung/reaper/internal/rip"
	"github.com/leslieleung/reaper/internal/typedef"
	"github.com/leslieleung/reaper/internal/ui"
	"github.com/spf13/cobra"
)

var Cmd = &cobra.Command{
	Use:   "daemon",
	Short: "daemon runs as a daemon to monitor git repositories",
	Run:   runDaemon,
}

func runDaemon(cmd *cobra.Command, args []string) {
	storageMap := config.GetStorageMap()

	// Retrieve the concurrency number from configuration. This determines the maximum number
	// of concurrent jobs the scheduler will run. If not configured (i.e., zero),
	// default to 3 concurrent jobs to ensure stable performance.
	concurrencyNum := config.GetConcurrencyNum()
	if concurrencyNum == 0 {
		concurrencyNum = 3
	}

	s, err := gocron.NewScheduler(
		gocron.WithLocation(time.Local),
		gocron.WithLimitConcurrentJobs(concurrencyNum, gocron.LimitModeWait),
	)
	if err != nil {
		ui.ErrorfExit("Error creating scheduler, %s", err)
	}

	for _, repo := range rip.GetRepositories("") {
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
			gocron.NewTask(rip.Rip, repo, storages),
		)
		if err != nil {
			ui.Errorf("Error scheduling %s, %s", repo.Name, err)
		}
		ui.Printf("Scheduled %s, cron: %s", repo.Name, repo.Cron)
	}
	ui.Printf("Starting daemon")
	s.Start()
}
