# Gitrieve Web UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a web UI to Gitrieve for visual management of backup tasks, including job monitoring, configuration management, and log viewing.

**Architecture:** Embedded HTTP server in the daemon process with REST APIs and static frontend resources. Uses SQLite for persistence and real-time logging with Server-Sent Events.

**Tech Stack:** Go (gin framework), SQLite, HTML/CSS/JavaScript, embedded static assets

## Global Constraints

- Go 1.23+ required
- Use existing gocron/v2 for scheduling
- SQLite for data persistence
- Embedded static assets using go:embed
- Single binary deployment
- Follow existing Go code patterns in the codebase
- Maintain backward compatibility with existing CLI commands

---

## Phase 1: MVP Implementation (Week 1-2)

### Phase 1 Overview
Build the core web server with basic job listing, manual execution, and simple log viewing. Focus on getting the fundamental architecture working.

### Task 1.1: Add Dependencies and Server Command

**Files:**
- Modify: `go.mod` - Add gin and sqlite dependencies
- Create: `cmd/server/server.go` - Main server command
- Create: `internal/server/types.go` - Common response types

**Interfaces:**
- Consumes: Existing config and repository modules
- Produces: HTTP server instance with basic routes

- [ ] **Step 1: Write the failing test for server command**

```go
// tests/cmd/server/server_test.go
package server

import (
    "net/http"
    "net/http/httptest"
    "testing"
)

func TestServerRootRoute(t *testing.T) {
    server := NewServer(nil)
    req, _ := http.NewRequest("GET", "/", nil)
    resp := httptest.NewRecorder()
    
    server.ServeHTTP(resp, req)
    
    if resp.Code != 200 {
        t.Errorf("Expected status 200, got %d", resp.Code)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./tests/cmd/server/server_test.go -v`
Expected: FAIL with "server not defined"

- [ ] **Step 3: Write minimal implementation**

```go
// cmd/server/server.go
package server

import (
    "github.com/gin-gonic/gin"
    "github.com/wnarutou/gitrieve/internal/config"
)

type Server struct {
    router *gin.Engine
}

func NewServer(cfg *config.Config) *Server {
    s := &Server{
        router: gin.Default(),
    }
    s.setupRoutes()
    return s
}

func (s *Server) setupRoutes() {
    s.router.GET("/", func(c *gin.Context) {
        c.String(200, "Gitrieve Web UI")
    })
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    s.router.ServeHTTP(w, r)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./tests/cmd/server/server_test.go -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git go.mod cmd/server/server.go tests/cmd/server/server_test.go
git commit -m "feat: add basic server command structure"
```

### Task 1.2: Integrate Server with Root Command

**Files:**
- Modify: `cmd/root.go` - Add server command
- Modify: `main.go` - Add server subcommand
- Create: `internal/server/config.go` - Server configuration

**Interfaces:**
- Consumes: Viper configuration system
- Produces: Integrated server command in CLI

- [ ] **Step 1: Write the failing test for root command integration**

```go
// tests/cmd/root_test.go
package cmd

import (
    "testing"
    "github.com/spf13/cobra"
)

func TestServerCommandExists(t *testing.T) {
    var cmd *cobra.Command
    var found bool
    
    rootCmd.Execute()
    
    for _, c := range rootCmd.Commands() {
        if c.Use == "server" {
            found = true
            cmd = c
            break
        }
    }
    
    if !found {
        t.Fatal("server command not found")
    }
    
    if cmd.Short != "start web server" {
        t.Errorf("Expected short description 'start web server', got %s", cmd.Short)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./tests/cmd/root_test.go -v`
Expected: FAIL with "server command not found"

- [ ] **Step 3: Write minimal implementation**

```go
// cmd/root.go
import (
    "github.com/wnarutou/gitrieve/cmd/server"
    // ... other imports
)

func init() {
    // ... existing commands
    rootCmd.AddCommand(server.Cmd)
}

// cmd/server/server.go
var Cmd = &cobra.Command{
    Use:   "server",
    Short: "start web server",
    Run: func(cmd *cobra.Command, args []string) {
        cfg := config.GetIns()
        s := NewServer(cfg)
        s.Run()
    },
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./tests/cmd/root_test.go -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/root.go main.go cmd/server/server.go internal/server/config.go tests/cmd/root_test.go
git commit -m "feat: integrate server command into root"
```

