package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/db"
	"github.com/wnarutou/gitrieve/internal/executor"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

type API struct {
	config   *config.Config
	db       *db.DB
	executor *executor.Executor
}

func NewAPI(cfg *config.Config, db *db.DB, exec *executor.Executor) *API {
	return &API{config: cfg, db: db, executor: exec}
}

func (a *API) CreateJob(c *gin.Context) {
	var req CreateJobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, Response{
			Code:    400,
			Message: "Invalid request: " + err.Error(),
		})
		return
	}

	jobIDs, err := a.executor.ExecuteJob(req.RepositoryKey)
	if err != nil {
		if errors.Is(err, executor.ErrRepositoryNotFound) {
			c.JSON(http.StatusNotFound, Response{
				Code:    404,
				Message: "Repository not found in configuration",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, Response{
			Code:    500,
			Message: "Failed to execute job: " + err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, Response{
		Code: 200,
		Data: CreateJobResponse{
			JobIDs: jobIDs,
			Status: string(executor.StatusRunning),
		},
	})
}

func (a *API) CancelJob(c *gin.Context) {
	jobID := c.Param("id")

	if jobID == "" {
		c.JSON(http.StatusBadRequest, Response{
			Code:    400,
			Message: "Job ID is required",
		})
		return
	}

	// Cancel the job
	err := a.executor.CancelJob(jobID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{
			Code:    500,
			Message: "Failed to cancel job: " + err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, Response{
		Code: 200,
		Data: CancelJobResponse{
			Status: string(executor.StatusCancelled),
		},
	})
}

func (a *API) GetJobs(c *gin.Context) {
	// Parse query parameters
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	status := c.Query("status")
	repository := c.Query("repository")

	// Validate pagination
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}

	const jobSelect = "SELECT id, job_name, repo_key, start_time, end_time, status, error_message FROM executions"

	// Build query
	query := jobSelect + " WHERE 1=1"
	args := []interface{}{}

	if status != "" && status != "all" {
		query += " AND status = ?"
		args = append(args, status)
	}

	if repository != "" {
		// name 模糊匹配原始输入；URL 搜索词先规范化，命中规范化后的 repo_key。
		query += " AND (job_name LIKE ? ESCAPE '\\' OR repo_key LIKE ? ESCAPE '\\')"
		args = append(args, "%"+escapeLike(repository)+"%", "%"+escapeLike(typedef.NormalizeURL(repository))+"%")
	}

	// Get total count
	countQuery := "SELECT COUNT(*) FROM executions" + query[len(jobSelect):]
	var total int64
	err := a.db.QueryRow(countQuery, args...).Scan(&total)
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{
			Code:    500,
			Message: "Failed to count jobs: " + err.Error(),
		})
		return
	}

	// Get paginated results
	query += " ORDER BY start_time DESC LIMIT ? OFFSET ?"
	args = append(args, limit, (page-1)*limit)

	rows, err := a.db.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{
			Code:    500,
			Message: "Failed to query jobs: " + err.Error(),
		})
		return
	}
	defer rows.Close()

	jobs := make([]Job, 0)
	for rows.Next() {
		var job Job
		var startTime time.Time
		var endTime *time.Time
		var errorMessage *string

		var repoKey string
		err := rows.Scan(&job.ID, &job.Name, &repoKey, &startTime, &endTime, &job.Status, &errorMessage)
		if err != nil {
			c.JSON(http.StatusInternalServerError, Response{
				Code:    500,
				Message: "Failed to scan job: " + err.Error(),
			})
			return
		}

		job.StartTime = &startTime
		job.EndTime = endTime
		if errorMessage != nil {
			job.ErrorMessage = *errorMessage
		}

		// Resolve URL from config by identity key; unmatched (renamed/deleted/old
		// empty-key rows) keeps the job_name snapshot and empty URL.
		for _, repo := range a.config.Repository {
			if repo.Matches(repoKey) {
				job.URL = repo.URL
				break
			}
		}

		jobs = append(jobs, job)
	}

	if err := rows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, Response{
			Code:    500,
			Message: "Failed to iterate jobs: " + err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, Response{
		Code: 200,
		Data: ListJobsResponse{
			Jobs:  jobs,
			Total: total,
			Page:  page,
			Limit: limit,
		},
	})
}

