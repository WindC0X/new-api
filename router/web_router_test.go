package router

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

//go:embed web/default/dist
var testDefaultBuildFS embed.FS

//go:embed web/default/dist/index.html
var testDefaultIndexPage []byte

//go:embed web/classic/dist
var testClassicBuildFS embed.FS

//go:embed web/classic/dist/index.html
var testClassicIndexPage []byte

//go:embed all:web/creative/dist
var testCreativeBuildFS embed.FS

//go:embed web/creative/dist/index.html
var testCreativeIndexPage []byte

func TestSetWebRouterKeepsCreativeRoutesGinSafe(t *testing.T) {
	engine := newCreativeWebTestEngine(t)

	redirect := httptest.NewRecorder()
	engine.ServeHTTP(redirect, httptest.NewRequest(http.MethodGet, "/creative", nil))
	require.Equal(t, http.StatusMovedPermanently, redirect.Code)
	require.Equal(t, "/creative/", redirect.Header().Get("Location"))

	spa := httptest.NewRecorder()
	engine.ServeHTTP(spa, httptest.NewRequest(http.MethodGet, "/creative/board", nil))
	require.Equal(t, http.StatusOK, spa.Code)
	requireCreativeIndexProductMarkup(t, spa.Body.String())

	missingAPI := httptest.NewRecorder()
	engine.ServeHTTP(missingAPI, httptest.NewRequest(http.MethodGet, "/creative/api/missing", nil))
	require.Equal(t, http.StatusNotFound, missingAPI.Code)
	requireNoCreativeFixtureMarkersInText(t, missingAPI.Body.String(), "/creative/api/missing")

	api := httptest.NewRecorder()
	engine.ServeHTTP(api, httptest.NewRequest(http.MethodGet, "/creative/api/bootstrap", nil))
	require.Equal(t, http.StatusUnauthorized, api.Code)
	requireNoCreativeFixtureMarkersInText(t, api.Body.String(), "/creative/api/bootstrap")
	require.Contains(t, api.Header().Get("Content-Type"), "application/json")

	unsafeAPI := httptest.NewRecorder()
	engine.ServeHTTP(unsafeAPI, httptest.NewRequest(http.MethodPatch, "/creative/api/preferences/model", strings.NewReader(`{"baseRevision":0}`)))
	require.Equal(t, http.StatusUnauthorized, unsafeAPI.Code)
	requireNoCreativeFixtureMarkersInText(t, unsafeAPI.Body.String(), "/creative/api/preferences/model")

	relay := httptest.NewRecorder()
	relayRequest := httptest.NewRequest(http.MethodPost, "/creative/relay/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","messages":[]}`))
	relayRequest.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(relay, relayRequest)
	require.Equal(t, http.StatusUnauthorized, relay.Code)
	requireNoCreativeFixtureMarkersInText(t, relay.Body.String(), "/creative/relay/v1/chat/completions")

	imageRelay := httptest.NewRecorder()
	imageRelayRequest := httptest.NewRequest(http.MethodPost, "/creative/relay/v1/images/generations", strings.NewReader(`{"model":"gpt-image-1","prompt":"draw"}`))
	imageRelayRequest.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(imageRelay, imageRelayRequest)
	require.Equal(t, http.StatusUnauthorized, imageRelay.Code)
	requireNoCreativeFixtureMarkersInText(t, imageRelay.Body.String(), "/creative/relay/v1/images/generations")

	videoRelay := httptest.NewRecorder()
	videoRelayRequest := httptest.NewRequest(http.MethodPost, "/creative/relay/v1/videos", strings.NewReader(`{"model":"sora-2","prompt":"draw"}`))
	videoRelayRequest.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(videoRelay, videoRelayRequest)
	require.Equal(t, http.StatusUnauthorized, videoRelay.Code)
	requireNoCreativeFixtureMarkersInText(t, videoRelay.Body.String(), "/creative/relay/v1/videos")

	videoFetch := httptest.NewRecorder()
	engine.ServeHTTP(videoFetch, httptest.NewRequest(http.MethodGet, "/creative/relay/v1/videos/task_abc", nil))
	require.Equal(t, http.StatusUnauthorized, videoFetch.Code)
	requireNoCreativeFixtureMarkersInText(t, videoFetch.Body.String(), "/creative/relay/v1/videos/task_abc")

	videoContent := httptest.NewRecorder()
	engine.ServeHTTP(videoContent, httptest.NewRequest(http.MethodGet, "/creative/relay/v1/videos/task_abc/content", nil))
	require.Equal(t, http.StatusUnauthorized, videoContent.Code)
	requireNoCreativeFixtureMarkersInText(t, videoContent.Body.String(), "/creative/relay/v1/videos/task_abc/content")

	doubleVersionVideo := httptest.NewRecorder()
	engine.ServeHTTP(doubleVersionVideo, httptest.NewRequest(http.MethodPost, "/creative/relay/v1/v1/videos", strings.NewReader(`{"model":"sora-2"}`)))
	require.Equal(t, http.StatusNotFound, doubleVersionVideo.Code)
	requireNoCreativeFixtureMarkersInText(t, doubleVersionVideo.Body.String(), "/creative/relay/v1/v1/videos")

	wrongMethodRelay := httptest.NewRecorder()
	engine.ServeHTTP(wrongMethodRelay, httptest.NewRequest(http.MethodGet, "/creative/relay/v1/chat/completions", nil))
	require.Equal(t, http.StatusNotFound, wrongMethodRelay.Code)
	requireNoCreativeFixtureMarkersInText(t, wrongMethodRelay.Body.String(), "GET /creative/relay/v1/chat/completions")

	wrongMethodImageRelay := httptest.NewRecorder()
	engine.ServeHTTP(wrongMethodImageRelay, httptest.NewRequest(http.MethodGet, "/creative/relay/v1/images/generations", nil))
	require.Equal(t, http.StatusNotFound, wrongMethodImageRelay.Code)
	requireNoCreativeFixtureMarkersInText(t, wrongMethodImageRelay.Body.String(), "GET /creative/relay/v1/images/generations")
}

func TestSetRouterKeepsCreativeRoutesWhenFrontendBaseURLIsConfigured(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalMaster := common.IsMasterNode
	originalRedisEnabled := common.RedisEnabled
	originalWebRateLimit := common.GlobalWebRateLimitEnable
	common.IsMasterNode = false
	common.RedisEnabled = false
	common.GlobalWebRateLimitEnable = false
	t.Setenv("FRONTEND_BASE_URL", "https://frontend.example")
	t.Cleanup(func() {
		common.IsMasterNode = originalMaster
		common.RedisEnabled = originalRedisEnabled
		common.GlobalWebRateLimitEnable = originalWebRateLimit
	})

	engine := gin.New()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("creative-router-frontend-base-url-secret"))))
	SetRouter(engine, ThemeAssets{
		DefaultBuildFS:    testDefaultBuildFS,
		DefaultIndexPage:  testDefaultIndexPage,
		ClassicBuildFS:    testClassicBuildFS,
		ClassicIndexPage:  testClassicIndexPage,
		CreativeBuildFS:   testCreativeBuildFS,
		CreativeIndexPage: testCreativeIndexPage,
	})

	creativeAPI := httptest.NewRecorder()
	engine.ServeHTTP(creativeAPI, httptest.NewRequest(http.MethodGet, "/creative/api/bootstrap", nil))
	require.Equal(t, http.StatusUnauthorized, creativeAPI.Code)
	require.Empty(t, creativeAPI.Header().Get("Location"))
	require.NotContains(t, creativeAPI.Header().Get("Location"), "frontend.example")
	require.NotEqual(t, "max-age=604800", creativeAPI.Header().Get("Cache-Control"))
	require.Contains(t, creativeAPI.Header().Get("Content-Type"), "application/json")

	creativeRelay := httptest.NewRecorder()
	relayRequest := httptest.NewRequest(http.MethodPost, "/creative/relay/v1/suno/submit/music", strings.NewReader(`{"prompt":"safe"}`))
	relayRequest.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(creativeRelay, relayRequest)
	require.Equal(t, http.StatusUnauthorized, creativeRelay.Code)
	require.Empty(t, creativeRelay.Header().Get("Location"))
	require.NotContains(t, creativeRelay.Header().Get("Location"), "frontend.example")
	require.NotEqual(t, "max-age=604800", creativeRelay.Header().Get("Cache-Control"))

	missingCreativeAPI := httptest.NewRecorder()
	engine.ServeHTTP(missingCreativeAPI, httptest.NewRequest(http.MethodGet, "/creative/api/missing", nil))
	require.Equal(t, http.StatusNotFound, missingCreativeAPI.Code)
	require.Empty(t, missingCreativeAPI.Header().Get("Location"))
	require.NotContains(t, missingCreativeAPI.Header().Get("Location"), "frontend.example")
	require.Contains(t, missingCreativeAPI.Header().Get("Cache-Control"), "no-store")

	missingCreativeRelay := httptest.NewRecorder()
	missingRelayRequest := httptest.NewRequest(http.MethodPost, "/creative/relay/v1/v1/videos", strings.NewReader(`{"model":"sora-2"}`))
	missingRelayRequest.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(missingCreativeRelay, missingRelayRequest)
	require.Equal(t, http.StatusNotFound, missingCreativeRelay.Code)
	require.Empty(t, missingCreativeRelay.Header().Get("Location"))
	require.NotContains(t, missingCreativeRelay.Header().Get("Location"), "frontend.example")
	require.Contains(t, missingCreativeRelay.Header().Get("Cache-Control"), "no-store")

	wrongMethodRelay := httptest.NewRecorder()
	engine.ServeHTTP(wrongMethodRelay, httptest.NewRequest(http.MethodGet, "/creative/relay/v1/images/generations", nil))
	require.Equal(t, http.StatusNotFound, wrongMethodRelay.Code)
	require.Empty(t, wrongMethodRelay.Header().Get("Location"))
	require.NotContains(t, wrongMethodRelay.Header().Get("Location"), "frontend.example")
	require.Contains(t, wrongMethodRelay.Header().Get("Cache-Control"), "no-store")

	trailingSlashAPI := httptest.NewRecorder()
	engine.ServeHTTP(trailingSlashAPI, httptest.NewRequest(http.MethodGet, "/creative/api/bootstrap/", nil))
	require.Contains(t, []int{http.StatusUnauthorized, http.StatusNotFound}, trailingSlashAPI.Code)
	require.Empty(t, trailingSlashAPI.Header().Get("Location"))
	require.NotContains(t, trailingSlashAPI.Header().Get("Location"), "frontend.example")
	require.Contains(t, trailingSlashAPI.Header().Get("Cache-Control"), "no-store")

	trailingSlashRelay := httptest.NewRecorder()
	trailingSlashRelayRequest := httptest.NewRequest(http.MethodPost, "/creative/relay/v1/images/generations/", strings.NewReader(`{"model":"gpt-image-1","prompt":"safe"}`))
	trailingSlashRelayRequest.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(trailingSlashRelay, trailingSlashRelayRequest)
	require.Contains(t, []int{http.StatusUnauthorized, http.StatusNotFound}, trailingSlashRelay.Code)
	require.Empty(t, trailingSlashRelay.Header().Get("Location"))
	require.NotContains(t, trailingSlashRelay.Header().Get("Location"), "frontend.example")
	require.Contains(t, trailingSlashRelay.Header().Get("Cache-Control"), "no-store")

	spaFallback := httptest.NewRecorder()
	engine.ServeHTTP(spaFallback, httptest.NewRequest(http.MethodGet, "/not-creative", nil))
	require.Equal(t, http.StatusMovedPermanently, spaFallback.Code)
	require.Equal(t, "https://frontend.example/not-creative", spaFallback.Header().Get("Location"))
}

func TestCreativeAPIRelayDoNotInheritLongLivedWebCache(t *testing.T) {
	engine := newCreativeWebTestEngine(t)

	for _, tt := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "api", method: http.MethodGet, path: "/creative/api/bootstrap"},
		{name: "relay", method: http.MethodPost, path: "/creative/relay/v1/images/generations", body: `{"model":"gpt-image-1","prompt":"safe"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			request.Header.Set("Content-Type", "application/json")
			engine.ServeHTTP(recorder, request)

			require.NotEqual(t, "max-age=604800", recorder.Header().Get("Cache-Control"))
			require.Contains(t, recorder.Header().Get("Cache-Control"), "no-store")
		})
	}
}

