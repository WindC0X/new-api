package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
)

type recordingVideoPollingAdaptor struct {
	key  string
	body map[string]any
}

func (a *recordingVideoPollingAdaptor) Init(info *relaycommon.RelayInfo) {}

func (a *recordingVideoPollingAdaptor) FetchTask(baseURL string, key string, body map[string]any, proxy string) (*http.Response, error) {
	a.key = key
	a.body = body
	return &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(`{
			"status": "IN_PROGRESS",
			"progress": "50%"
		}`)),
	}, nil
}

func (a *recordingVideoPollingAdaptor) ParseTaskResult(body []byte) (*relaycommon.TaskInfo, error) {
	return &relaycommon.TaskInfo{
		Status:   model.TaskStatusInProgress,
		Progress: "50%",
	}, nil
}

func (a *recordingVideoPollingAdaptor) AdjustBillingOnComplete(task *model.Task, taskResult *relaycommon.TaskInfo) int {
	return 0
}

func TestUpdateVideoSingleTaskUsesStoredChannelKeyAffinity(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 901, 901, 901
	seedUser(t, userID, 10000)
	seedToken(t, tokenID, userID, "sk-token", 5000)
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:     channelID,
		Name:   "fresh-channel-key-should-not-be-used",
		Key:    "sk-fresh-random-key",
		Status: common.ChannelStatusEnabled,
		Type:   constant.ChannelTypeOpenAI,
	}).Error)

	task := makeTask(userID, channelID, 2000, tokenID, BillingSourceWallet, 0)
	task.TaskID = "task_public_affinity"
	task.PrivateData.UpstreamTaskID = "upstream_affinity"
	task.PrivateData.Key = "sk-original-selected-key"
	task.Status = model.TaskStatusSubmitted
	require.NoError(t, model.DB.Create(task).Error)

	adaptor := &recordingVideoPollingAdaptor{}
	err := updateVideoSingleTask(ctx, adaptor, &model.Channel{
		Id:     channelID,
		Key:    "sk-fresh-random-key",
		Status: common.ChannelStatusEnabled,
		Type:   constant.ChannelTypeOpenAI,
	}, "upstream_affinity", map[string]*model.Task{
		"upstream_affinity": task,
	})

	require.NoError(t, err)
	require.Equal(t, "sk-original-selected-key", adaptor.key)
	require.Equal(t, "upstream_affinity", adaptor.body["task_id"])
	require.Equal(t, string(task.Action), adaptor.body["action"])
}
