package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/wnarutou/gitrieve/internal/auth"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// newTestRouter builds a gin engine with the given middleware and a dummy
// 200 OK handler, for testing auth behavior.
func newTestRouter(mw gin.HandlerFunc) *gin.Engine {
	r := gin.New()
	r.Use(mw)
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"code": 200})
	})
	return r
}

func TestAuthMiddleware_NoHeader_Returns401(t *testing.T) {
	mw := auth.NewAuthMiddleware("secret-token").Middleware()
	router := newTestRouter(mw)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

func TestAuthMiddleware_ValidToken_PassesThrough(t *testing.T) {
	mw := auth.NewAuthMiddleware("secret-token").Middleware()
	router := newTestRouter(mw)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}
}

func TestAuthMiddleware_WrongToken_Returns401(t *testing.T) {
	mw := auth.NewAuthMiddleware("secret-token").Middleware()
	router := newTestRouter(mw)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

func TestAuthMiddleware_EmptyToken_IsPassThrough(t *testing.T) {
	mw := auth.NewAuthMiddleware("").Middleware()
	router := newTestRouter(mw)

	// No Authorization header at all, but should still pass through.
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d for empty token, got %d", http.StatusOK, w.Code)
	}
}