### Task 1.3: Add Database Schema and Models

**Files:**
- Create: `internal/db/db.go` - Database connection and setup
- Create: `internal/db/models.go` - Data models
- Create: `internal/db/migrations.go` - Database migrations

**Interfaces:**
- Consumes: SQLite database
- Produces: Database models and migration functions

- [ ] **Step 1: Write the failing test for database setup**

```go
// tests/internal/db/db_test.go
package db

import (
    "testing"
    "github.com/stretchr/testify/assert"
)

func TestDatabaseInitialization(t *testing.T) {
    db, err := Initialize(":memory:")
    assert.NoError(t, err)
    assert.NotNil(t, db)
    
    // Test tables exist
    var count int
    err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table'").Scan(&count)
    assert.NoError(t, err)
    assert.Greater(t, count, 0)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./tests/internal/db/db_test.go -v`
Expected: FAIL with "Initialize not defined"

- [ ] **Step 3: Write minimal implementation**

```go
// internal/db/db.go
package db

import (
    "database/sql"
    _ "github.com/mattn/go-sqlite3"
)

type DB struct {
    *sql.DB
}

func Initialize(path string) (*DB, error) {
    db, err := sql.Open("sqlite3", path)
    if err != nil {
        return nil, err
    }
    
    // Create tables
    _, err = db.Exec(`
        CREATE TABLE IF NOT EXISTS executions (
            id TEXT PRIMARY KEY,
            job_name TEXT NOT NULL,
            start_time DATETIME NOT NULL,
            end_time DATETIME,
            status TEXT NOT NULL,
            error_message TEXT,
            created_at DATETIME DEFAULT CURRENT_TIMESTAMP
        );
        
        CREATE TABLE IF NOT EXISTS logs (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            execution_id TEXT NOT NULL,
            timestamp DATETIME NOT NULL,
            level TEXT NOT NULL,
            message TEXT NOT NULL,
            FOREIGN KEY (execution_id) REFERENCES executions(id)
        );
    `)
    
    return &DB{db}, err
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./tests/internal/db/db_test.go -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/db/ tests/internal/db/ go.mod
git commit -m "feat: add database schema and models"
```

### Task 1.4: Implement Basic Job Status API

**Files:**
- Create: `internal/server/api.go` - API handlers
- Create: `internal/server/middleware.go` - Authentication middleware
- Create: `tests/internal/server/api_test.go` - API tests

**Interfaces:**
- Consumes: Database models, configuration
- Produces: REST API endpoints for job status

- [ ] **Step 1: Write the failing test for job status API**

```go
// tests/internal/server/api_test.go
package server

import (
    "net/http"
    "net/http/httptest"
    "testing"
    "github.com/gin-gonic/gin"
)

func TestGetJobsAPI(t *testing.T) {
    // Setup test database with mock data
    db, _ := db.Initialize(":memory:")
    
    server := NewTestServer(db)
    req, _ := http.NewRequest("GET", "/api/jobs", nil)
    resp := httptest.NewRecorder()
    
    server.ServeHTTP(resp, req)
    
    if resp.Code != 200 {
        t.Errorf("Expected status 200, got %d", resp.Code)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./tests/internal/server/api_test.go -v`
Expected: FAIL with "NewTestServer not defined"

- [ ] **Step 3: Write minimal implementation**

```go
// internal/server/api.go
package server

import (
    "github.com/gin-gonic/gin"
    "github.com/wnarutou/gitrieve/internal/config"
    "github.com/wnarutou/gitrieve/internal/db"
)

type API struct {
    config *config.Config
    db     *db.DB
}

func NewAPI(cfg *config.Config, db *db.DB) *API {
    return &API{config: cfg, db: db}
}

func (a *API) GetJobs(c *gin.Context) {
    jobs := make([]Job, 0)
    
    // Query jobs from config
    for _, repo := range a.config.Repository {
        jobs = append(jobs, Job{
            Name: repo.Name,
            URL:  repo.URL,
            Status: "idle",
        })
    }
    
    c.JSON(200, Response{
        Code: 200,
        Data: jobs,
    })
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./tests/internal/server/api_test.go -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/server/api.go internal/server/middleware.go tests/internal/server/api_test.go
git commit -m "feat: implement basic job status API"
```

### Task 1.5: Create Static Frontend Structure