// GetJobLogs streams logs for a given job ID as Server-Sent Events.
func (a *API) GetJobLogs(c *gin.Context) {
	jobID := c.Param("id")
	if jobID == "" {
		c.JSON(http.StatusBadRequest, Response{
			Code:    400,
			Message: "Job ID is required",
		})
		return
	}

	// Validate the job exists
	var status string
	err := a.db.QueryRow("SELECT status FROM executions WHERE id = ?", jobID).Scan(&status)
	if err != nil {
		c.JSON(http.StatusNotFound, Response{
			Code:    404,
			Message: "Job not found",
		})
		return
	}

	// Set SSE headers
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	var lastID int64
	heartbeatTicker := time.NewTicker(15 * time.Second)
	defer heartbeatTicker.Stop()

	c.Stream(func(w io.Writer) bool {
		// Check if the client disconnected
		if c.Request.Context().Err() != nil {
			return false
		}

		// Query logs newer than lastID
		rows, err := a.db.Query(
			"SELECT id, execution_id, timestamp, level, message FROM logs WHERE execution_id = ? AND id > ? ORDER BY id ASC",
			jobID, lastID,
		)
		if err != nil {
			return true
		}

		for rows.Next() {
			var entry LogEntry
			if err := rows.Scan(&entry.ID, &entry.ExecutionID, &entry.Timestamp, &entry.Level, &entry.Message); err != nil {
				rows.Close()
				return true
			}
			lastID = entry.ID

			data, err := json.Marshal(entry)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		rows.Close()

		// Check job completion status; stop streaming after flushing remaining logs
		var currentStatus string
		if err := a.db.QueryRow("SELECT status FROM executions WHERE id = ?", jobID).Scan(&currentStatus); err == nil {
			if currentStatus == string(executor.StatusCompleted) ||
				currentStatus == string(executor.StatusFailed) ||
				currentStatus == string(executor.StatusCancelled) {
				fmt.Fprintf(w, "event: done\ndata: {\"status\":\"%s\"}\n\n", currentStatus)
				return false
			}
		}

		// Wait before polling again, but bail out on disconnect or heartbeat
		select {
		case <-heartbeatTicker.C:
			// Send a heartbeat comment to keep the connection alive
			fmt.Fprintf(w, ": heartbeat\n\n")
			return true
		case <-c.Request.Context().Done():
			// Client disconnected; stop streaming
			return false
		case <-time.After(time.Second):
			// Poll for new logs
		}

		return true
	})
}

// runStats 是单仓库/单组织的聚合运行统计。
type runStats struct {
	LastRun *time.Time
	Total   int64
	Success int64
	Failed  int64
}

// lookupStats 返回配置条目的运行统计。type=repo 直接取自身键；type=org/user
// 对「路径边界前缀」（entry.Key()+"/"）命中的成员求和，last_run 取成员最大值。
// 前缀以 "/" 结尾，避免 github.com/acme 误吞 github.com/acme2/x。
func lookupStats(stats map[string]runStats, repo typedef.Repository) runStats {
	key := repo.Key()
	if key == "" {
		return runStats{}
	}
	switch repo.GetType() {
	case typedef.TypeOrg, typedef.TypeUser:
		prefix := key + "/"
		var sum runStats
		for k, s := range stats {
			if !strings.HasPrefix(k, prefix) {
				continue
			}
			sum.Total += s.Total
			sum.Success += s.Success
			sum.Failed += s.Failed
			if s.LastRun != nil && (sum.LastRun == nil || s.LastRun.After(*sum.LastRun)) {
				t := *s.LastRun
				sum.LastRun = &t
			}
		}
		return sum
	default:
		return stats[key]
	}
}

// GetRepositories returns repositories with per-repo execution stats, last/next
// run times, search (fuzzy name or URL match) and pagination.
func (a *API) GetRepositories(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	search := c.Query("search")

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}

	// Aggregate per-repository execution stats from the DB.
	stats := map[string]runStats{}

	// Note: we select the bare start_time column (constrained to the max by
	// HAVING) rather than MAX(start_time). The modernc.org/sqlite driver only
	// converts TEXT to time.Time for columns with a declared DATETIME type;
	// aggregate expressions like MAX(start_time) have no declared type and come
	// back as a raw string that database/sql cannot scan into *time.Time.
	rows, err := a.db.Query(`
		SELECT repo_key,
		       start_time AS last_run,
		       COUNT(*)        AS total,
		       COALESCE(SUM(status = 'completed'), 0) AS success,
		       COALESCE(SUM(status = 'failed'), 0)    AS failed
		FROM executions
		GROUP BY repo_key
		HAVING start_time = MAX(start_time)`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{Code: 500, Message: "Failed to query repository stats: " + err.Error()})
		return
	}
	defer rows.Close()

	for rows.Next() {
		var key string
		var lastRun sql.NullTime
		var a runStats
		if err := rows.Scan(&key, &lastRun, &a.Total, &a.Success, &a.Failed); err != nil {
			c.JSON(http.StatusInternalServerError, Response{Code: 500, Message: "Failed to scan repository stats: " + err.Error()})
			return
		}
		if lastRun.Valid {
			a.LastRun = &lastRun.Time
		}
		stats[key] = a
	}
	if err := rows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, Response{Code: 500, Message: "Failed to iterate repository stats: " + err.Error()})
		return
	}

	// Fuzzy name or URL filter (in-memory equivalent of LIKE '%search%'). SQL
	// LIKE is case-insensitive for ASCII, so match that by folding both sides to
	// lower case before the Contains check.
	filtered := make([]typedef.Repository, 0, len(a.config.Repository))
	for _, repo := range a.config.Repository {
		if search != "" {
			inName := strings.Contains(strings.ToLower(repo.Name), strings.ToLower(search))
			inURL := strings.Contains(strings.ToLower(repo.EffectiveURL()), strings.ToLower(search))
			if !inName && !inURL {
				continue
			}
		}
		filtered = append(filtered, repo)
	}

	total := len(filtered)
	start := (page - 1) * limit
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}

	now := time.Now()
	overviews := make([]RepositoryOverview, 0, end-start)
	for _, repo := range filtered[start:end] {
		// Serve the effective URL (type=user/org with empty URL and an orgName
		// synthesizes https://github.com/<orgName>). The frontend keys rows off
		// r.URL, so a raw config entry without `url` would otherwise come back
		// with URL=="" and its row buttons would no-op / 404. repo is a loop copy,
		// so this neither mutates nor persists the config.
		repo.URL = repo.EffectiveURL()
		s := lookupStats(stats, repo)
		overviews = append(overviews, RepositoryOverview{
			Repository:  repo,
			LastRunTime: s.LastRun,
			NextRunTime: nextRunTime(repo.Cron, now),
			TotalRuns:   s.Total,
			SuccessRuns: s.Success,
			FailedRuns:  s.Failed,
		})
	}

	c.JSON(http.StatusOK, Response{Code: 200, Data: ListRepositoriesResponse{
		Repositories: overviews,
		Total:        total,
		Page:         page,
		Limit:        limit,
	}})
}

