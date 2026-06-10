package mj

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestMJAdaptorSubmitResponseUsesPublicTaskIDAndKeepsUpstreamPrivate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/creative/relay/v1/mj/submit/imagine", strings.NewReader(`{"prompt":"safe"}`))
	ctx.Set("task_request", &dto.MidjourneyRequest{Prompt: "safe", Action: constant.MjActionImagine})

	adaptor := &TaskAdaptor{}
	upstream := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"code":1,"description":"ok","result":"upstream-secret-id"}`)),
	}
	info := &relaycommon.RelayInfo{TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "task_public_mj"}}

	upstreamID, taskData, taskErr := adaptor.DoResponse(ctx, upstream, info)

	require.Nil(t, taskErr)
	require.Equal(t, "upstream-secret-id", upstreamID)
	var response dto.MidjourneyResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, 1, response.Code)
	require.Equal(t, "task_public_mj", response.Result)
	require.NotContains(t, recorder.Body.String(), "upstream-secret-id")
	var taskDTO dto.MidjourneyDto
	require.NoError(t, common.Unmarshal(taskData, &taskDTO))
	require.Equal(t, "task_public_mj", taskDTO.MjId)
	require.Equal(t, constant.MjActionImagine, taskDTO.Action)
}

func TestMJAdaptorSubmitUsesMJAPISecretHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/creative/relay/v1/mj/submit/imagine", strings.NewReader(`{"prompt":"safe"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	req := httptest.NewRequest(http.MethodPost, "https://mj-upstream.example/mj/submit/imagine", nil)
	adaptor := &TaskAdaptor{}

	require.NoError(t, adaptor.BuildRequestHeader(ctx, req, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ApiKey: "sk-mj-secret"}}))
	require.Equal(t, "sk-mj-secret", req.Header.Get("mj-api-secret"))
	require.Empty(t, req.Header.Get("Authorization"))
}

func TestMJAdaptorFetchUsesMJAPISecretHeader(t *testing.T) {
	var gotSecret string
	var gotAuthorization string
	previousTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "/mj/task/upstream-task/fetch", r.URL.Path)
		gotSecret = r.Header.Get("mj-api-secret")
		gotAuthorization = r.Header.Get("Authorization")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"upstream-task","status":"IN_PROGRESS","progress":"50%"}`)),
			Request:    r,
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previousTransport })

	adaptor := &TaskAdaptor{}
	resp, err := adaptor.FetchTask("https://mj-upstream.example", "sk-stored-mj-key", map[string]any{"task_id": "upstream-task"}, "")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, "sk-stored-mj-key", gotSecret)
	require.Empty(t, gotAuthorization)
}

func TestMJAdaptorParseTaskResultMapsStatusAndImageURL(t *testing.T) {
	adaptor := &TaskAdaptor{}
	result, err := adaptor.ParseTaskResult([]byte(`{"id":"upstream-id","status":"SUCCESS","progress":"100%","imageUrl":"https://cdn.example/image.png"}`))

	require.NoError(t, err)
	require.Equal(t, "upstream-id", result.TaskID)
	require.Equal(t, string(model.TaskStatusSuccess), result.Status)
	require.Equal(t, "100%", result.Progress)
	require.Equal(t, "https://cdn.example/image.png", result.Url)
}

func TestMJAdaptorParseTaskResultDoesNotUseVideoURLAsImageResult(t *testing.T) {
	adaptor := &TaskAdaptor{}
	result, err := adaptor.ParseTaskResult([]byte(`{"id":"upstream-id","status":"SUCCESS","progress":"100%","videoUrl":"https://cdn.example/video.mp4"}`))

	require.NoError(t, err)
	require.Equal(t, "upstream-id", result.TaskID)
	require.Equal(t, string(model.TaskStatusSuccess), result.Status)
	require.Empty(t, result.Url)
}