**Files:**
- Create: `web/static/css/main.css` - Main stylesheet
- Create: `web/templates/index.html` - Main page template
- Create: `web/static/js/main.js` - Frontend JavaScript

**Interfaces:**
- Consumes: API responses
- Produces: User interface for job listing

- [ ] **Step 1: Write the failing test for static file serving**

```go
// tests/internal/server/static_test.go
package server

import (
    "net/http"
    "net/http/httptest"
    "testing"
)

func TestStaticFileServing(t *testing.T) {
    server := NewTestServer(nil)
    req, _ := http.NewRequest("GET", "/static/css/main.css", nil)
    resp := httptest.NewRecorder()
    
    server.ServeHTTP(resp, req)
    
    if resp.Code != 200 {
        t.Errorf("Expected status 200, got %d", resp.Code)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./tests/internal/server/static_test.go -v`
Expected: FAIL with "static files not found"

- [ ] **Step 3: Write minimal implementation**

```go
// internal/server/server.go
func (s *Server) setupRoutes() {
    // Static files
    s.router.Static("/static", "./web/static")
    s.router.LoadHTMLGlob("web/templates/*")
    
    // Main page
    s.router.GET("/", func(c *gin.Context) {
        c.HTML(200, "index.html", gin.H{
            "title": "Gitrieve",
        })
    })
    
    // API routes
    api := s.router.Group("/api")
    {
        api.GET("/jobs", s.api.GetJobs)
    }
}
```

- [ ] **Step 4: Create basic HTML template**

```html
<!-- web/templates/index.html -->
<!DOCTYPE html>
<html>
<head>
    <title>{{.title}}</title>
    <link rel="stylesheet" href="/static/css/main.css">
</head>
<body>
    <div class="container">
        <h1>Gitrieve Job List</h1>
        <div id="jobs-list">
            <!-- Jobs will be loaded here -->
        </div>
    </div>
    <script src="/static/js/main.js"></script>
</body>
</html>
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./tests/internal/server/static_test.go -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add web/ internal/server/server.go
git commit -m "feat: add static frontend structure"
```

### Task 1.6: Implement Job Execution Tracking

**Files:**
- Create: `internal/executor/executor.go` - Job execution manager
- Create: `internal/logger/logger.go` - Structured logging
- Create: `tests/internal/executor/executor_test.go` - Executor tests

**Interfaces:**
- Consumes: Job configurations, storage backends
- Produces: Execution records and logs

- [ ] **Step 1: Write the failing test for job execution**

```go
// tests/internal/executor/executor_test.go
package executor

import (
    "testing"
    "github.com/stretchr/testify/assert"
)

func TestExecuteJob(t *testing.T) {
    executor := NewExecutor(nil)
    
    job := Job{
        Name: "test-repo",
        URL:  "https://github.com/test/repo.git",
    }
    
    execution, err := executor.ExecuteJob(job)
    assert.NoError(t, err)
    assert.NotNil(t, execution)
    assert.Equal(t, "test-repo", execution.JobName)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./tests/internal/executor/executor_test.go -v`
Expected: FAIL with "executor not defined"

- [ ] **Step 3: Write minimal implementation**

```go
// internal/executor/executor.go
package executor

import (
    "time"
    "github.com/google/uuid"
)

type Execution struct {
    ID        string
    JobName   string
    StartTime time.Time
    EndTime   time.Time
    Status    string
}

type Executor struct {
    logger *Logger
}

func NewExecutor(logger *Logger) *Executor {
    return &Executor{logger: logger}
}

func (e *Executor) ExecuteJob(job Job) (*Execution, error) {
    execution := &Execution{
        ID:        uuid.New().String(),
        JobName:   job.Name,
        StartTime: time.Now(),
        Status:    "running",
    }
    
    // Log start
    e.logger.Log(execution.ID, job.Name, "info", "Starting job execution")
    
    // Simulate execution
    time.Sleep(100 * time.Millisecond)
    
    execution.EndTime = time.Now()
    execution.Status = "success"
    
    e.logger.Log(execution.ID, job.Name, "info", "Job completed successfully")
    
    return execution, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./tests/internal/executor/executor_test.go -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/executor/ internal/logger/ tests/internal/executor/
git commit -m "feat: implement job execution tracking"
```

## Phase 2: Advanced Features (Week 3-4)

### Phase 2 Overview
Add advanced features including real-time logging, configuration management, and detailed job history.

### Task 2.1: Implement Real-time Logging with SSE