// CreateRepository adds a new repository to the configuration.
func (a *API) CreateRepository(c *gin.Context) {
	var repo typedef.Repository
	if err := c.ShouldBindJSON(&repo); err != nil {
		c.JSON(http.StatusBadRequest, Response{
			Code:    400,
			Message: "Invalid request: " + err.Error(),
		})
		return
	}

	if repo.Name == "" {
		c.JSON(http.StatusBadRequest, Response{
			Code:    400,
			Message: "Repository name is required",
		})
		return
	}

	// user/org 空 URL → 填合成 URL；随后统一判身份键非空。
	repo.URL = repo.EffectiveURL()
	if repo.Key() == "" {
		c.JSON(http.StatusBadRequest, Response{
			Code:    400,
			Message: "Repository needs a non-empty URL (or orgName for user/org type)",
		})
		return
	}

	// 判重按身份键（URL），name 允许重复。
	for _, existing := range a.config.Repository {
		if existing.Key() == repo.Key() {
			c.JSON(http.StatusConflict, Response{
				Code:    409,
				Message: "Repository with URL '" + repo.Key() + "' already exists",
			})
			return
		}
	}

	// Append to in-memory config
	a.config.Repository = append(a.config.Repository, repo)

	// Persist config; tolerate save failures with a warning
	msg := ""
	if err := config.Save(); err != nil {
		msg = "Repository added in memory but failed to persist config: " + err.Error()
	}

	c.JSON(http.StatusOK, Response{
		Code:    200,
		Data:    repo,
		Message: msg,
	})
}

