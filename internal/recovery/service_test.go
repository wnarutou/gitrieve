package recovery

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/archive"
	"github.com/wnarutou/gitrieve/internal/cache"
	"github.com/wnarutou/gitrieve/internal/db"
	"github.com/wnarutou/gitrieve/internal/logger"
	"github.com/wnarutou/gitrieve/internal/scm"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

func testFileStorage(root string) typedef.MultiStorage {
	return typedef.MultiStorage{Storage: typedef.Storage{Name: "backup", Type: "file", Path: root}}
}

func writeGitBackupArchive(t *testing.T, filename, rootName string) {
	t.Helper()
	source := filepath.Join(t.TempDir(), rootName)
	_, err := git.PlainInit(source, false)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(source, "README.md"), []byte("backup\n"), 0o644))
	buf, err := archive.Create(context.Background(), source, rootName)
	require.NoError(t, err)
	defer buf.Close()
	require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o755))
	destination, err := os.Create(filename)
	require.NoError(t, err)
	_, err = io.Copy(destination, buf)
	require.NoError(t, err)
	require.NoError(t, destination.Close())
}

func TestServiceRestoresEveryDiscoveredRepositoryWithoutConfiguration(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	currentDir := filepath.Join(base, "app")
	alphaDir := filepath.Join(root, "github.com", "acme", "alpha")
	writeGitBackupArchive(t, filepath.Join(alphaDir, "alpha.tar.gz"), "alpha")
	writeBackupArchive(t, filepath.Join(alphaDir, "discussions.tar.gz"), map[string]string{
		"discussion/2.md": "discussion\n",
	})
	betaDir := filepath.Join(root, "github.com", "acme", "beta")
	writeBackupArchive(t, filepath.Join(betaDir, "issues.tar.gz"), map[string]string{
		"issues/1.md": "issue\n",
	})

	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testDB.Close()) })
	svc := NewService(testDB, logger.NewLogger(testDB), currentDir)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	jobID, scan, err := svc.Start(context.Background(), testFileStorage(root))
	require.NoError(t, err)
	require.Equal(t, 2, scan.TotalRepositories)
	require.True(t, svc.Wait(jobID))

	readme, err := os.ReadFile(filepath.Join(
		currentDir, ".gitrieve", "github.com", "acme", "alpha", "code", "README.md",
	))
	require.NoError(t, err)
	require.Equal(t, "backup\n", string(readme))
	_, err = os.Stat(filepath.Join(
		currentDir, ".gitrieve", "github.com", "acme", "alpha", "discussion", "2.md",
	))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(
		currentDir, ".gitrieve", "github.com", "acme", "beta", "issues", "1.md",
	))
	require.NoError(t, err)

	var status, repoKey, errorMessage string
	require.NoError(t, testDB.QueryRow(
		`SELECT status, repo_key, COALESCE(error_message, '') FROM executions WHERE id = ?`, jobID,
	).Scan(&status, &repoKey, &errorMessage))
	require.Equal(t, "completed", status)
	require.Empty(t, repoKey, "restore maintenance jobs must not affect repository health")
	require.Empty(t, errorMessage)

	var logs int
	require.NoError(t, testDB.QueryRow(`SELECT COUNT(*) FROM logs WHERE execution_id = ?`, jobID).Scan(&logs))
	require.Greater(t, logs, 0)
}

func TestServiceCancelMarksMaintenanceJobCancelled(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	currentDir := filepath.Join(base, "app")
	writeBackupArchive(t, filepath.Join(root, "github.com", "acme", "alpha", "alpha.tar.gz"), map[string]string{
		"alpha/.git/HEAD": "ref: refs/heads/main\n",
	})

	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testDB.Close()) })
	svc := NewService(testDB, logger.NewLogger(testDB), currentDir)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	started := make(chan struct{})
	previous := restoreComponent
	restoreComponent = func(ctx context.Context, _ string, _ *scm.Repository, _ cache.Component, _ []typedef.MultiStorage) (cache.Result, error) {
		close(started)
		<-ctx.Done()
		return cache.Result{}, ctx.Err()
	}
	t.Cleanup(func() { restoreComponent = previous })

	jobID, _, err := svc.Start(context.Background(), testFileStorage(root))
	require.NoError(t, err)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("restore did not start")
	}
	require.True(t, svc.Cancel(jobID))
	require.True(t, svc.Wait(jobID))

	var status string
	require.NoError(t, testDB.QueryRow(`SELECT status FROM executions WHERE id = ?`, jobID).Scan(&status))
	require.Equal(t, "cancelled", status)
}

func TestServiceRejectsAConcurrentRestoreJob(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	currentDir := filepath.Join(base, "app")
	writeBackupArchive(t, filepath.Join(root, "github.com", "acme", "alpha", "alpha.tar.gz"), map[string]string{
		"alpha/.git/HEAD": "ref: refs/heads/main\n",
	})

	testDB, err := db.Initialize(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testDB.Close()) })
	svc := NewService(testDB, logger.NewLogger(testDB), currentDir)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	started := make(chan struct{})
	previous := restoreComponent
	restoreComponent = func(ctx context.Context, _ string, _ *scm.Repository, _ cache.Component, _ []typedef.MultiStorage) (cache.Result, error) {
		close(started)
		<-ctx.Done()
		return cache.Result{}, ctx.Err()
	}
	t.Cleanup(func() { restoreComponent = previous })

	first, _, err := svc.Start(context.Background(), testFileStorage(root))
	require.NoError(t, err)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first restore did not start")
	}
	_, _, err = svc.Start(context.Background(), testFileStorage(root))
	require.ErrorIs(t, err, ErrRestoreActive)

	require.True(t, svc.Cancel(first))
	require.True(t, svc.Wait(first))
}