**Files:**
- Create: `internal/server/sse.go` - Server-Sent Events handler
- Create: `web/static/js/log-stream.js` - Log streaming client
- Update: `internal/logger/logger.go` - Add log streaming support

**Interfaces:**
- Consumes: Log entries from job executions
- Produces: Real-time log streams via SSE

- [ ] **Step 1: Write the failing test for SSE endpoint**

```go
// tests/internal/server/sse_test.go
package server

import (
    "net/http"
    "net/http/httptest"
    "testing"
)

func TestLogStreamEndpoint(t *testing.T) {
    server := NewTestServer(nil)
    req, _ := http.NewRequest("GET", "/api/logs/test-id/tail", nil)
    resp := httptest.NewRecorder()
    
    server.ServeHTTP(resp, req)
    
    if resp.Code != 200 {
        t.Errorf("Expected status 200, got %d", resp.Code)
    }
    
    if resp.Header().Get("Content-Type") != "text/event-stream" {
        t.Errorf("Expected Content-Type: text/event-stream, got %s", resp.Header().Get("Content-Type"))
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./tests/internal/server/sse_test.go -v`
Expected: FAIL with "SSE endpoint not implemented"

- [ ] **Step 3: Write minimal implementation**

```go
// internal/server/sse.go
package server

import (
    "github.com/gin-gonic/gin"
    "github.com/wnarutou/gitrieve/internal/logger"
)

func (s *Server) setupSSE() {
    s.router.GET("/api/logs/:execution_id/tail", func(c *gin.Context) {
        executionID := c.Param("execution_id")
        
        c.Header("Content-Type", "text/event-stream")
        c.Header("Cache-Control", "no-cache")
        c.Header("Connection", "keep-alive")
        
        // Create log stream
        stream := s.logger.Subscribe(executionID)
        
        for log := range stream {
            c.SSEvent("log", log)
            c.Flush()
        }
    })
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./tests/internal/server/sse_test.go -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/server/sse.go web/static/js/log-stream.js
git commit -m "feat: implement real-time logging with SSE"
```

### Task 2.2: Add Configuration Management API

**Files:**
- Create: `internal/config/api.go` - Configuration management handlers
- Create: `internal/config/validator.go` - Configuration validation
- Create: `tests/internal/config/api_test.go` - Configuration API tests

**Interfaces:**
- Consumes: YAML configuration files
- Produces: CRUD operations for repository configurations

- [ ] **Step 1: Write the failing test for configuration API**

```go
// tests/internal/config/api_test.go
package config

import (
    "net/http"
    "net/http/httptest"
    "testing"
    "github.com/gin-gonic/gin"
)

func TestCreateConfig(t *testing.T) {
    server := NewTestServer()
    req := `{"name": "test-repo", "url": "https://github.com/test/repo.git"}`
    
    req, _ := http.NewRequest("POST", "/api/config", bytes.NewBufferString(req))
    resp := httptest.NewRecorder()
    
    server.ServeHTTP(resp, req)
    
    if resp.Code != 201 {
        t.Errorf("Expected status 201, got %d", resp.Code)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./tests/internal/config/api_test.go -v`
Expected: FAIL with "configuration API not implemented"

- [ ] **Step 3: Write minimal implementation**

```go
// internal/config/api.go
package config

import (
    "github.com/gin-gonic/gin"
    "github.com/wnarutou/gitrieve/internal/typedef"
)

type ConfigAPI struct {
    config *typedef.Config
}

func NewConfigAPI(cfg *typedef.Config) *ConfigAPI {
    return &ConfigAPI{config: cfg}
}

func (c *ConfigAPI) CreateConfig(ctx *gin.Context) {
    var req RepositoryConfig
    if err := ctx.ShouldBindJSON(&req); err != nil {
        ctx.JSON(400, Response{Code: 400, Error: err.Error()})
        return
    }
    
    // Validate config
    if err := c.validateConfig(req); err != nil {
        ctx.JSON(400, Response{Code: 400, Error: err.Error()})
        return
    }
    
    // Add to config
    newRepo := typedef.Repository{
        Name: req.Name,
        URL:  req.URL,
        Cron: req.Cron,
    }
    
    c.config.Repository = append(c.config.Repository, newRepo)
    
    ctx.JSON(201, Response{
        Code: 201,
        Data: newRepo,
        Message: "Created",
    })
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./tests/internal/config/api_test.go -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/config/api.go internal/config/validator.go tests/internal/config/
git commit -m "feat: add configuration management API"
```