func TestCreativeEmbeddedNoCacheHeaders(t *testing.T) {
	engine := newCreativeWebTestEngine(t)

	tests := []struct {
		path              string
		wantMimePart      string
		wantProductMarkup bool
	}{
		{path: "/creative/", wantMimePart: "text/html", wantProductMarkup: true},
		{path: "/creative/deep-link", wantMimePart: "text/html", wantProductMarkup: true},
		{path: "/creative/index.html", wantMimePart: "text/html", wantProductMarkup: true},
		{path: "/creative/sw.js", wantMimePart: "javascript"},
		{path: "/creative/version.json", wantMimePart: "json"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tt.path, nil))

			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, "no-cache", recorder.Header().Get("Cache-Control"))
			require.Contains(t, recorder.Header().Get("Content-Type"), tt.wantMimePart)
			requireNoCreativeFixtureMarkersInText(t, recorder.Body.String(), tt.path)
			if tt.wantProductMarkup {
				requireCreativeIndexProductMarkup(t, recorder.Body.String())
			}
			requireCreativeEmbeddedSecurityHeaders(t, recorder)
		})
	}
}

func TestCreativeEmbeddedAssetCacheAndCSPHeaders(t *testing.T) {
	engine := newCreativeWebTestEngine(t)
	assetPaths := creativeAssetPathsReferencedByIndex(t)

	for _, path := range []string{assetPaths.js, assetPaths.css} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, "public, max-age=31536000, immutable", recorder.Header().Get("Cache-Control"))
			require.NotEmpty(t, recorder.Body.String())
			requireNoCreativeFixtureMarkersInText(t, recorder.Body.String(), path)
			requireCreativeEmbeddedSecurityHeaders(t, recorder)
			requireCreativeEmbeddedProvenanceHeaders(t, recorder)
		})
	}

	missingAsset := httptest.NewRecorder()
	engine.ServeHTTP(missingAsset, httptest.NewRequest(http.MethodGet, "/creative/assets/not-a-real-asset/deep-link", nil))
	require.Equal(t, http.StatusNotFound, missingAsset.Code)
	require.Equal(t, "no-cache", missingAsset.Header().Get("Cache-Control"))
	require.NotContains(t, missingAsset.Header().Get("Content-Type"), "text/html")
	requireNoCreativeFixtureMarkersInText(t, missingAsset.Body.String(), "/creative/assets/not-a-real-asset/deep-link")
	requireCreativeEmbeddedSecurityHeaders(t, missingAsset)
}

