package router

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"

	"github.com/gin-gonic/gin"
)

func SetRouter(router *gin.Engine, assets ThemeAssets) {
	SetApiRouter(router)
	SetDashboardRouter(router)
	SetRelayRouter(router)
	SetVideoRouter(router)
	frontendBaseUrl := os.Getenv("FRONTEND_BASE_URL")
	if common.IsMasterNode && frontendBaseUrl != "" {
		frontendBaseUrl = ""
		common.SysLog("FRONTEND_BASE_URL is ignored on master node")
	}
	if frontendBaseUrl == "" {
		SetWebRouter(router, assets)
	} else {
		SetCreativeRouter(router)
		frontendBaseUrl = strings.TrimSuffix(frontendBaseUrl, "/")
		router.NoRoute(func(c *gin.Context) {
			path := ""
			if c.Request != nil && c.Request.URL != nil {
				path = c.Request.URL.Path
			}
			if path == "/creative/api" || strings.HasPrefix(path, "/creative/api/") ||
				path == "/creative/relay" || strings.HasPrefix(path, "/creative/relay/") {
				if path == "/creative/api" || strings.HasPrefix(path, "/creative/api/") {
					c.Set(middleware.RouteTagKey, "api")
				} else {
					c.Set(middleware.RouteTagKey, "relay")
				}
				setCreativeNoStoreHeaders(c)
				controller.RelayNotFound(c)
				return
			}
			c.Set(middleware.RouteTagKey, "web")
			c.Redirect(http.StatusMovedPermanently, fmt.Sprintf("%s%s", frontendBaseUrl, c.Request.RequestURI))
		})
	}
}
