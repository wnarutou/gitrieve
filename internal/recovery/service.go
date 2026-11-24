package recovery

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wnarutou/gitrieve/internal/cache"
	"github.com/wnarutou/gitrieve/internal/db"
	"github.com/wnarutou/gitrieve/internal/lock"
	"github.com/wnarutou/gitrieve/internal/logger"
	"github.com/wnarutou/gitrieve/internal/scm"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

const JobNamePrefix = "Restore from backup"

const restoreWorkers = 3

var (
	ErrClosed              = errors.New("restore service is closed")
	ErrFileStorageRequired = errors.New("restore source must be a file storage")
	ErrNoRepositories      = errors.New("no restorable repositories found")
	ErrRestoreActive       = errors.New("a restore job is already running")
)

// restoreComponent is the cache restoration seam used by the batch worker.
var restoreComponent = cache.RestoreComponent

type job struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type Service struct {
	db         *db.DB
	log        *logger.Logger
	currentDir string

	mu     sync.Mutex
	jobs   map[string]*job
	closed bool
	wg     sync.WaitGroup
}

type repositoryResult struct {
	repository Repository
	restored   int
	skipped    int
	failures   []string
}

func NewService(database *db.DB, log *logger.Logger, currentDir string) *Service {
	return &Service{
		db:         database,
		log:        log,
		currentDir: currentDir,
		jobs:       make(map[string]*job),
	}
}

func (s *Service) CurrentDir() string {
	return s.currentDir
}

// Start scans the selected file storage and launches one maintenance job that
// restores every discovered cache. The job's repo_key is empty, so repository
// health and last-success timestamps are unaffected.
func (s *Service) Start(ctx context.Context, source typedef.MultiStorage) (string, ScanResult, error) {
	if source.Type != "file" {
		return "", ScanResult{}, ErrFileStorageRequired
	}
	if err := ctx.Err(); err != nil {
		return "", ScanResult{}, err
	}
	scan, err := Scan(source.Path, s.currentDir)
	if err != nil {
		return "", ScanResult{}, err
	}
	source.Path = scan.Root
	if scan.TotalRepositories == 0 {
		return "", scan, ErrNoRepositories
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", scan, ErrClosed
	}
	if len(s.jobs) > 0 {
		s.mu.Unlock()
		return "", scan, ErrRestoreActive
	}
	jobID := uuid.New().String()
	jobName := JobNamePrefix + ": " + source.Name
	jobCtx, cancel := context.WithCancel(context.Background())
	s.jobs[jobID] = &job{cancel: cancel, done: make(chan struct{})}
	s.wg.Add(1)
	s.mu.Unlock()

	if _, err := s.db.Exec(`
		INSERT INTO executions (id, job_name, repo_key, start_time, status)
		VALUES (?, ?, '', ?, 'pending')`, jobID, jobName, time.Now(),
	); err != nil {
		s.removeJob(jobID)
		cancel()
		s.wg.Done()
		return "", scan, fmt.Errorf("create restore execution: %w", err)
	}

	go s.run(jobCtx, jobID, jobName, source, scan.Repositories)
	return jobID, scan, nil
}

func (s *Service) run(ctx context.Context, jobID, jobName string, source typedef.MultiStorage, repositories []Repository) {
	defer s.wg.Done()
	defer s.removeJob(jobID)

	s.setStatus(jobID, "running", "")
	s.logf(jobID, jobName, "info", "Found %s with cache backups in %s", countLabel(len(repositories), "repository", "repositories"), source.Path)

	results := make(chan repositoryResult)
	sem := make(chan struct{}, restoreWorkers)
	var workers sync.WaitGroup
	for _, repository := range repositories {
		repository := repository
		workers.Add(1)
		go func() {
			defer workers.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results <- repositoryResult{repository: repository}
				return
			}
			defer func() { <-sem }()
			results <- s.restoreRepository(ctx, jobID, jobName, source, repository)
		}()
	}
	go func() {
		workers.Wait()
		close(results)
	}()

	restored, skipped, failed := 0, 0, 0
	var failures []string
	for result := range results {
		restored += result.restored
		skipped += result.skipped
		failed += len(result.failures)
		failures = append(failures, result.failures...)
	}

	switch {
	case ctx.Err() != nil:
		s.logf(jobID, jobName, "info", "Restore cancelled: %s restored, %s already present",
			countLabel(restored, "cache component", "cache components"),
			countLabel(skipped, "cache component", "cache components"))
		s.setStatus(jobID, "cancelled", "")
	case failed > 0:
		message := countLabel(failed, "cache component failed", "cache components failed")
		s.logf(jobID, jobName, "error", "Restore finished with %s: %s restored, %s already present",
			message,
			countLabel(restored, "cache component", "cache components"),
			countLabel(skipped, "cache component", "cache components"))
		s.setStatus(jobID, "failed", message+": "+joinFailures(failures))
	default:
		s.logf(jobID, jobName, "info", "Restore completed: %s restored, %s already present",
			countLabel(restored, "cache component", "cache components"),
			countLabel(skipped, "cache component", "cache components"))
		s.setStatus(jobID, "completed", "")
	}
}

