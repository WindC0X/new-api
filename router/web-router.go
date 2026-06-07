package router

import (
	"embed"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-contrib/gzip"
	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
)

// ThemeAssets holds the embedded frontend assets for both themes and creative.
type ThemeAssets struct {
	DefaultBuildFS    embed.FS
	DefaultIndexPage  []byte
	ClassicBuildFS    embed.FS
	ClassicIndexPage  []byte
	CreativeBuildFS   embed.FS
	CreativeIndexPage []byte
}

func SetWebRouter(router *gin.Engine, assets ThemeAssets) {
	defaultFS := common.EmbedFolder(assets.DefaultBuildFS, "web/default/dist")
	classicFS := common.EmbedFolder(assets.ClassicBuildFS, "web/classic/dist")
	creativeFS := common.EmbedFolder(assets.CreativeBuildFS, "web/creative/dist")
	themeFS := common.NewThemeAwareFS(defaultFS, classicFS)

	router.Use(gzip.Gzip(gzip.DefaultCompression))
	router.Use(middleware.GlobalWebRateLimit())
	router.Use(middleware.Cache())
	router.Use(static.Serve("/", themeFS))

	// /creative — explicit route registration avoids trailing-slash redirect
	// interference from gin's middleware chain and provides a clean SPA fallback
	// for client-side navigation (e.g. /creative/board, /creative/settings).
	// NOTE: admin static.Serve("/", themeFS) runs before these routes because it
	// is a middleware.  If the admin dist ever gains a /creative/ directory, the
	// behaviour here is undefined by design — choose to mount on a unique prefix.
	creativeServer := http.StripPrefix("/creative", http.FileServer(creativeFS))
	serveCreative := func(c *gin.Context) {
		p := c.Request.URL.Path

		// Reserve /creative/relay[/...] for the future session-auth relay.
		if p == "/creative/relay" || strings.HasPrefix(p, "/creative/relay/") {
			controller.RelayNotFound(c)
			return
		}
		// SW-critical files must not be cached by the global Cache() middleware.
		switch p {
		case "/creative/sw.js", "/creative/index.html", "/creative/version.json":
			c.Header("Cache-Control", "no-cache")
		}
		// SPA root — serve the embedded opentu index.html.
		stripped := strings.TrimPrefix(p, "/creative")
		if stripped == "" || stripped == "/" {
			c.Header("Cache-Control", "no-cache")
			c.Data(http.StatusOK, "text/html; charset=utf-8", assets.CreativeIndexPage)
			return
		}
		// Serve real files (hashed JS/CSS, sw.js, manifest, precache manifests)
		// with SPA fallback for client-side navigation paths.
		// Reject directories to prevent accidental directory listing.
		if fi, err := creativeFS.Open(stripped); err == nil {
			if s, se := fi.Stat(); se == nil && s.IsDir() {
				fi.Close()
				c.Header("Cache-Control", "no-cache")
				c.Data(http.StatusOK, "text/html; charset=utf-8", assets.CreativeIndexPage)
				return
			}
			fi.Close()
			creativeServer.ServeHTTP(c.Writer, c.Request)
			return
		}
		// File not found — SPA fallback for client-side routes.
		c.Header("Cache-Control", "no-cache")
		c.Data(http.StatusOK, "text/html; charset=utf-8", assets.CreativeIndexPage)
	}

	router.GET("/creative", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/creative/")
	})
	router.Any("/creative/*filepath", serveCreative)

	router.NoRoute(func(c *gin.Context) {
		c.Set(middleware.RouteTagKey, "web")
		if strings.HasPrefix(c.Request.RequestURI, "/v1") || strings.HasPrefix(c.Request.RequestURI, "/api") || strings.HasPrefix(c.Request.RequestURI, "/assets") {
			controller.RelayNotFound(c)
			return
		}
		c.Header("Cache-Control", "no-cache")
		if common.GetTheme() == "classic" {
			c.Data(http.StatusOK, "text/html; charset=utf-8", assets.ClassicIndexPage)
		} else {
			c.Data(http.StatusOK, "text/html; charset=utf-8", assets.DefaultIndexPage)
		}
	})
}