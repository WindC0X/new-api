package relay

import (
	"io"
	"net/http"
	"strings"
	"testing"

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
