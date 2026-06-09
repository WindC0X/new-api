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

//go:embed web/creative/dist
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

	wrongMethodRelay := httptest.NewRecorder()
	engine.ServeHTTP(wrongMethodRelay, httptest.NewRequest(http.MethodGet, "/creative/relay/v1/chat/completions", nil))
	require.Equal(t, http.StatusNotFound, wrongMethodRelay.Code)
	requireNoCreativeFixtureMarkersInText(t, wrongMethodRelay.Body.String(), "GET /creative/relay/v1/chat/completions")

	wrongMethodImageRelay := httptest.NewRecorder()
	engine.ServeHTTP(wrongMethodImageRelay, httptest.NewRequest(http.MethodGet, "/creative/relay/v1/images/generations", nil))
	require.Equal(t, http.StatusNotFound, wrongMethodImageRelay.Code)
	requireNoCreativeFixtureMarkersInText(t, wrongMethodImageRelay.Body.String(), "GET /creative/relay/v1/images/generations")
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

	deepLink := httptest.NewRecorder()
	engine.ServeHTTP(deepLink, httptest.NewRequest(http.MethodGet, "/creative/assets/not-a-real-asset/deep-link", nil))
	require.Equal(t, http.StatusOK, deepLink.Code)
	require.Equal(t, "no-cache", deepLink.Header().Get("Cache-Control"))
	require.Contains(t, deepLink.Header().Get("Content-Type"), "text/html")
	requireCreativeIndexProductMarkup(t, deepLink.Body.String())
	requireNoCreativeFixtureMarkersInText(t, deepLink.Body.String(), "/creative/assets/not-a-real-asset/deep-link")
	requireCreativeEmbeddedSecurityHeaders(t, deepLink)
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

	require.True(t, strings.Contains(body, "Opentu") || strings.Contains(body, "OpenTu"), "creative index should contain Opentu/OpenTu product markup")
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
