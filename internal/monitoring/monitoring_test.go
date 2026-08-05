package monitoring_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/wnarutou/gitrieve/internal/monitoring"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestHealthCheck_Returns200AndStatusOK(t *testing.T) {
	monitor := monitoring.NewMonitor()
	r := gin.New()
	r.GET("/health", monitor.HealthCheck)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	var body struct {
		Code int `json:"code"`
		Data struct {
			Status    string `json:"status"`
			Uptime    string `json:"uptime"`
			Timestamp string `json:"timestamp"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if body.Data.Status != "ok" {
		t.Fatalf("expected status 'ok', got %q", body.Data.Status)
	}
	if body.Data.Uptime == "" {
		t.Fatal("expected non-empty uptime")
	}
	if body.Data.Timestamp == "" {
		t.Fatal("expected non-empty timestamp")
	}
}

func TestGetMetrics_Returns200AndGoroutines(t *testing.T) {
	monitor := monitoring.NewMonitor()
	r := gin.New()
	r.GET("/api/metrics", monitor.GetMetrics)

	req := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	var body struct {
		Code int `json:"code"`
		Data struct {
			Uptime     string `json:"uptime"`
			Timestamp  string `json:"timestamp"`
			Goroutines int    `json:"goroutines"`
			GoVersion  string `json:"go_version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if body.Data.Goroutines <= 0 {
		t.Fatalf("expected goroutines > 0, got %d", body.Data.Goroutines)
	}
	if body.Data.GoVersion == "" {
		t.Fatal("expected non-empty go_version")
	}
}