### Task 2.3: Implement Configuration Persistence

**Files:**
- Create: `internal/config/persistence.go` - Config file I/O
- Update: `internal/config/api.go` - Add save/load functionality
- Create: `tests/internal/config/persistence_test.go` - Persistence tests

**Interfaces:**
- Consumes: Viper configuration system
- Produces: Persistent configuration storage

- [ ] **Step 1: Write the failing test for config persistence**

```go
// tests/internal/config/persistence_test.go
package config

import (
    "testing"
    "github.com/stretchr/testify/assert"
    "github.com/spf13/viper"
)

func TestSaveAndLoadConfig(t *testing.T) {
    cfg := NewPersistence()
    
    // Test save
    err := cfg.Save(map[string]interface{}{
        "repository": []map[string]interface{}{
            {
                "name": "test-repo",
                "url":  "https://github.com/test/repo.git",
            },
        },
    })
    assert.NoError(t, err)
    
    // Test load
    data, err := cfg.Load()
    assert.NoError(t, err)
    assert.NotNil(t, data)
    assert.Equal(t, "test-repo", data["repository"].([]interface{})[0].(map[string]interface{})["name"])
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./tests/internal/config/persistence_test.go -v`
Expected: FAIL with "persistence not implemented"

- [ ] **Step 3: Write minimal implementation**

```go
// internal/config/persistence.go
package config

import (
    "github.com/spf13/viper"
    "os"
    "path/filepath"
)

type Persistence struct {
    configPath string
}

func NewPersistence() *Persistence {
    return &Persistence{
        configPath: "config.yaml",
    }
}

func (p *Persistence) Save(data map[string]interface{}) error {
    vp := viper.New()
    for key, value := range data {
        vp.Set(key, value)
    }
    
    // Create directory if not exists
    dir := filepath.Dir(p.configPath)
    if err := os.MkdirAll(dir, 0755); err != nil {
        return err
    }
    
    return vp.WriteConfigAs(p.configPath)
}

func (p *Persistence) Load() (map[string]interface{}, error) {
    vp := viper.New()
    vp.SetConfigFile(p.configPath)
    
    if err := vp.ReadInConfig(); err != nil {
        return nil, err
    }
    
    data := make(map[string]interface{})
    for _, key := range vp.AllKeys() {
        data[key] = vp.Get(key)
    }
    
    return data, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./tests/internal/config/persistence_test.go -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/config/persistence.go tests/internal/config/persistence_test.go
git commit -m "feat: implement configuration persistence"
```

### Task 2.4: Create Configuration Management UI

**Files:**
- Create: `web/templates/config.html` - Configuration management page
- Create: `web/static/js/config.js` - Configuration management JavaScript
- Update: `web/static/css/main.css` - Add configuration styles

**Interfaces:**
- Consumes: Configuration API endpoints
- Produces: User interface for managing configurations

- [ ] **Step 1: Write the failing test for config page**

```go
// tests/web/config_test.go
package web

import (
    "net/http"
    "net/http/httptest"
    "testing"
)

func TestConfigPage(t *testing.T) {
    server := NewTestServer()
    req, _ := http.NewRequest("GET", "/config", nil)
    resp := httptest.NewRecorder()
    
    server.ServeHTTP(resp, req)
    
    if resp.Code != 200 {
        t.Errorf("Expected status 200, got %d", resp.Code)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./tests/web/config_test.go -v`
Expected: FAIL with "config page not found"

- [ ] **Step 3: Write minimal implementation**

```go
// web/templates/config.html
<!DOCTYPE html>
<html>
<head>
    <title>Configuration - Gitrieve</title>
    <link rel="stylesheet" href="/static/css/main.css">
</head>
<body>
    <div class="container">
        <h1>Configuration Management</h1>
        <div class="toolbar">
            <button id="add-config">Add Configuration</button>
            <button id="import-config">Import</button>
            <button id="export-config">Export</button>
        </div>
        <table id="config-table">
            <thead>
                <tr>
                    <th>Name</th>
                    <th>URL</th>
                    <th>Cron</th>
                    <th>Actions</th>
                </tr>
            </thead>
            <tbody></tbody>
        </table>
    </div>
    <script src="/static/js/config.js"></script>
</body>
</html>
```