func TestCreativeEmbeddedProvenanceHeaders(t *testing.T) {
	engine := newCreativeWebTestEngine(t)
	assetPaths := creativeAssetPathsReferencedByIndex(t)
	versionBytes, err := testCreativeBuildFS.ReadFile("web/creative/dist/version.json")
	require.NoError(t, err)
	expectedVersionHash := fmt.Sprintf("sha256:%x", sha256.Sum256(versionBytes))
	var metadata creativeVersionMetadata
	require.NoError(t, common.Unmarshal(versionBytes, &metadata))

	for _, path := range []string{
		"/creative/version.json",
		"/creative/",
		assetPaths.js,
	} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, expectedVersionHash, recorder.Header().Get("X-Creative-Version-Hash"))
			require.Equal(t, safeCreativeHeaderValue(metadata.Version), recorder.Header().Get("X-Creative-Build-Version"))
			require.Equal(t, safeCreativeHeaderValue(metadata.BuildTime), recorder.Header().Get("X-Creative-Build-Time"))
			require.Equal(t, safeCreativeHeaderValue(metadata.GitCommit), recorder.Header().Get("X-Creative-Git-Commit"))
		})
	}
}

func TestCreativeEmbeddedProductionBuildContract(t *testing.T) {
	indexBytes, err := testCreativeBuildFS.ReadFile("web/creative/dist/index.html")
	require.NoError(t, err)
	indexHTML := string(indexBytes)
	requireCreativeIndexProductMarkup(t, indexHTML)
	creativeAssetPathsReferencedByIndex(t)

	_, err = testCreativeBuildFS.ReadFile("web/creative/dist/sw.js")
	require.NoError(t, err)
	_, err = testCreativeBuildFS.ReadFile("web/creative/dist/version.json")
	require.NoError(t, err)

	require.NoError(t, fs.WalkDir(testCreativeBuildFS, "web/creative/dist", func(path string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		if d.IsDir() {
			return nil
		}
		content, readErr := testCreativeBuildFS.ReadFile(path)
		require.NoError(t, readErr)
		requireNoCreativeFixtureMarkersInText(t, string(content), path)
		return nil
	}))
}

