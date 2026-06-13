package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
)

func Cache() func(c *gin.Context) {
	return func(c *gin.Context) {
		path := ""
		if c.Request != nil && c.Request.URL != nil {
			path = c.Request.URL.Path
		}
		if path == "" && c.Request != nil {
			path = c.Request.RequestURI
		}

		if path == "/" {
			c.Header("Cache-Control", "no-cache")
		} else if path == "/creative/api" || strings.HasPrefix(path, "/creative/api/") ||
			path == "/creative/relay" || strings.HasPrefix(path, "/creative/relay/") {
			c.Header("Cache-Control", "private, no-store")
			c.Header("Pragma", "no-cache")
		} else {
			c.Header("Cache-Control", "max-age=604800") // one week
		}
		c.Header("Cache-Version", "b688f2fb5be447c25e5aa3bd063087a83db32a288bf6a4f35f2d8db310e40b14")
		c.Next()
	}
}