- [ ] **Step 4: Add route to server**

```go
// internal/server/server.go
func (s *Server) setupRoutes() {
    // ... existing routes
    
    s.router.GET("/config", func(c *gin.Context) {
        c.HTML(200, "config.html", gin.H{
            "title": "Configuration - Gitrieve",
        })
    })
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./tests/web/config_test.go -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add web/templates/config.html web/static/js/config.js web/static/css/main.css
git commit -m "feat: add configuration management UI"
```

## Phase 3: Polish and Production Ready (Week 5-6)

### Phase 3 Overview
Add production-ready features including authentication, monitoring, documentation, and deployment improvements.

### Task 3.1: Implement Authentication Middleware

**Files:**
- Update: `internal/server/middleware.go` - Add authentication
- Create: `internal/auth/auth.go` - Authentication logic
- Create: `tests/internal/auth/auth_test.go` - Authentication tests

**Interfaces:**
- Consumes: Configuration tokens
- Produces: Protected routes and authentication checks

- [ ] **Step 1: Write the failing test for authentication**

```go
// tests/internal/auth/auth_test.go
package auth

import (
    "net/http"
    "net/http/httptest"
    "testing"
)

func TestAuthenticationMiddleware(t *testing.T) {
    middleware := NewAuthMiddleware("secret-token")
    
    handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(200)
    }))
    
    req, _ := http.NewRequest("GET", "/", nil)
    resp := httptest.NewRecorder()
    
    handler.ServeHTTP(resp, req)
    
    if resp.Code != 401 {
        t.Errorf("Expected status 401, got %d", resp.Code)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./tests/internal/auth/auth_test.go -v`
Expected: FAIL with "authentication not implemented"

- [ ] **Step 3: Write minimal implementation**

```go
// internal/auth/auth.go
package auth

import (
    "strings"
    "github.com/gin-gonic/gin"
)

type AuthMiddleware struct {
    token string
}

func NewAuthMiddleware(token string) *AuthMiddleware {
    return &AuthMiddleware{token: token}
}

func (a *AuthMiddleware) Middleware() gin.HandlerFunc {
    return func(c *gin.Context) {
        authHeader := c.GetHeader("Authorization")
        if authHeader == "" {
            c.JSON(401, gin.H{"error": "Authorization header required"})
            c.Abort()
            return
        }
        
        token := strings.TrimPrefix(authHeader, "Bearer ")
        if token != a.token {
            c.JSON(401, gin.H{"error": "Invalid token"})
            c.Abort()
            return
        }
        
        c.Next()
    }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./tests/internal/auth/auth_test.go -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/auth/ tests/internal/auth/ internal/server/middleware.go
git commit -m "feat: add authentication middleware"
```

### Task 3.2: Add Monitoring and Health Checks

**Files:**
- Create: `internal/monitoring/monitoring.go` - Health checks and metrics
- Create: `internal/monitoring/metrics.go` - Metrics collection
- Update: `internal/server/api.go` - Add health check endpoints

**Interfaces:**
- Consumes: System metrics and status
- Produces: Health check endpoints and monitoring data

- [ ] **Step 1: Write the failing test for health checks**

```go
// tests/internal/monitoring/monitoring_test.go
package monitoring

import (
    "net/http"
    "net/http/httptest"
    "testing"
)

func TestHealthCheck(t *testing.T) {
    monitor := NewMonitor()
    
    req, _ := http.NewRequest("GET", "/health", nil)
    resp := httptest.NewRecorder()
    
    monitor.HealthCheck(resp, req)
    
    if resp.Code != 200 {
        t.Errorf("Expected status 200, got %d", resp.Code)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./tests/internal/monitoring/monitoring_test.go -v`
Expected: FAIL with "monitoring not implemented"

- [ ] **Step 3: Write minimal implementation**

```go
// internal/monitoring/monitoring.go
package monitoring

import (
    "time"
    "github.com/gin-gonic/gin"
)

type Monitor struct {
    startTime time.Time
}

func NewMonitor() *Monitor {
    return &Monitor{
        startTime: time.Now(),
    }
}

func (m *Monitor) HealthCheck(c *gin.Context) {
    uptime := time.Since(m.startTime)
    
    c.JSON(200, gin.H{
        "status": "ok",
        "uptime": uptime.String(),
        "timestamp": time.Now(),
    })
}

func (m *Monitor) GetMetrics() gin.H {
    return gin.H{
        "uptime": time.Since(m.startTime).String(),
        "timestamp": time.Now(),
    }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./tests/internal/monitoring/monitoring_test.go -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/monitoring/ tests/internal/monitoring/ internal/server/api.go
git commit -m "feat: add monitoring and health checks"
```

