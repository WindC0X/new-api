package main

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

//go:embed router/web/creative/dist
var routerCreativeBuildFS embed.FS

type mainCreativeReferencedAssets struct {
	js  string
	css string
}

var (
	mainCreativeAssetReferencePattern = regexp.MustCompile(`/creative/assets/[^"'\s>)]+`)
	mainCreativeFixtureMarkers        = []string{
		"creative fixture",
		"creativeServiceWorkerFixture",
		"9.9.9-test",
		"creative-test-commit",
	}
)

func TestCreativeProductionRootDistMatchesRouterDistAndContract(t *testing.T) {
	rootIndex := mainReadCreativeRootFile(t, "index.html")
	require.True(t, bytes.Equal(rootIndex, creativeIndexPage), "main.go creativeIndexPage must be the same bytes as web/creative/dist/index.html")
	mainRequireCreativeIndexEmbeddedMarkup(t, string(rootIndex))
	rootAssets := mainCreativeAssetPathsReferencedByIndex(t, string(rootIndex))

	mainRequireCreativeRootRouterDistTreesEqual(t)
	mainRequireCreativeRootRouterFileEqual(t, "index.html")
	mainRequireCreativeRootRouterFileEqual(t, "sw.js")
	mainRequireCreativeRootRouterFileEqual(t, "version.json")
	mainRequireCreativeRootRouterFileEqual(t, strings.TrimPrefix(rootAssets.js, "/creative/"))
	mainRequireCreativeRootRouterFileEqual(t, strings.TrimPrefix(rootAssets.css, "/creative/"))

	routerIndex := mainReadCreativeRouterFile(t, "index.html")
	routerAssets := mainCreativeAssetPathsReferencedByIndex(t, string(routerIndex))
	require.Equal(t, rootAssets, routerAssets, "production root and router creative dist must reference the same /creative entry assets")

	require.NoError(t, fs.WalkDir(creativeBuildFS, "web/creative/dist", func(path string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		if d.IsDir() {
			return nil
		}
		content, readErr := creativeBuildFS.ReadFile(path)
		require.NoError(t, readErr)
		mainRequireNoCreativeFixtureMarkersInText(t, string(content), path)
		return nil
	}))
}

func mainRequireCreativeRootRouterDistTreesEqual(t *testing.T) {
	t.Helper()

	root := mainCreativeDistTree(t, creativeBuildFS, "web/creative/dist")
	router := mainCreativeDistTree(t, routerCreativeBuildFS, "router/web/creative/dist")
	require.Equal(t, root, router, "creative dist file list and hashes must match between web/creative/dist and router/web/creative/dist")
}

func mainCreativeDistTree(t *testing.T, filesystem fs.FS, root string) map[string]string {
	t.Helper()

	files := map[string]string{}
	require.NoError(t, fs.WalkDir(filesystem, root, func(path string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		if d.IsDir() {
			return nil
		}
		content, readErr := fs.ReadFile(filesystem, path)
		require.NoError(t, readErr)
		relativePath := strings.TrimPrefix(path, strings.TrimSuffix(root, "/")+"/")
		files[relativePath] = fmt.Sprintf("%x", sha256.Sum256(content))
		return nil
	}))
	require.NotEmpty(t, files, "creative dist tree %s must not be empty", root)
	return files
}

func mainRequireCreativeRootRouterFileEqual(t *testing.T, relativePath string) {
	t.Helper()

	root := mainReadCreativeRootFile(t, relativePath)
	router := mainReadCreativeRouterFile(t, relativePath)
	rootHash := fmt.Sprintf("%x", sha256.Sum256(root))
	routerHash := fmt.Sprintf("%x", sha256.Sum256(router))
	require.Equal(t, rootHash, routerHash, "creative dist file %s must match between web/creative/dist and router/web/creative/dist", relativePath)
}

func mainReadCreativeRootFile(t *testing.T, relativePath string) []byte {
	t.Helper()

	content, err := creativeBuildFS.ReadFile("web/creative/dist/" + strings.TrimPrefix(relativePath, "/"))
	require.NoError(t, err)
	return content
}

func mainReadCreativeRouterFile(t *testing.T, relativePath string) []byte {
	t.Helper()

	content, err := routerCreativeBuildFS.ReadFile("router/web/creative/dist/" + strings.TrimPrefix(relativePath, "/"))
	require.NoError(t, err)
	return content
}

func mainCreativeAssetPathsReferencedByIndex(t *testing.T, indexHTML string) mainCreativeReferencedAssets {
	t.Helper()

	matches := mainCreativeAssetReferencePattern.FindAllString(indexHTML, -1)
	require.NotEmpty(t, matches, "creative index.html must reference assets under /creative/assets/")
	sort.Strings(matches)

	var assets mainCreativeReferencedAssets
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

func mainRequireCreativeIndexEmbeddedMarkup(t *testing.T, body string) {
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

func mainRequireNoCreativeFixtureMarkersInText(t *testing.T, body string, context string) {
	t.Helper()

	for _, marker := range mainCreativeFixtureMarkers {
		require.NotContains(t, body, marker, "%s must not contain stale creative fixture marker %q", context, marker)
	}
}
