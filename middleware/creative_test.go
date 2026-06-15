package middleware

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCreativeRelayModelReaderSupportsMultipartAndReplaysBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "creative-model-05"))
	require.NoError(t, writer.WriteField("prompt", "a cat playing piano"))
	require.NoError(t, writer.Close())

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/creative/relay/v1/videos", bytes.NewReader(body.Bytes()))
	ctx.Request.Header.Set("Content-Type", writer.FormDataContentType())

	modelName, err := readCreativeRelayModel(ctx)

	require.NoError(t, err)
	require.Equal(t, "creative-model-05", modelName)
	replayed, err := io.ReadAll(ctx.Request.Body)
	require.NoError(t, err)
	require.Equal(t, body.Bytes(), replayed)
	form, err := common.ParseMultipartFormReusable(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"creative-model-05"}, form.Value["model"])
	require.Equal(t, []string{"a cat playing piano"}, form.Value["prompt"])
}

func TestCreativeOriginUsesForwardedProtoButKeepsRequestHost(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "http://internal.example/creative/relay/v1/suno/fetch/task_1", nil)
	ctx.Request.Host = "internal.example"
	ctx.Request.Header.Set("Origin", "https://internal.example")
	ctx.Request.Header.Set("X-Forwarded-Proto", "https")
	ctx.Request.Header.Set("X-Forwarded-Host", "evil.example")

	require.True(t, creativeUnsafeRequestOriginIsValid(ctx))
	require.Equal(t, "https://internal.example", creativeRequestOrigin(ctx))

	ctx.Request.Header.Set("Origin", "https://evil.example")
	require.False(t, creativeUnsafeRequestOriginIsValid(ctx))
}

func TestCreativeOriginUsesStandardForwardedProto(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "http://console.example/creative/api/bootstrap", nil)
	ctx.Request.Host = "console.example"
	ctx.Request.Header.Set("Forwarded", `for=127.0.0.1;proto=https;host=evil.example`)
	ctx.Request.Header.Set("Referer", "https://console.example/creative/")

	require.Equal(t, "https://console.example", creativeRequestOrigin(ctx))
	require.True(t, creativeUnsafeRequestOriginIsValid(ctx))

	ctx.Request.Header.Set("Referer", "https://evil.example/creative/")
	require.False(t, creativeUnsafeRequestOriginIsValid(ctx))
}

func TestCreativeOriginUsesFirstForwardedProtoValue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "http://console.example/creative/api/bootstrap", nil)
	ctx.Request.Host = "console.example"
	ctx.Request.Header.Set("X-Forwarded-Proto", "https,http")
	ctx.Request.Header.Set("Origin", "https://console.example")

	require.Equal(t, "https://console.example", creativeRequestOrigin(ctx))
	require.True(t, creativeUnsafeRequestOriginIsValid(ctx))
}

func TestCreativeOriginWithoutForwardedHeadersKeepsHTTPFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "http://console.example/creative/api/bootstrap", nil)
	ctx.Request.Host = "console.example"
	ctx.Request.Header.Set("Origin", "http://console.example")

	require.Equal(t, "http://console.example", creativeRequestOrigin(ctx))
	require.True(t, creativeUnsafeRequestOriginIsValid(ctx))

	ctx.Request.Header.Set("Origin", "https://console.example")
	require.False(t, creativeUnsafeRequestOriginIsValid(ctx))
}

func TestCreativeOriginIgnoresInvalidForwardedProto(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "http://console.example/creative/api/bootstrap", nil)
	ctx.Request.Host = "console.example"
	ctx.Request.Header.Set("X-Forwarded-Proto", "javascript")
	ctx.Request.Header.Set("Origin", "http://console.example")

	require.Equal(t, "http://console.example", creativeRequestOrigin(ctx))
	require.True(t, creativeUnsafeRequestOriginIsValid(ctx))
}

func TestCreativeRejectCrossOriginWhenPresentAllowsOriginlessSafeGet(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CreativeRejectCrossOriginWhenPresent())
	router.GET("/creative/api/bootstrap", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	originless := httptest.NewRecorder()
	router.ServeHTTP(originless, httptest.NewRequest(http.MethodGet, "http://example.com/creative/api/bootstrap", nil))
	require.Equal(t, http.StatusOK, originless.Code)

	sameOrigin := httptest.NewRecorder()
	sameOriginRequest := httptest.NewRequest(http.MethodGet, "http://example.com/creative/api/bootstrap", nil)
	sameOriginRequest.Header.Set("Origin", "http://example.com")
	router.ServeHTTP(sameOrigin, sameOriginRequest)
	require.Equal(t, http.StatusOK, sameOrigin.Code)

	crossOrigin := httptest.NewRecorder()
	crossOriginRequest := httptest.NewRequest(http.MethodGet, "http://example.com/creative/api/bootstrap", nil)
	crossOriginRequest.Header.Set("Origin", "https://evil.example")
	router.ServeHTTP(crossOrigin, crossOriginRequest)
	require.Equal(t, http.StatusForbidden, crossOrigin.Code)
}