func newCreativeWebTestEngine(t *testing.T) *gin.Engine {
	t.Helper()

	gin.SetMode(gin.TestMode)
	originalRedisEnabled := common.RedisEnabled
	originalWebRateLimit := common.GlobalWebRateLimitEnable
	common.RedisEnabled = false
	common.GlobalWebRateLimitEnable = false
	t.Cleanup(func() {
		common.RedisEnabled = originalRedisEnabled
		common.GlobalWebRateLimitEnable = originalWebRateLimit
	})

	engine := gin.New()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("creative-router-test-secret"))))
	require.NotPanics(t, func() {
		SetWebRouter(engine, ThemeAssets{
			DefaultBuildFS:    testDefaultBuildFS,
			DefaultIndexPage:  testDefaultIndexPage,
			ClassicBuildFS:    testClassicBuildFS,
			ClassicIndexPage:  testClassicIndexPage,
			CreativeBuildFS:   testCreativeBuildFS,
			CreativeIndexPage: testCreativeIndexPage,
		})
	})
	return engine
}

func requireCreativeEmbeddedSecurityHeaders(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()

	csp := recorder.Header().Get("Content-Security-Policy")
	require.Contains(t, csp, "frame-ancestors 'self'")
	require.Contains(t, csp, "base-uri 'self'")
	require.Contains(t, csp, "object-src 'none'")
	require.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))
	require.Equal(t, "origin-when-cross-origin", recorder.Header().Get("Referrer-Policy"))
	permissionsPolicy := recorder.Header().Get("Permissions-Policy")
	require.Contains(t, permissionsPolicy, "camera=()")
	require.Contains(t, permissionsPolicy, "microphone=()")
	require.Contains(t, permissionsPolicy, "geolocation=()")
	require.Contains(t, permissionsPolicy, "payment=()")
}

