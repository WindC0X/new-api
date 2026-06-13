package router

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-contrib/gzip"
	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
)

const (
	creativeNoCacheControl        = "no-cache"
	creativeImmutableCacheControl = "public, max-age=31536000, immutable"
	creativeEmbeddedCSP           = "frame-ancestors 'self'; base-uri 'self'; object-src 'none'"
	creativePermissionsPolicy     = "camera=(), microphone=(), geolocation=(), payment=()"
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

func creativeNoStore() gin.HandlerFunc {
	return func(c *gin.Context) {
		setCreativeNoStoreHeaders(c)
		c.Next()
	}
}

func setCreativeNoStoreHeaders(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")
}

func creativeRouteNotFound(c *gin.Context) {
	controller.RelayNotFound(c)
}

func SetCreativeRouter(router *gin.Engine) {
	creativeAPIRouter := router.Group("/creative/api")
	creativeAPIRouter.Use(middleware.RouteTag("api"))
	creativeAPIRouter.Use(creativeNoStore())
	creativeAPIRouter.Use(middleware.BodyStorageCleanup())
	creativeAPIRouter.Use(middleware.CreativeSessionHeaderBridge(), middleware.UserAuth())
	creativeAPIRouter.Use(middleware.CreativeRejectCrossOriginWhenPresent())
	{
		creativeAPIRouter.GET("/bootstrap", controller.CreativeBootstrap)
		creativeAPIRouter.GET("/models", controller.CreativeListModels)
		creativeAPIRouter.GET("/preferences/model", controller.CreativeGetModelPreference)
		creativeAPIRouter.PATCH("/preferences/model", middleware.CreativeRequireNonce(), controller.CreativePatchModelPreference)
		creativeAPIRouter.GET("/documents", controller.CreativeListDocuments)
		creativeAPIRouter.POST("/documents", middleware.CreativeRequireNonce(), controller.CreativeCreateDocument)
		creativeAPIRouter.GET("/documents/:id", controller.CreativeGetDocument)
		creativeAPIRouter.PUT("/documents/:id", middleware.CreativeRequireNonce(), controller.CreativeUpdateDocument)
		creativeAPIRouter.DELETE("/documents/:id", middleware.CreativeRequireNonce(), controller.CreativeDeleteDocument)
		creativeAPIRouter.POST("/assets", middleware.CreativeRequireNonce(), controller.CreativeUploadAsset)
		creativeAPIRouter.GET("/assets/:id", controller.CreativeGetAsset)
		creativeAPIRouter.GET("/assets/:id/content", controller.CreativeGetAssetContent)
		creativeAPIRouter.DELETE("/assets/:id", middleware.CreativeRequireNonce(), controller.CreativeDeleteAsset)

		creativeAPIRouter.GET("/bootstrap/", creativeRouteNotFound)
		creativeAPIRouter.GET("/models/", creativeRouteNotFound)
		creativeAPIRouter.GET("/preferences/model/", creativeRouteNotFound)
		creativeAPIRouter.PATCH("/preferences/model/", creativeRouteNotFound)
		creativeAPIRouter.GET("/documents/", creativeRouteNotFound)
		creativeAPIRouter.POST("/documents/", creativeRouteNotFound)
		creativeAPIRouter.GET("/documents/:id/", creativeRouteNotFound)
		creativeAPIRouter.PUT("/documents/:id/", creativeRouteNotFound)
		creativeAPIRouter.DELETE("/documents/:id/", creativeRouteNotFound)
		creativeAPIRouter.POST("/assets/", creativeRouteNotFound)
		creativeAPIRouter.GET("/assets/:id/", creativeRouteNotFound)
		creativeAPIRouter.GET("/assets/:id/content/", creativeRouteNotFound)
		creativeAPIRouter.DELETE("/assets/:id/", creativeRouteNotFound)
	}

	creativeRelayRouter := router.Group("/creative/relay/v1")
	creativeRelayRouter.Use(middleware.RouteTag("relay"))
	creativeRelayRouter.Use(creativeNoStore())
	creativeRelayRouter.Use(middleware.BodyStorageCleanup())
	creativeRelayRouter.Use(middleware.SystemPerformanceCheck())
	creativeRelayRouter.Use(middleware.CreativeSessionHeaderBridge(), middleware.UserAuth())
	creativeRelayRouter.Use(middleware.ModelRequestRateLimit())
	creativeRelayRouter.Use(middleware.CreativeRequireSameOrigin())
	creativeRelayRouter.Use(middleware.CreativeRequireNonce())
	creativeRelayRouter.Use(controller.CreativeRejectForbiddenRelayFields())
	{
		creativeDistributedRelayRouter := creativeRelayRouter.Group("")
		creativeDistributedRelayRouter.Use(middleware.CreativeRelaySessionBroker(), middleware.Distribute())
		creativeDistributedRelayRouter.POST("/chat/completions", controller.CreativeRelayChatCompletions)
		creativeDistributedRelayRouter.POST("/images/generations", controller.CreativeRelayImagesGenerations)
		creativeDistributedRelayRouter.POST("/chat/completions/", creativeRouteNotFound)
		creativeDistributedRelayRouter.POST("/images/generations/", creativeRouteNotFound)

		creativeVideoRelayRouter := creativeRelayRouter.Group("/videos")
		creativeVideoRelayRouter.Use(controller.CreativeVideoRelayGate())
		creativeVideoRelayRouter.Use(controller.CreativeVideoSubmitIdempotency())
		creativeVideoRelayRouter.Use(middleware.CreativeRelaySessionBroker(), middleware.Distribute())
		creativeVideoRelayRouter.POST("", controller.CreativeRelayVideos)
		creativeVideoRelayRouter.GET("/:task_id", controller.CreativeRelayVideoFetch)
		creativeVideoRelayRouter.GET("/:task_id/content", controller.CreativeRelayVideoContent)
		creativeVideoRelayRouter.POST("/", creativeRouteNotFound)
		creativeVideoRelayRouter.GET("/:task_id/", creativeRouteNotFound)
		creativeVideoRelayRouter.GET("/:task_id/content/", creativeRouteNotFound)

		creativeSunoRelayRouter := creativeRelayRouter.Group("/suno")
		creativeSunoRelayRouter.POST("/submit/:action", controller.CreativeSunoSubmitGuard(), middleware.CreativeRelaySessionBroker(), middleware.Distribute(), controller.CreativeRelaySunoSubmit)
		creativeSunoRelayRouter.GET("/fetch/:id", middleware.CreativeRelaySessionBroker(), middleware.Distribute(), controller.CreativeRelaySunoFetch)
		creativeSunoRelayRouter.POST("/fetch", middleware.CreativeRelaySessionBroker(), middleware.Distribute(), controller.CreativeRelaySunoFetch)
		creativeSunoRelayRouter.POST("/submit/:action/", creativeRouteNotFound)
		creativeSunoRelayRouter.GET("/fetch/:id/", creativeRouteNotFound)
		creativeSunoRelayRouter.POST("/fetch/", creativeRouteNotFound)

		creativeMJRelayRouter := creativeRelayRouter.Group("/mj")
		creativeMJRelayRouter.POST("/submit/imagine", controller.CreativeMJSubmitImagineGuard(), middleware.CreativeRelaySessionBroker(), middleware.Distribute(), controller.CreativeRelayMJSubmitImagine)
		creativeMJRelayRouter.GET("/task/:task_id/fetch", middleware.CreativeRelaySessionBroker(), middleware.Distribute(), controller.CreativeRelayMJFetch)
		creativeMJRelayRouter.POST("/task/list-by-condition", middleware.CreativeRelaySessionBroker(), middleware.Distribute(), controller.CreativeRelayMJListByCondition)
		creativeMJRelayRouter.GET("/image/:task_id", middleware.CreativeRelaySessionBroker(), middleware.Distribute(), controller.CreativeRelayMJImage)
		creativeMJRelayRouter.POST("/submit/action", middleware.CreativeRelaySessionBroker(), controller.CreativeRelayMJUnsupported)
		creativeMJRelayRouter.POST("/submit/change", middleware.CreativeRelaySessionBroker(), controller.CreativeRelayMJUnsupported)
		creativeMJRelayRouter.POST("/submit/simple-change", middleware.CreativeRelaySessionBroker(), controller.CreativeRelayMJUnsupported)
		creativeMJRelayRouter.POST("/submit/modal", middleware.CreativeRelaySessionBroker(), controller.CreativeRelayMJUnsupported)
		creativeMJRelayRouter.POST("/submit/shorten", middleware.CreativeRelaySessionBroker(), controller.CreativeRelayMJUnsupported)
		creativeMJRelayRouter.POST("/submit/blend", middleware.CreativeRelaySessionBroker(), controller.CreativeRelayMJUnsupported)
		creativeMJRelayRouter.POST("/submit/describe", middleware.CreativeRelaySessionBroker(), controller.CreativeRelayMJUnsupported)
		creativeMJRelayRouter.POST("/submit/edits", middleware.CreativeRelaySessionBroker(), controller.CreativeRelayMJUnsupported)
		creativeMJRelayRouter.POST("/submit/video", middleware.CreativeRelaySessionBroker(), controller.CreativeRelayMJUnsupported)
		creativeMJRelayRouter.POST("/submit/upload-discord-images", middleware.CreativeRelaySessionBroker(), controller.CreativeRelayMJUnsupported)
		creativeMJRelayRouter.POST("/insight-face/swap", middleware.CreativeRelaySessionBroker(), controller.CreativeRelayMJUnsupported)
		creativeMJRelayRouter.GET("/task/:task_id/image-seed", middleware.CreativeRelaySessionBroker(), controller.CreativeRelayMJUnsupported)
		creativeMJRelayRouter.POST("/submit/imagine/", creativeRouteNotFound)
		creativeMJRelayRouter.GET("/task/:task_id/fetch/", creativeRouteNotFound)
		creativeMJRelayRouter.POST("/task/list-by-condition/", creativeRouteNotFound)
		creativeMJRelayRouter.GET("/image/:task_id/", creativeRouteNotFound)
		creativeMJRelayRouter.POST("/submit/action/", creativeRouteNotFound)
		creativeMJRelayRouter.POST("/submit/change/", creativeRouteNotFound)
		creativeMJRelayRouter.POST("/submit/simple-change/", creativeRouteNotFound)
		creativeMJRelayRouter.POST("/submit/modal/", creativeRouteNotFound)
		creativeMJRelayRouter.POST("/submit/shorten/", creativeRouteNotFound)
		creativeMJRelayRouter.POST("/submit/blend/", creativeRouteNotFound)
		creativeMJRelayRouter.POST("/submit/describe/", creativeRouteNotFound)
		creativeMJRelayRouter.POST("/submit/edits/", creativeRouteNotFound)
		creativeMJRelayRouter.POST("/submit/video/", creativeRouteNotFound)
		creativeMJRelayRouter.POST("/submit/upload-discord-images/", creativeRouteNotFound)
		creativeMJRelayRouter.POST("/insight-face/swap/", creativeRouteNotFound)
		creativeMJRelayRouter.GET("/task/:task_id/image-seed/", creativeRouteNotFound)
	}
}

func SetWebRouter(router *gin.Engine, assets ThemeAssets) {
	defaultFS := common.EmbedFolder(assets.DefaultBuildFS, "web/default/dist")
	classicFS := common.EmbedFolder(assets.ClassicBuildFS, "web/classic/dist")
	creativeFS := common.EmbedFolder(assets.CreativeBuildFS, "web/creative/dist")
	themeFS := common.NewThemeAwareFS(defaultFS, classicFS)
	creativeProvenance := loadCreativeProvenanceHeaders(creativeFS)

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

	SetCreativeRouter(router)

	serveCreative := func(c *gin.Context) {
		p := c.Request.URL.Path
		setCreativeEmbeddedHeaders(c, creativeProvenance)

		// /creative/api and /creative/relay are handled by explicit routes above.
		if p == "/creative/api" || strings.HasPrefix(p, "/creative/api/") ||
			p == "/creative/relay" || strings.HasPrefix(p, "/creative/relay/") {
			controller.RelayNotFound(c)
			return
		}
		// SW-critical files must not be cached by the global Cache() middleware.
		if creativeNoCachePath(p) {
			c.Header("Cache-Control", creativeNoCacheControl)
		}
		// SPA root — serve the embedded opentu index.html.
		stripped := strings.TrimPrefix(p, "/creative")
		if stripped == "" || stripped == "/" || stripped == "/index.html" {
			c.Header("Cache-Control", creativeNoCacheControl)
			c.Data(http.StatusOK, "text/html; charset=utf-8", assets.CreativeIndexPage)
			return
		}
		// Serve real files (hashed JS/CSS, sw.js, manifest, precache manifests)
		// with SPA fallback for client-side navigation paths.
		// Reject directories to prevent accidental directory listing.
		if fi, err := creativeFS.Open(stripped); err == nil {
			if s, se := fi.Stat(); se == nil && s.IsDir() {
				fi.Close()
				c.Header("Cache-Control", creativeNoCacheControl)
				c.Data(http.StatusOK, "text/html; charset=utf-8", assets.CreativeIndexPage)
				return
			}
			fi.Close()
			if creativeImmutableAssetPath(p) {
				c.Header("Cache-Control", creativeImmutableCacheControl)
			}
			creativeServer.ServeHTTP(c.Writer, c.Request)
			return
		}
		// File not found — SPA fallback for client-side routes.
		c.Header("Cache-Control", creativeNoCacheControl)
		c.Data(http.StatusOK, "text/html; charset=utf-8", assets.CreativeIndexPage)
	}

	router.GET("/creative", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/creative/")
	})
	router.GET("/creative/", serveCreative)

	router.NoRoute(func(c *gin.Context) {
		c.Set(middleware.RouteTagKey, "web")
		if strings.HasPrefix(c.Request.URL.Path, "/creative/") {
			serveCreative(c)
			return
		}
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

type creativeVersionMetadata struct {
	Version   string `json:"version"`
	BuildTime string `json:"buildTime"`
	GitCommit string `json:"gitCommit"`
}

type creativeProvenanceHeaders struct {
	versionHash string
	version     string
	buildTime   string
	gitCommit   string
}

type creativeFileOpener interface {
	Open(name string) (http.File, error)
}

func loadCreativeProvenanceHeaders(creativeFS creativeFileOpener) creativeProvenanceHeaders {
	file, err := creativeFS.Open("/version.json")
	if err != nil {
		return creativeProvenanceHeaders{}
	}
	defer file.Close()

	versionBytes, err := io.ReadAll(file)
	if err != nil || len(versionBytes) == 0 {
		return creativeProvenanceHeaders{}
	}
	hash := sha256.Sum256(versionBytes)
	provenance := creativeProvenanceHeaders{
		versionHash: "sha256:" + fmt.Sprintf("%x", hash),
	}
	var metadata creativeVersionMetadata
	if err := common.Unmarshal(versionBytes, &metadata); err == nil {
		provenance.version = safeCreativeHeaderValue(metadata.Version)
		provenance.buildTime = safeCreativeHeaderValue(metadata.BuildTime)
		provenance.gitCommit = safeCreativeHeaderValue(metadata.GitCommit)
	}
	return provenance
}

func setCreativeEmbeddedHeaders(c *gin.Context, provenance creativeProvenanceHeaders) {
	c.Header("Content-Security-Policy", creativeEmbeddedCSP)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Referrer-Policy", "origin-when-cross-origin")
	c.Header("Permissions-Policy", creativePermissionsPolicy)
	if provenance.versionHash != "" {
		c.Header("X-Creative-Version-Hash", provenance.versionHash)
	}
	if provenance.version != "" {
		c.Header("X-Creative-Build-Version", provenance.version)
	}
	if provenance.buildTime != "" {
		c.Header("X-Creative-Build-Time", provenance.buildTime)
	}
	if provenance.gitCommit != "" {
		c.Header("X-Creative-Git-Commit", provenance.gitCommit)
	}
}

func creativeNoCachePath(path string) bool {
	switch path {
	case "/creative/", "/creative/index.html", "/creative/sw.js", "/creative/version.json":
		return true
	default:
		return false
	}
}

func creativeImmutableAssetPath(path string) bool {
	return strings.HasPrefix(path, "/creative/assets/")
}

func safeCreativeHeaderValue(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "")
	return value
}
