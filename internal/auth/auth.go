package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// AuthMiddleware provides token-based authentication for protected routes.
type AuthMiddleware struct {
	token string
}

// NewAuthMiddleware creates a new AuthMiddleware with the given bearer token.
func NewAuthMiddleware(token string) *AuthMiddleware {
	return &AuthMiddleware{token: token}
}

// Middleware returns a gin.HandlerFunc that validates the Authorization
// header as a Bearer token. If no token is configured, the middleware is
// a no-op pass-through so authentication is effectively disabled.
func (a *AuthMiddleware) Middleware() gin.HandlerFunc {
	// When no token is configured, auth is disabled — pass through.
	if a.token == "" {
		return func(c *gin.Context) {
			c.Next()
		}
	}

	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"code":    http.StatusUnauthorized,
				"message": "Authorization header required",
			})
			c.Abort()
			return
		}

		// Expect "Bearer <token>"
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			c.JSON(http.StatusUnauthorized, gin.H{
				"code":    http.StatusUnauthorized,
				"message": "Invalid token",
			})
			c.Abort()
			return
		}

		if parts[1] != a.token {
			c.JSON(http.StatusUnauthorized, gin.H{
				"code":    http.StatusUnauthorized,
				"message": "Invalid token",
			})
			c.Abort()
			return
		}

		c.Next()
	}
}