func requireCreativeEmbeddedProvenanceHeaders(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()

	require.NotEmpty(t, recorder.Header().Get("X-Creative-Version-Hash"))
	require.NotEmpty(t, recorder.Header().Get("X-Creative-Build-Version"))
	require.NotEmpty(t, recorder.Header().Get("X-Creative-Build-Time"))
	require.NotEmpty(t, recorder.Header().Get("X-Creative-Git-Commit"))
}

func requireCreativeIndexProductMarkup(t *testing.T, body string) {
	t.Helper()

	require.Contains(t, body, "New API Creative", "creative index should expose embedded New API Creative product markup")
	require.Contains(t, body, `id="app-boot-loading"`, "creative index should keep the boot loading shell")
	require.Contains(t, body, `data-app-boot-title`, "creative index boot shell should keep title node")
	require.Contains(t, body, `data-app-boot-progress`, "creative index boot shell should keep progress node")
	require.Contains(t, body, `id="root"`, "creative index should keep the React mount root")
	require.NotContains(t, body, "OpenTu", "embedded creative index must not expose standalone OpenTU branding")
	require.NotContains(t, body, "Opentu", "embedded creative index must not expose standalone Opentu branding")
	require.NotContains(t, strings.ToLower(body), "opentu.ai", "embedded creative index must not expose standalone Opentu host")
}

func requireNoCreativeFixtureMarkersInText(t *testing.T, body string, context string) {
	t.Helper()

	for _, marker := range creativeFixtureMarkers {
		require.NotContains(t, body, marker, "%s must not contain stale creative fixture marker %q", context, marker)
	}
}

type creativeReferencedAssets struct {
	js  string
	css string
}

var (
	creativeAssetReferencePattern = regexp.MustCompile(`/creative/assets/[^"'\s>)]+`)
	creativeFixtureMarkers        = []string{
		"creative fixture",
		"creativeServiceWorkerFixture",
		"9.9.9-test",
		"creative-test-commit",
	}
)

func creativeAssetPathsReferencedByIndex(t *testing.T) creativeReferencedAssets {
	t.Helper()

	indexBytes, err := testCreativeBuildFS.ReadFile("web/creative/dist/index.html")
	require.NoError(t, err)
	matches := creativeAssetReferencePattern.FindAllString(string(indexBytes), -1)
	require.NotEmpty(t, matches, "creative index.html must reference assets under /creative/assets/")
	sort.Strings(matches)

	var assets creativeReferencedAssets
	for _, match := range matches {
		withoutQuery := strings.SplitN(match, "?", 2)[0]
		switch {
		case assets.js == "" && strings.HasSuffix(withoutQuery, ".js"):
			assets.js = match
		case assets.css == "" && strings.HasSuffix(withoutQuery, ".css"):
			assets.css = match
		}
	}
	require.NotEmpty(t, assets.js, "creative index.html must reference at least one JS asset under /creative/assets/")
	require.NotEmpty(t, assets.css, "creative index.html must reference at least one CSS asset under /creative/assets/")
	return assets
}
