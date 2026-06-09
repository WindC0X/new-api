package controller

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCreativeAssetUploadContentRangeAndDeleteContract(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 701)
	installCreativeAssetRuntimeForControllerTest(t)

	router := newCreativeAssetSessionRouter(t, 701)
	auth := bootstrapCreativeSessionAuth(t, router)

	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1, 2, 3, 4}
	upload := performCreativeAssetMultipart(t, router, "/creative/api/assets", png, "image/png", "image", auth.cookies, creativeSameOriginNonceHeaders(auth))
	require.Equal(t, http.StatusCreated, upload.Code)
	require.Equal(t, "private, no-store", upload.Header().Get("Cache-Control"))

	uploadPayload := decodeCreativeResponse(t, upload)
	asset := creativeResponseObject(t, creativeResponseData(t, uploadPayload), "asset")
	assetID, ok := asset["id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, assetID)
	require.Equal(t, "/creative/api/assets/"+assetID+"/content", asset["url"])
	uploadJSON := upload.Body.String()
	require.NotContains(t, uploadJSON, "ObjectKey")
	require.NotContains(t, uploadJSON, "StorageBackend")
	require.NotContains(t, uploadJSON, "bucket")
	require.NotContains(t, uploadJSON, "sourceUrl")

	get := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/creative/api/assets/"+assetID+"/content", nil)
	req.Header.Set("Range", "bytes=0-7")
	for _, cookie := range auth.cookies {
		req.AddCookie(cookie)
	}
	router.ServeHTTP(get, req)
	require.Equal(t, http.StatusPartialContent, get.Code)
	require.Equal(t, "image/png", get.Header().Get("Content-Type"))
	require.Equal(t, "nosniff", get.Header().Get("X-Content-Type-Options"))
	require.Equal(t, "Cookie", get.Header().Get("Vary"))
	require.Equal(t, "bytes 0-7/12", get.Header().Get("Content-Range"))
	require.Equal(t, png[:8], get.Body.Bytes())

	require.NoError(t, model.RefreshCreativeDocumentAssetRefs(701, "doc-with-ref", []string{assetID}))
	deleteReferenced := performCreativeSessionJSON(t, router, http.MethodDelete, "/creative/api/assets/"+assetID, nil, auth.cookies, creativeSameOriginNonceHeaders(auth))
	require.Equal(t, http.StatusConflict, deleteReferenced.Code)

	require.NoError(t, model.DeleteCreativeDocumentAssetRefs(701, "doc-with-ref"))
	deleteUnreferenced := performCreativeSessionJSON(t, router, http.MethodDelete, "/creative/api/assets/"+assetID, nil, auth.cookies, creativeSameOriginNonceHeaders(auth))
	require.Equal(t, http.StatusOK, deleteUnreferenced.Code)
}

func TestCreativeAssetAPIsRejectTokenAuthMissingNonceSourceURLAndSVG(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 702)
	installCreativeAssetRuntimeForControllerTest(t)

	// Direct handler has no multipart body; the important API-token gate is the context flag.
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/creative/api/assets", nil)
	ctx.Set("id", 702)
	ctx.Set("use_access_token", true)
	CreativeUploadAsset(ctx)
	require.Equal(t, http.StatusForbidden, recorder.Code)

	router := newCreativeAssetSessionRouter(t, 702)
	auth := bootstrapCreativeSessionAuth(t, router)
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1}

	missingNonce := performCreativeAssetMultipart(t, router, "/creative/api/assets", png, "image/png", "image", auth.cookies, nil)
	require.Equal(t, http.StatusForbidden, missingNonce.Code)

	sourceURL := performCreativeAssetMultipartWithFields(t, router, "/creative/api/assets", png, "image/png", auth.cookies, creativeSameOriginNonceHeaders(auth), map[string]string{
		"mediaType": "image",
		"sourceUrl": "https://bucket.example/object?X-Amz-Signature=secret",
	})
	require.Equal(t, http.StatusBadRequest, sourceURL.Code)
	require.NotContains(t, sourceURL.Body.String(), "X-Amz-Signature")

	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)
	svgUpload := performCreativeAssetMultipart(t, router, "/creative/api/assets", svg, "image/svg+xml", "image", auth.cookies, creativeSameOriginNonceHeaders(auth))
	require.Equal(t, http.StatusBadRequest, svgUpload.Code)
}

