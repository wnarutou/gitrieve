package server

import (
	"github.com/gin-gonic/gin"
)

// Middleware placeholder for future authentication
func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Basic middleware - no authentication for now
		c.Next()
	}
}