func (s *Service) restoreRepository(ctx context.Context, jobID, jobName string, source typedef.MultiStorage, repository Repository) repositoryResult {
	result := repositoryResult{repository: repository}
	r := &scm.Repository{Host: repository.Host, Owner: repository.Owner, Name: repository.Name}
	for _, component := range repository.Components {
		if err := ctx.Err(); err != nil {
			return result
		}
		label := string(component.Component)
		s.logf(jobID, jobName, "info", "Restoring %s cache for %s", label, repository.Key)
		unlock, err := lock.Acquire(ctx, r, lockComponent(component.Component), s.currentDir)
		if err != nil {
			if ctx.Err() != nil {
				return result
			}
			result.failures = append(result.failures, fmt.Sprintf("%s %s: lock: %v", repository.Key, label, err))
			s.logf(jobID, jobName, "error", "Failed to lock %s cache for %s: %v", label, repository.Key, err)
			continue
		}
		restoreResult, err := restoreComponent(ctx, s.currentDir, r, component.Component, []typedef.MultiStorage{source})
		unlock()
		if err != nil {
			if ctx.Err() != nil {
				return result
			}
			result.failures = append(result.failures, fmt.Sprintf("%s %s: %v", repository.Key, label, err))
			s.logf(jobID, jobName, "error", "Failed to restore %s cache for %s: %v", label, repository.Key, err)
			continue
		}
		switch restoreResult.Status {
		case cache.StatusRestored:
			result.restored++
			s.logf(jobID, jobName, "info", "Restored %s cache for %s", label, repository.Key)
		case cache.StatusAlreadyPresent:
			result.skipped++
			s.logf(jobID, jobName, "info", "Skipped %s cache for %s: already present", label, repository.Key)
		case cache.StatusNotFound:
			result.failures = append(result.failures, fmt.Sprintf("%s %s: backup archive missing", repository.Key, label))
			s.logf(jobID, jobName, "error", "Backup archive missing for %s %s", repository.Key, label)
		}
	}
	return result
}

func lockComponent(component cache.Component) string {
	if component == cache.ComponentIssues {
		return "issue"
	}
	return string(component)
}

func (s *Service) setStatus(jobID, status, message string) {
	if status == "pending" || status == "running" {
		_, _ = s.db.Exec(
			`UPDATE executions SET status = ?, error_message = ? WHERE id = ?`,
			status, message, jobID,
		)
		return
	}
	_, _ = s.db.Exec(
		`UPDATE executions SET status = ?, error_message = ?, end_time = ? WHERE id = ?`,
		status, message, time.Now(), jobID,
	)
}

func (s *Service) logf(jobID, jobName, level, format string, args ...interface{}) {
	if s.log == nil {
		return
	}
	_ = s.log.Log(jobID, jobName, level, fmt.Sprintf(format, args...))
}

func (s *Service) removeJob(jobID string) {
	s.mu.Lock()
	if current, ok := s.jobs[jobID]; ok {
		delete(s.jobs, jobID)
		close(current.done)
	}
	s.mu.Unlock()
}

func (s *Service) Cancel(jobID string) bool {
	s.mu.Lock()
	current, ok := s.jobs[jobID]
	s.mu.Unlock()
	if !ok {
		return false
	}
	current.cancel()
	return true
}

func (s *Service) Wait(jobID string) bool {
	s.mu.Lock()
	current, ok := s.jobs[jobID]
	s.mu.Unlock()
	if ok {
		<-current.done
		return true
	}
	var status string
	if err := s.db.QueryRow(`SELECT status FROM executions WHERE id = ?`, jobID).Scan(&status); err != nil {
		return false
	}
	switch status {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.wg.Wait()
		return nil
	}
	s.closed = true
	for _, current := range s.jobs {
		current.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return nil
}

func joinFailures(failures []string) string {
	if len(failures) == 0 {
		return ""
	}
	const maxDetails = 5
	visible := failures
	if len(visible) > maxDetails {
		visible = visible[:maxDetails]
	}
	out := visible[0]
	for _, failure := range visible[1:] {
		out += "; " + failure
	}
	if len(failures) > maxDetails {
		out += fmt.Sprintf("; and %d more", len(failures)-maxDetails)
	}
	return out
}

func countLabel(count int, singular, plural string) string {
	label := plural
	if count == 1 {
		label = singular
	}
	return fmt.Sprintf("%d %s", count, label)
}
