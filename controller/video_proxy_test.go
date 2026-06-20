package controller

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRedactURLForLogStripsPrivateURLMaterial(t *testing.T) {
	raw := "https://storage.example.com/private/object.mp4?X-Amz-Signature=secret&token=leak#fragment"

	redacted := redactURLForLog(raw)

	require.Equal(t, "https://storage.example.com/[redacted]", redacted)
	require.NotContains(t, redacted, "X-Amz-Signature")
	require.NotContains(t, redacted, "secret")
	require.NotContains(t, redacted, "token")
	require.NotContains(t, redacted, "object.mp4")
}

func TestRedactURLForLogDoesNotEchoInvalidURL(t *testing.T) {
	raw := strings.Repeat("not-a-url-with-secret-token", 4)

	redacted := redactURLForLog(raw)

	require.Equal(t, "[redacted-url]", redacted)
	require.NotContains(t, redacted, "secret-token")
}

func TestApplyVideoProxyCacheHeadersArePrivateByDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	applyVideoProxyCacheHeaders(c)

	require.Equal(t, "private, no-store", recorder.Header().Get("Cache-Control"))
	require.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))
}

func TestGetVertexTaskKeyCreativeMissingStoredKeyFailsClosed(t *testing.T) {
	task := &model.Task{
		TaskID: "task_public_vertex",
		PrivateData: model.TaskPrivateData{
			IdempotencyKey: "opentu-video-local-id",
			UpstreamTaskID: "vertex-upstream-id",
			Key:            "",
		},
	}
	channel := &model.Channel{Key: "sk-current-channel-key"}

	key := getVertexTaskKey(channel, task)

	require.Empty(t, key)
}

func TestGetVertexTaskKeyLegacyCanFallbackToChannelKey(t *testing.T) {
	task := &model.Task{TaskID: "legacy-upstream-id"}
	channel := &model.Channel{Key: "sk-current-channel-key"}

	key := getVertexTaskKey(channel, task)

	require.Equal(t, "sk-current-channel-key", key)
}

func TestCreativeVideoContentPlatformAllowedFailClosed(t *testing.T) {
	require.True(t, creativeVideoContentPlatformAllowed(constant.TaskPlatform("openai")))
	require.True(t, creativeVideoContentPlatformAllowed(constant.TaskPlatform("55")))
	require.True(t, creativeVideoContentPlatformAllowed(constant.TaskPlatform("Gemini")))
	require.False(t, creativeVideoContentPlatformAllowed(constant.TaskPlatformCreativeImage))
	require.False(t, creativeVideoContentPlatformAllowed(constant.TaskPlatformSuno))
	require.False(t, creativeVideoContentPlatformAllowed(constant.TaskPlatformMidjourney))
	require.False(t, creativeVideoContentPlatformAllowed(constant.TaskPlatform("")))
}