func TestCreativeDocumentAssetRefsRefreshAndRejectCredentialedURL(t *testing.T) {
	setupCreativeControllerTestDB(t)
	assetID := "asset_doc_ref_123456"
	require.NoError(t, model.DB.Create(&model.CreativeAsset{
		UserId:         703,
		AssetId:        assetID,
		ContentHash:    "hash-doc-ref",
		MediaType:      "image",
		MimeType:       "image/png",
		SizeBytes:      3,
		StorageBackend: model.CreativeAssetStorageDatabase,
		Data:           []byte("png"),
	}).Error)

	create := runCreativeHandler(t, CreativeCreateDocument, http.MethodPost, "/creative/api/documents", map[string]any{
		"id":       "doc-assets",
		"snapshot": map[string]any{"imageUrl": "/creative/api/assets/" + assetID + "/content"},
		"metadata": map[string]any{"thumbnailUrl": "http://example.com/creative/api/assets/" + assetID + "/content"},
	}, 703, nil)
	require.Equal(t, http.StatusCreated, create.Code)
	refs, err := model.ListCreativeDocumentAssetRefs(703, "doc-assets")
	require.NoError(t, err)
	require.Len(t, refs, 1)
	require.Equal(t, assetID, refs[0].AssetId)

	rejectSignedURL := runCreativeHandler(t, CreativeCreateDocument, http.MethodPost, "/creative/api/documents", map[string]any{
		"id":       "doc-signed-url",
		"snapshot": map[string]any{"imageUrl": "https://bucket.example/private.png?X-Amz-Signature=secret"},
	}, 703, nil)
	require.Equal(t, http.StatusBadRequest, rejectSignedURL.Code)
	require.NotContains(t, rejectSignedURL.Body.String(), "X-Amz-Signature")

	deleted := runCreativeHandler(t, CreativeDeleteDocument, http.MethodDelete, "/creative/api/documents/doc-assets", map[string]any{
		"baseRevision": 1,
	}, 703, gin.Params{{Key: "id", Value: "doc-assets"}})
	require.Equal(t, http.StatusOK, deleted.Code)
	refs, err = model.ListCreativeDocumentAssetRefs(703, "doc-assets")
	require.NoError(t, err)
	require.Empty(t, refs)
}

func newCreativeAssetSessionRouter(t *testing.T, userId int) *gin.Engine {
	t.Helper()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("creative-asset-test-secret"))))
	router.GET("/creative/api/bootstrap", func(c *gin.Context) {
		session := sessions.Default(c)
		session.Set("username", "creative-asset-user")
		session.Set("role", common.RoleCommonUser)
		session.Set("id", userId)
		session.Set("status", common.UserStatusEnabled)
		session.Set("group", "default")
		c.Set("id", userId)
		CreativeBootstrap(c)
	})
	api := router.Group("/creative/api")
	api.Use(middleware.BodyStorageCleanup())
	api.Use(middleware.CreativeSessionHeaderBridge(), middleware.UserAuth())
	api.POST("/assets", middleware.CreativeRequireNonce(), CreativeUploadAsset)
	api.GET("/assets/:id", CreativeGetAsset)
	api.GET("/assets/:id/content", CreativeGetAssetContent)
	api.DELETE("/assets/:id", middleware.CreativeRequireNonce(), CreativeDeleteAsset)
	return router
}

func installCreativeAssetRuntimeForControllerTest(t *testing.T) {
	t.Helper()

	require.NoError(t, model.DB.AutoMigrate(&model.CreativeAsset{}, &model.CreativeDocumentAssetRef{}))
	runtime, err := service.NewCreativeAssetRuntime(service.CreativeAssetConfig{
		Enabled:                   true,
		RolloutMode:               service.CreativeAssetRolloutLocal,
		StorageBackend:            model.CreativeAssetStorageDatabase,
		DatabaseGlobalMaxBytes:    1024 * 1024,
		DatabaseUserMaxBytes:      1024 * 1024,
		DatabaseReservedFreeBytes: 0,
		UserMaxBytes:              1024 * 1024,
		UserMaxAssets:             100,
		DiskSpaceProviderForWrites: func() common.DiskSpaceInfo {
			return common.DiskSpaceInfo{Total: 1024 * 1024, Free: 1024 * 1024, UsedPercent: 1}
		},
	}, nil)
	require.NoError(t, err)
	service.SetCreativeAssetRuntimeForTest(t, runtime)
}

func performCreativeAssetMultipart(t *testing.T, router *gin.Engine, target string, file []byte, mimeType string, mediaType string, cookies []*http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return performCreativeAssetMultipartWithFields(t, router, target, file, mimeType, cookies, headers, map[string]string{"mediaType": mediaType})
}

func performCreativeAssetMultipartWithFields(t *testing.T, router *gin.Engine, target string, file []byte, mimeType string, cookies []*http.Cookie, headers map[string]string, fields map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		require.NoError(t, writer.WriteField(key, value))
	}
	part, err := writer.CreatePart(textprotoMIMEHeader(map[string]string{
		"Content-Disposition": `form-data; name="file"; filename="asset.bin"`,
		"Content-Type":        mimeType,
	}))
	require.NoError(t, err)
	_, err = part.Write(file)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	request := httptest.NewRequest(http.MethodPost, target, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Content-Length", strconv.Itoa(body.Len()))
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func textprotoMIMEHeader(values map[string]string) textproto.MIMEHeader {
	header := textproto.MIMEHeader{}
	for key, value := range values {
		header.Set(key, value)
	}
	return header
}
