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