### Task 3.3: Create Docker and Deployment Configuration

**Files:**
- Create: `Dockerfile` - Container configuration
- Create: `docker-compose.yml` - Docker Compose setup
- Update: `Makefile` - Build and deployment targets

**Interfaces:**
- Consumes: Go application and dependencies
- Produces: Containerized deployment artifacts

- [ ] **Step 1: Write the failing test for Docker build**

```bash
# tests/docker/Dockerfile.test
FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o gitrieve main.go

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /app/gitrieve .
COPY --from=builder /app/web ./web
EXPOSE 8080
CMD ["./gitrieve", "server"]
```

- [ ] **Step 2: Test Docker build**

```bash
docker build -t gitrieve:test -f tests/docker/Dockerfile.test .
```
Expected: FAIL with build errors

- [ ] **Step 3: Create proper Dockerfile**

```dockerfile
# Dockerfile
FROM golang:1.23-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o gitrieve main.go

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/

COPY --from=builder /app/gitrieve .
COPY --from=builder /app/web ./web

EXPOSE 8080
CMD ["./gitrieve", "server", "--port", "8080"]
```

- [ ] **Step 4: Test Docker build**

```bash
docker build -t gitrieve:test .
```
Expected: SUCCESS

- [ ] **Step 5: Commit**

```bash
git add Dockerfile docker-compose.yml Makefile
git commit -m "feat: add Docker deployment configuration"
```

### Task 3.4: Add Documentation and README Updates

**Files:**
- Update: `README.md` - Add Web UI documentation
- Create: `docs/web-ui.md` - Web UI user guide
- Create: `docs/api.md` - API documentation

**Interfaces:**
- Consumes: Implementation details
- Produces: User and developer documentation

- [ ] **Step 1: Write documentation for Web UI**

```markdown
# docs/web-ui.md

# Gitrieve Web UI Guide

## Starting the Web Server

```bash
gitrieve server --port 8080
```

## Accessing the Web UI

Open your browser and navigate to `http://localhost:8080`

## Features

- Job Management: View, create, edit, and delete backup jobs
- Real-time Monitoring: Watch job execution with live logs
- Configuration Management: Import/export job configurations
- Execution History: View past job runs and their logs
```

- [ ] **Step 2: Update README.md**

```markdown
# Gitrieve

[Previous content...]

## Web UI

Gitrieve includes a web UI for managing backup jobs and monitoring execution.

### Starting the Web Server

```bash
# Start with default config
gitrieve server

# Start with custom port
gitrieve server --port 8080

# Start with authentication
gitrieve server --auth-token your-secret-token
```

### Accessing the Web UI

Open your browser to `http://localhost:8080` (or your configured port).

### Web UI Features

- **Job Management**: View, create, edit, and delete backup jobs
- **Real-time Monitoring**: Watch job execution with live logs
- **Configuration Management**: Import/export job configurations
- **Execution History**: View past job runs and their logs
```

- [ ] **Step 3: Commit**

```bash
git add README.md docs/web-ui.md docs/api.md
git commit -m "docs: add Web UI documentation"
```

## Timeline Estimates

| Phase | Tasks | Duration |
|-------|-------|----------|
| Phase 1 (MVP) | 6 tasks | 1-2 weeks |
| Phase 2 (Advanced) | 4 tasks | 1-2 weeks |
| Phase 3 (Production) | 4 tasks | 1 week |
| **Total** | **14 tasks** | **3-5 weeks** |

## Testing Strategy

- **Unit Tests**: Each component has corresponding unit tests
- **Integration Tests**: Test API endpoints and database interactions
- **E2E Tests**: Test complete user workflows
- **Performance Tests**: Test under concurrent load

## Deployment Considerations

- Single binary deployment with embedded assets
- Docker containerization for easy deployment
- systemd service for production deployment
- Configuration file management for production settings

## Success Criteria

- Web UI fully functional with all planned features
- All tests passing (unit, integration, e2e)
- Performance benchmarks met
- Documentation complete and up-to-date
- Deployment guide available