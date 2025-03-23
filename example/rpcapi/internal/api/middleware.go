package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// requestLogger is a middleware for logging requests.
func (api *API) requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery

		c.Next() // Process request

		// Log details after the request is handled
		latency := time.Since(start)
		clientIP := c.ClientIP()
		method := c.Request.Method
		statusCode := c.Writer.Status()

		if raw != "" {
			path = path + "?" + raw
		}

		api.logger.Printf("[GIN] %s | %3d | %13v | %15s | %-7s %s",
			time.Now().Format(time.RFC3339),
			statusCode,
			latency,
			clientIP,
			method,
			path,
		)
	}
}