func TestCreativeRelayModelReaderAllowsGetWithoutBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/creative/relay/v1/videos/task_abc", nil)

	modelName, err := readCreativeRelayModel(ctx)

	require.NoError(t, err)
	require.Empty(t, modelName)
}

func TestCreativeRelayModelReaderUsesServerSideOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/creative/relay/v1/suno/submit/music", strings.NewReader(`{"prompt":"safe song prompt"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set(ContextKeyCreativeRelayModelOverride, "suno_music")

	modelName, err := readCreativeRelayModel(ctx)

	require.NoError(t, err)
	require.Equal(t, "suno_music", modelName)
	replayed, err := io.ReadAll(ctx.Request.Body)
	require.NoError(t, err)
	require.JSONEq(t, `{"prompt":"safe song prompt"}`, string(replayed))
}

func TestCreativeSunoDistributorReadsActionModelAndRelayMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/creative/relay/v1/suno/submit/lyrics", strings.NewReader(`{"prompt":"safe lyrics prompt"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Params = gin.Params{{Key: "action", Value: "lyrics"}}

	req, shouldSelectChannel, err := getModelRequest(ctx)

	require.NoError(t, err)
	require.True(t, shouldSelectChannel)
	require.Equal(t, "suno_lyrics", req.Model)
	require.Equal(t, relayconstant.RelayModeSunoSubmit, ctx.GetInt("relay_mode"))
}

func TestCreativeMJDistributorReadsImagineModelAndRelayMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/creative/relay/v1/mj/submit/imagine", strings.NewReader(`{"prompt":"safe image prompt"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	req, shouldSelectChannel, err := getModelRequest(ctx)

	require.NoError(t, err)
	require.True(t, shouldSelectChannel)
	require.Equal(t, "mj_imagine", req.Model)
	require.Equal(t, relayconstant.RelayModeMidjourneyImagine, ctx.GetInt("relay_mode"))
}

func TestCreativeRelayModelReaderIgnoresUnsupportedMJActionModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/creative/relay/v1/mj/submit/action", strings.NewReader(`{"model":"mj-private-model","prompt":"safe"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	modelName, err := readCreativeRelayModel(ctx)

	require.NoError(t, err)
	require.Empty(t, modelName)
	replayed, err := io.ReadAll(ctx.Request.Body)
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"mj-private-model","prompt":"safe"}`, string(replayed))
}

func TestCreativeMJDistributorDoesNotSelectChannelForFetchOrImage(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		path     string
		wantMode int
		paramKey string
		paramVal string
		body     string
	}{
		{
			name:     "fetch",
			method:   http.MethodGet,
			path:     "/creative/relay/v1/mj/task/task_abc/fetch",
			wantMode: relayconstant.RelayModeMidjourneyTaskFetch,
			paramKey: "task_id",
			paramVal: "task_abc",
		},
		{
			name:     "image",
			method:   http.MethodGet,
			path:     "/creative/relay/v1/mj/image/task_abc",
			wantMode: relayconstant.RelayModeMidjourneyImage,
			paramKey: "task_id",
			paramVal: "task_abc",
		},
		{
			name:     "list",
			method:   http.MethodPost,
			path:     "/creative/relay/v1/mj/task/list-by-condition",
			wantMode: relayconstant.RelayModeMidjourneyTaskFetchByCondition,
			body:     `{"ids":["task_abc"]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			if tt.paramKey != "" {
				ctx.Params = gin.Params{{Key: tt.paramKey, Value: tt.paramVal}}
			}

			req, shouldSelectChannel, err := getModelRequest(ctx)

			require.NoError(t, err)
			require.False(t, shouldSelectChannel)
			require.Empty(t, req.Model)
			require.Equal(t, tt.wantMode, ctx.GetInt("relay_mode"))
		})
	}
}

func TestCreativeVideoDistributorReadsSubmitModelAndRelayMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/creative/relay/v1/videos", strings.NewReader(`{"model":"creative-model-05","prompt":"safe"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	req, shouldSelectChannel, err := getModelRequest(ctx)

	require.NoError(t, err)
	require.True(t, shouldSelectChannel)
	require.Equal(t, "creative-model-05", req.Model)
	require.Equal(t, relayconstant.RelayModeVideoSubmit, ctx.GetInt("relay_mode"))
}

func TestCreativeVideoDistributorDoesNotSelectChannelForFetch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/creative/relay/v1/videos/task_abc", nil)
	ctx.Params = gin.Params{{Key: "task_id", Value: "task_abc"}}

	req, shouldSelectChannel, err := getModelRequest(ctx)

	require.NoError(t, err)
	require.False(t, shouldSelectChannel)
	require.Empty(t, req.Model)
	require.Equal(t, relayconstant.RelayModeVideoFetchByID, ctx.GetInt("relay_mode"))
}
