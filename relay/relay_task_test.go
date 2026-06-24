package relay

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

type closeTrackingReadCloser struct {
	io.Reader
	closed bool
}

func (r *closeTrackingReadCloser) Close() error {
	r.closed = true
	return nil
}

func TestTaskSubmitNonOKResponseErrorClosesBody(t *testing.T) {
	body := &closeTrackingReadCloser{Reader: strings.NewReader(`{"error":"upstream rejected"}`)}
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Body:       body,
	}

	taskErr := taskSubmitNonOKResponseError(resp)

	require.NotNil(t, taskErr)
	require.True(t, body.closed, "non-200 task submit response body must be closed")
}

func TestTaskSubmitNonOKResponseErrorDoesNotExposeRawUpstreamBody(t *testing.T) {
	body := &closeTrackingReadCloser{Reader: strings.NewReader(`{"error":"provider rejected","api_key":"sk-leak","callback":"https://evil.example/hook","url":"https://signed.example/private?token=secret"}`)}
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Body:       body,
	}

	taskErr := taskSubmitNonOKResponseError(resp)

	require.NotNil(t, taskErr)
	require.Equal(t, "fail_to_fetch_task", taskErr.Code)
	require.NotContains(t, taskErr.Message, "sk-leak")
	require.NotContains(t, taskErr.Message, "callback")
	require.NotContains(t, taskErr.Message, "signed.example")
	require.NotContains(t, taskErr.Message, "provider rejected")
	require.NotContains(t, taskErr.Message, "secret")
	require.True(t, body.closed, "non-200 task submit response body must still be closed")
}

func TestRealtimeFetchKeyAndTaskIDCreativeMissingStoredKeyFailsClosed(t *testing.T) {
	task := &model.Task{
		TaskID: "task_public_realtime",
		PrivateData: model.TaskPrivateData{
			IdempotencyKey: "creative-request-id",
			UpstreamTaskID: "upstream_realtime",
			Key:            "",
		},
	}
	channel := &model.Channel{Key: "sk-current-channel-key"}

	key, upstreamID, ok := realtimeFetchKeyAndTaskID(task, channel)

	require.False(t, ok)
	require.Empty(t, key)
	require.Empty(t, upstreamID)
}

func TestRealtimeFetchKeyAndTaskIDCreativeUsesStoredKeyAndUpstreamID(t *testing.T) {
	task := &model.Task{
		TaskID: "task_public_realtime",
		PrivateData: model.TaskPrivateData{
			IdempotencyKey: "creative-request-id",
			UpstreamTaskID: "upstream_realtime",
			Key:            "sk-selected-key",
		},
	}
	channel := &model.Channel{Key: "sk-current-channel-key"}

	key, upstreamID, ok := realtimeFetchKeyAndTaskID(task, channel)

	require.True(t, ok)
	require.Equal(t, "sk-selected-key", key)
	require.Equal(t, "upstream_realtime", upstreamID)
}

func TestRealtimeFetchKeyAndTaskIDLegacyCanFallbackToChannelKey(t *testing.T) {
	task := &model.Task{TaskID: "legacy_upstream_id"}
	channel := &model.Channel{Key: "sk-current-channel-key"}

	key, upstreamID, ok := realtimeFetchKeyAndTaskID(task, channel)

	require.True(t, ok)
	require.Equal(t, "sk-current-channel-key", key)
	require.Equal(t, "legacy_upstream_id", upstreamID)
}

func TestTaskModel2DtoRewritesRawResultURLToContentProxy(t *testing.T) {
	task := &model.Task{
		TaskID: "task_public_dto",
		PrivateData: model.TaskPrivateData{
			ResultURL: "https://provider.example/private/video.mp4?X-Amz-Signature=secret",
		},
	}

	dto := TaskModel2Dto(task)

	require.Equal(t, "/v1/videos/task_public_dto/content", dto.ResultURL)
	require.NotContains(t, dto.ResultURL, "provider.example")
	require.NotContains(t, dto.ResultURL, "X-Amz-Signature")
}

func TestTaskModel2DtoRedactsCreativeImageChannelMetadata(t *testing.T) {
	task := &model.Task{
		TaskID:    "task_creative_image_dto",
		Platform:  constant.TaskPlatformCreativeImage,
		ChannelId: 42,
	}
	task.SetData(map[string]any{
		"version":         1,
		"creativeManaged": true,
		"bindingId":       "mock:gpt-image-2:preview",
		"channelId":       42,
		"channel_id":      43,
		"userParams": map[string]any{
			"size": "1024x1024",
		},
	})

	dto := TaskModel2Dto(task)

	var redacted map[string]any
	require.NoError(t, common.Unmarshal(dto.Data, &redacted))
	require.NotContains(t, redacted, "channelId")
	require.NotContains(t, redacted, "channel_id")
	require.Equal(t, true, redacted["creativeManaged"])
	require.Equal(t, "mock:gpt-image-2:preview", redacted["bindingId"])
	require.Equal(t, map[string]any{"size": "1024x1024"}, redacted["userParams"])

	var stored map[string]any
	require.NoError(t, common.Unmarshal(task.Data, &stored))
	require.Equal(t, float64(42), stored["channelId"])
	require.Equal(t, float64(43), stored["channel_id"])
	require.Equal(t, 42, task.ChannelId)
}

func TestSunoTaskModel2DtoRewritesRawProviderURLsToContentProxy(t *testing.T) {
	task := &model.Task{
		TaskID:   "task_suno_public",
		Platform: constant.TaskPlatformSuno,
		Data: []byte(`[
			{
				"id":"clip-1",
				"audio_url":"https://cdn.example/clip-1.mp3?token=secret",
				"image_url":"https://cdn.example/cover.png?token=secret",
				"status":"complete"
			}
		]`),
	}

	dto := SunoTaskModel2DtoForPath(task, "/creative/relay/v1/suno/fetch/task_suno_public")

	require.NotContains(t, string(dto.Data), "cdn.example")
	require.NotContains(t, string(dto.Data), "token=secret")
	require.Contains(t, string(dto.Data), "/creative/relay/v1/suno/fetch/task_suno_public/content")
	require.Contains(t, string(dto.Data), "kind=audio")
	require.Contains(t, string(dto.Data), "clip_id=clip-1")
}