// UpdateRepository modifies an existing repository by identity key (partial update via JSON merge).
func (a *API) UpdateRepository(c *gin.Context) {
	// The route uses a *id catch-all so identity keys containing "/" reach the
	// handler; gin prefixes the captured value with "/", which we strip before
	// matching against repo keys.
	id := strings.TrimPrefix(c.Param("id"), "/")

	// Locate the existing repository by identity key (URL).
	idx := -1
	for i, existing := range a.config.Repository {
		if existing.Matches(id) {
			idx = i
			break
		}
	}
	if idx == -1 {
		c.JSON(http.StatusNotFound, Response{
			Code:    404,
			Message: "Repository not found",
		})
		return
	}

	// Decode the patch as a generic map for a partial merge.
	var patch map[string]interface{}
	if err := c.ShouldBindJSON(&patch); err != nil {
		c.JSON(http.StatusBadRequest, Response{
			Code:    400,
			Message: "Invalid request: " + err.Error(),
		})
		return
	}

	// Marshal the existing repository into a map, overlay the patch fields,
	// then unmarshal back into a typed struct. This preserves unspecified
	// fields while applying only the supplied changes.
	existingRaw, err := json.Marshal(a.config.Repository[idx])
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{
			Code:    500,
			Message: "Failed to marshal repository: " + err.Error(),
		})
		return
	}
	var mergedMap map[string]interface{}
	if err := json.Unmarshal(existingRaw, &mergedMap); err != nil {
		c.JSON(http.StatusInternalServerError, Response{
			Code:    500,
			Message: "Failed to unmarshal repository: " + err.Error(),
		})
		return
	}
	for k, v := range patch {
		mergedMap[k] = v
	}
	finalRaw, err := json.Marshal(mergedMap)
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{
			Code:    500,
			Message: "Failed to marshal merged repository: " + err.Error(),
		})
		return
	}
	var updated typedef.Repository
	if err := json.Unmarshal(finalRaw, &updated); err != nil {
		c.JSON(http.StatusInternalServerError, Response{
			Code:    500,
			Message: "Failed to unmarshal merged repository: " + err.Error(),
		})
		return
	}

	// user/org 空 URL → 填合成 URL；空身份键拒绝；与其他仓库 URL 冲突拒绝。
	updated.URL = updated.EffectiveURL()
	if updated.Key() == "" {
		c.JSON(http.StatusBadRequest, Response{
			Code:    400,
			Message: "Repository needs a non-empty URL (or orgName for user/org type)",
		})
		return
	}
	for i, other := range a.config.Repository {
		if i != idx && other.Key() == updated.Key() {
			c.JSON(http.StatusConflict, Response{
				Code:    409,
				Message: "Repository with URL '" + updated.Key() + "' already exists",
			})
			return
		}
	}

	a.config.Repository[idx] = updated

	msg := ""
	if err := config.Save(); err != nil {
		msg = "Repository updated in memory but failed to persist config: " + err.Error()
	}

	c.JSON(http.StatusOK, Response{
		Code:    200,
		Data:    updated,
		Message: msg,
	})
}

// DeleteRepository removes a repository from the configuration by identity key.
func (a *API) DeleteRepository(c *gin.Context) {
	// See UpdateRepository: strip the leading "/" gin adds to *id catch-all values.
	id := strings.TrimPrefix(c.Param("id"), "/")

	idx := -1
	for i, existing := range a.config.Repository {
		if existing.Matches(id) {
			idx = i
			break
		}
	}
	if idx == -1 {
		c.JSON(http.StatusNotFound, Response{
			Code:    404,
			Message: "Repository not found",
		})
		return
	}

	// Remove element at idx
	a.config.Repository = append(a.config.Repository[:idx], a.config.Repository[idx+1:]...)

	msg := ""
	if err := config.Save(); err != nil {
		msg = "Repository deleted in memory but failed to persist config: " + err.Error()
	}

	c.JSON(http.StatusOK, Response{
		Code:    200,
		Data:    SuccessResponse{Success: true},
		Message: msg,
	})
}

// GetStorages returns all storage backends from the configuration.
func (a *API) GetStorages(c *gin.Context) {
	c.JSON(http.StatusOK, Response{
		Code: 200,
		Data: a.config.Storage,
	})
}

// CreateStorage adds a new storage backend to the configuration.
func (a *API) CreateStorage(c *gin.Context) {
	var storage typedef.MultiStorage
	if err := c.ShouldBindJSON(&storage); err != nil {
		c.JSON(http.StatusBadRequest, Response{
			Code:    400,
			Message: "Invalid request: " + err.Error(),
		})
		return
	}

	if storage.Name == "" {
		c.JSON(http.StatusBadRequest, Response{
			Code:    400,
			Message: "Storage name is required",
		})
		return
	}

	// Validate type
	if storage.Type != "file" && storage.Type != "s3" {
		c.JSON(http.StatusBadRequest, Response{
			Code:    400,
			Message: "Storage type must be 'file' or 's3'",
		})
		return
	}

	// Check for duplicates
	for _, existing := range a.config.Storage {
		if existing.Name == storage.Name {
			c.JSON(http.StatusConflict, Response{
				Code:    409,
				Message: "Storage with name '" + storage.Name + "' already exists",
			})
			return
		}
	}

	// Append to in-memory config
	a.config.Storage = append(a.config.Storage, storage)

	// Persist config; tolerate save failures with a warning
	msg := ""
	if err := config.Save(); err != nil {
		msg = "Storage added in memory but failed to persist config: " + err.Error()
	}

	c.JSON(http.StatusOK, Response{
		Code:    200,
		Data:    storage,
		Message: msg,
	})
}

// UpdateStorage modifies an existing storage backend by name (partial update via JSON merge).
func (a *API) UpdateStorage(c *gin.Context) {
	id := c.Param("id")

	// Locate the existing storage
	idx := -1
	for i, existing := range a.config.Storage {
		if existing.Name == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		c.JSON(http.StatusNotFound, Response{
			Code:    404,
			Message: "Storage not found",
		})
		return
	}

	// Decode the patch as a generic map for a partial merge.
	var patch map[string]interface{}
	if err := c.ShouldBindJSON(&patch); err != nil {
		c.JSON(http.StatusBadRequest, Response{
			Code:    400,
			Message: "Invalid request: " + err.Error(),
		})
		return
	}

	// Marshal the existing storage into a map, overlay the patch fields,
	// then unmarshal back into a typed struct. This preserves unspecified
	// fields while applying only the supplied changes.
	existingRaw, err := json.Marshal(a.config.Storage[idx])
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{
			Code:    500,
			Message: "Failed to marshal storage: " + err.Error(),
		})
		return
	}
	var mergedMap map[string]interface{}
	if err := json.Unmarshal(existingRaw, &mergedMap); err != nil {
		c.JSON(http.StatusInternalServerError, Response{
			Code:    500,
			Message: "Failed to unmarshal storage: " + err.Error(),
		})
		return
	}
	for k, v := range patch {
		mergedMap[k] = v
	}
	finalRaw, err := json.Marshal(mergedMap)
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{
			Code:    500,
			Message: "Failed to marshal merged storage: " + err.Error(),
		})
		return
	}
	var updated typedef.MultiStorage
	if err := json.Unmarshal(finalRaw, &updated); err != nil {
		c.JSON(http.StatusInternalServerError, Response{
			Code:    500,
			Message: "Failed to unmarshal merged storage: " + err.Error(),
		})
		return
	}

	a.config.Storage[idx] = updated

	msg := ""
	if err := config.Save(); err != nil {
		msg = "Storage updated in memory but failed to persist config: " + err.Error()
	}

	c.JSON(http.StatusOK, Response{
		Code:    200,
		Data:    updated,
		Message: msg,
	})
}

// DeleteStorage removes a storage backend from the configuration by name.
func (a *API) DeleteStorage(c *gin.Context) {
	id := c.Param("id")

	idx := -1
	for i, existing := range a.config.Storage {
		if existing.Name == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		c.JSON(http.StatusNotFound, Response{
			Code:    404,
			Message: "Storage not found",
		})
		return
	}

	// Remove element at idx
	a.config.Storage = append(a.config.Storage[:idx], a.config.Storage[idx+1:]...)

	msg := ""
	if err := config.Save(); err != nil {
		msg = "Storage deleted in memory but failed to persist config: " + err.Error()
	}

	c.JSON(http.StatusOK, Response{
		Code:    200,
		Data:    SuccessResponse{Success: true},
		Message: msg,
	})
}
