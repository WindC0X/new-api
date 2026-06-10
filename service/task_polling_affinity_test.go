package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
)

type recordingVideoPollingAdaptor struct {
	key          string
	body         map[string]any
	responseBody string
	result       *relaycommon.TaskInfo
	adjustQuota  int
}

func (a *recordingVideoPollingAdaptor) Init(info *relaycommon.RelayInfo) {}

func (a *recordingVideoPollingAdaptor) FetchTask(baseURL string, key string, body map[string]any, proxy string) (*http.Response, error) {
	a.key = key
	a.body = body
	responseBody := a.responseBody
	if responseBody == "" {
		responseBody = `{
			"status": "IN_PROGRESS",
			"progress": "50%"
		}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(responseBody)),
	}, nil
}

func (a *recordingVideoPollingAdaptor) ParseTaskResult(body []byte) (*relaycommon.TaskInfo, error) {
	if a.result != nil {
		result := *a.result
		return &result, nil
	}
	return &relaycommon.TaskInfo{
		Status:   model.TaskStatusInProgress,
		Progress: "50%",
	}, nil
}

func (a *recordingVideoPollingAdaptor) AdjustBillingOnComplete(task *model.Task, taskResult *relaycommon.TaskInfo) int {
	return a.adjustQuota
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

type recordingSunoPollingAdaptor struct {
	calls []recordingSunoPollingCall
}

type recordingSunoPollingCall struct {
	baseURL string
	key     string
	ids     []string
}

func (a *recordingSunoPollingAdaptor) Init(info *relaycommon.RelayInfo) {}

func (a *recordingSunoPollingAdaptor) FetchTask(baseURL string, key string, body map[string]any, proxy string) (*http.Response, error) {
	ids := body["ids"].([]string)
	a.calls = append(a.calls, recordingSunoPollingCall{
		baseURL: baseURL,
		key:     key,
		ids:     append([]string(nil), ids...),
	})
	items := make([]dto.SunoDataResponse, 0, len(ids))
	for _, id := range ids {
		items = append(items, dto.SunoDataResponse{
			TaskID: id,
			Status: string(model.TaskStatusInProgress),
		})
	}
	responseBody, err := common.Marshal(dto.TaskResponse[[]dto.SunoDataResponse]{
		Code: dto.TaskSuccessCode,
		Data: items,
	})
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(string(responseBody))),
	}, nil
}

func (a *recordingSunoPollingAdaptor) ParseTaskResult(body []byte) (*relaycommon.TaskInfo, error) {
	return nil, nil
}

func (a *recordingSunoPollingAdaptor) AdjustBillingOnComplete(task *model.Task, taskResult *relaycommon.TaskInfo) int {
	return 0
}

func TestUpdateSunoTasksGroupsByStoredKeyAndUsesChannelBaseURL(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 902, 902, 902
	seedUser(t, userID, 10000)
	seedToken(t, tokenID, userID, "sk-token", 5000)
	baseURL := "https://suno-upstream.example"
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:      channelID,
		Name:    "multi-key-suno",
		Key:     "sk-fresh-a\nsk-fresh-b",
		BaseURL: &baseURL,
		Status:  common.ChannelStatusEnabled,
		Type:    constant.ChannelTypeSunoAPI,
	}).Error)

	taskA := makeTask(userID, channelID, 1000, tokenID, BillingSourceWallet, 0)
	taskA.TaskID = "task_public_suno_a"
	taskA.PrivateData.UpstreamTaskID = "upstream_suno_a"
	taskA.PrivateData.Key = "sk-selected-a"
	taskA.Status = model.TaskStatusSubmitted
	require.NoError(t, model.DB.Create(taskA).Error)

	taskB := makeTask(userID, channelID, 1000, tokenID, BillingSourceWallet, 0)
	taskB.TaskID = "task_public_suno_b"
	taskB.PrivateData.UpstreamTaskID = "upstream_suno_b"
	taskB.PrivateData.Key = "sk-selected-b"
	taskB.Status = model.TaskStatusSubmitted
	require.NoError(t, model.DB.Create(taskB).Error)

	adaptor := &recordingSunoPollingAdaptor{}
	originalGetTaskAdaptor := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(platform constant.TaskPlatform) TaskPollingAdaptor {
		require.Equal(t, constant.TaskPlatformSuno, platform)
		return adaptor
	}
	t.Cleanup(func() { GetTaskAdaptorFunc = originalGetTaskAdaptor })

	err := UpdateSunoTasks(ctx, map[int][]string{
		channelID: []string{"upstream_suno_a", "upstream_suno_b"},
	}, map[string]*model.Task{
		"upstream_suno_a": taskA,
		"upstream_suno_b": taskB,
	})

	require.NoError(t, err)
	require.Len(t, adaptor.calls, 2)
	seen := map[string][]string{}
	for _, call := range adaptor.calls {
		require.Equal(t, baseURL, call.baseURL)
		seen[call.key] = call.ids
	}
	require.Equal(t, []string{"upstream_suno_a"}, seen["sk-selected-a"])
	require.Equal(t, []string{"upstream_suno_b"}, seen["sk-selected-b"])
}

func TestDispatchPlatformUpdateForMidjourneyUsesGenericStoredKeyAffinity(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID = 906, 906, 906
	seedUser(t, userID, 10000)
	seedToken(t, tokenID, userID, "sk-token", 5000)
	baseURL := "https://mj-upstream.example"
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:      channelID,
		Name:    "mj-multi-key",
		Key:     "sk-fresh-mj-key",
		BaseURL: &baseURL,
		Status:  common.ChannelStatusEnabled,
		Type:    constant.ChannelTypeMidjourney,
	}).Error)

	task := makeTask(userID, channelID, 1000, tokenID, BillingSourceWallet, 0)
	task.TaskID = "task_public_mj_affinity"
	task.Platform = constant.TaskPlatformMidjourney
	task.Action = constant.MjActionImagine
	task.PrivateData.UpstreamTaskID = "upstream_mj_affinity"
	task.PrivateData.Key = "sk-selected-mj-key"
	task.Status = model.TaskStatusSubmitted
	require.NoError(t, model.DB.Create(task).Error)

	adaptor := &recordingVideoPollingAdaptor{}
	originalGetTaskAdaptor := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(platform constant.TaskPlatform) TaskPollingAdaptor {
		require.Equal(t, constant.TaskPlatform(constant.TaskPlatformMidjourney), platform)
		return adaptor
	}
	t.Cleanup(func() { GetTaskAdaptorFunc = originalGetTaskAdaptor })

	DispatchPlatformUpdate(constant.TaskPlatformMidjourney, map[int][]string{
		channelID: []string{"upstream_mj_affinity"},
	}, map[string]*model.Task{
		"upstream_mj_affinity": task,
	})

	require.Equal(t, "sk-selected-mj-key", adaptor.key)
	require.Equal(t, "upstream_mj_affinity", adaptor.body["task_id"])
	require.Equal(t, constant.MjActionImagine, adaptor.body["action"])
}

func TestUpdateMidjourneySingleTaskSuccessWithoutImageURLLeavesResultURLEmpty(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, channelID = 907, 907
	seedUser(t, userID, 10000)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, 0, 0, BillingSourceWallet, 0)
	task.TaskID = "task_public_mj_no_image"
	task.Platform = constant.TaskPlatformMidjourney
	task.Action = constant.MjActionImagine
	task.PrivateData.UpstreamTaskID = "upstream_mj_no_image"
	task.PrivateData.Key = "sk-selected-mj-key"
	task.Status = model.TaskStatusInProgress
	require.NoError(t, model.DB.Create(task).Error)

	adaptor := &recordingVideoPollingAdaptor{
		responseBody: `{"id":"upstream_mj_no_image","status":"SUCCESS","progress":"100%"}`,
		result: &relaycommon.TaskInfo{
			TaskID:   "upstream_mj_no_image",
			Status:   model.TaskStatusSuccess,
			Progress: "100%",
		},
	}

	err := updateVideoSingleTask(ctx, adaptor, &model.Channel{
		Id:     channelID,
		Key:    "sk-fresh-mj-key",
		Status: common.ChannelStatusEnabled,
		Type:   constant.ChannelTypeMidjourney,
	}, "upstream_mj_no_image", map[string]*model.Task{
		"upstream_mj_no_image": task,
	})

	require.NoError(t, err)
	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	require.Equal(t, model.TaskStatusSuccess, string(reloaded.Status))
	require.Empty(t, reloaded.PrivateData.ResultURL)
	require.NotContains(t, reloaded.PrivateData.ResultURL, "/v1/videos/")
}

func TestUpdateMidjourneySingleTaskFailureRefundsOnlyCASWinner(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 908, 908, 908
	const initQuota, preConsumed, tokenRemain = 10000, 2000, 5000
	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-token", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.TaskID = "task_public_mj_cas_refund"
	task.Platform = constant.TaskPlatformMidjourney
	task.Action = constant.MjActionImagine
	task.PrivateData.UpstreamTaskID = "upstream_mj_cas_refund"
	task.PrivateData.Key = "sk-selected-mj-key"
	task.Status = model.TaskStatusInProgress
	require.NoError(t, model.DB.Create(task).Error)

	var first model.Task
	require.NoError(t, model.DB.First(&first, task.ID).Error)
	var second model.Task
	require.NoError(t, model.DB.First(&second, task.ID).Error)

	adaptor := &recordingVideoPollingAdaptor{
		responseBody: `{"id":"upstream_mj_cas_refund","status":"FAILURE","failReason":"upstream failed"}`,
		result: &relaycommon.TaskInfo{
			TaskID: "upstream_mj_cas_refund",
			Status: model.TaskStatusFailure,
			Reason: "upstream failed",
		},
	}
	channel := &model.Channel{
		Id:     channelID,
		Key:    "sk-fresh-mj-key",
		Status: common.ChannelStatusEnabled,
		Type:   constant.ChannelTypeMidjourney,
	}

	require.NoError(t, updateVideoSingleTask(ctx, adaptor, channel, "upstream_mj_cas_refund", map[string]*model.Task{
		"upstream_mj_cas_refund": &first,
	}))
	require.NoError(t, updateVideoSingleTask(ctx, adaptor, channel, "upstream_mj_cas_refund", map[string]*model.Task{
		"upstream_mj_cas_refund": &second,
	}))

	require.Equal(t, initQuota+preConsumed, getUserQuota(t, userID))
	require.Equal(t, tokenRemain+preConsumed, getTokenRemainQuota(t, tokenID))
	require.Equal(t, int64(1), countLogs(t))
}

func TestUpdateMidjourneySingleTaskSubscriptionFailureRefundsOnlyCASWinner(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID, subscriptionID = 909, 909, 909, 909
	const preConsumed, tokenRemain = 3000, 7000
	const subscriptionUsed int64 = 50000
	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "sk-token", tokenRemain)
	seedChannel(t, channelID)
	seedSubscription(t, subscriptionID, userID, 100000, subscriptionUsed)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceSubscription, subscriptionID)
	task.TaskID = "task_public_mj_sub_cas_refund"
	task.Platform = constant.TaskPlatformMidjourney
	task.Action = constant.MjActionImagine
	task.PrivateData.UpstreamTaskID = "upstream_mj_sub_cas_refund"
	task.PrivateData.Key = "sk-selected-mj-key"
	task.Status = model.TaskStatusInProgress
	require.NoError(t, model.DB.Create(task).Error)

	var first model.Task
	require.NoError(t, model.DB.First(&first, task.ID).Error)
	var second model.Task
	require.NoError(t, model.DB.First(&second, task.ID).Error)

	adaptor := &recordingVideoPollingAdaptor{
		responseBody: `{"id":"upstream_mj_sub_cas_refund","status":"FAILURE","failReason":"subscription upstream failed"}`,
		result: &relaycommon.TaskInfo{
			TaskID: "upstream_mj_sub_cas_refund",
			Status: model.TaskStatusFailure,
			Reason: "subscription upstream failed",
		},
	}
	channel := &model.Channel{
		Id:     channelID,
		Key:    "sk-fresh-mj-key",
		Status: common.ChannelStatusEnabled,
		Type:   constant.ChannelTypeMidjourney,
	}

	require.NoError(t, updateVideoSingleTask(ctx, adaptor, channel, "upstream_mj_sub_cas_refund", map[string]*model.Task{
		"upstream_mj_sub_cas_refund": &first,
	}))
	require.NoError(t, updateVideoSingleTask(ctx, adaptor, channel, "upstream_mj_sub_cas_refund", map[string]*model.Task{
		"upstream_mj_sub_cas_refund": &second,
	}))

	require.Equal(t, subscriptionUsed-int64(preConsumed), getSubscriptionUsed(t, subscriptionID))
	require.Equal(t, tokenRemain+preConsumed, getTokenRemainQuota(t, tokenID))
	require.Equal(t, int64(1), countLogs(t))
}

func TestUpdateMidjourneySingleTaskSuccessSettlesOnlyCASWinner(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 910, 910, 910
	const initQuota, preConsumed, actualQuota, tokenRemain = 10000, 3000, 1000, 7000
	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-token", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.TaskID = "task_public_mj_cas_settle"
	task.Platform = constant.TaskPlatformMidjourney
	task.Action = constant.MjActionImagine
	task.PrivateData.UpstreamTaskID = "upstream_mj_cas_settle"
	task.PrivateData.Key = "sk-selected-mj-key"
	task.Status = model.TaskStatusInProgress
	require.NoError(t, model.DB.Create(task).Error)

	var first model.Task
	require.NoError(t, model.DB.First(&first, task.ID).Error)
	var second model.Task
	require.NoError(t, model.DB.First(&second, task.ID).Error)

	adaptor := &recordingVideoPollingAdaptor{
		responseBody: `{"id":"upstream_mj_cas_settle","status":"SUCCESS","progress":"100%","imageUrl":"https://cdn.example/mj.png"}`,
		result: &relaycommon.TaskInfo{
			TaskID:   "upstream_mj_cas_settle",
			Status:   model.TaskStatusSuccess,
			Progress: "100%",
			Url:      "https://cdn.example/mj.png",
		},
		adjustQuota: actualQuota,
	}
	channel := &model.Channel{
		Id:     channelID,
		Key:    "sk-fresh-mj-key",
		Status: common.ChannelStatusEnabled,
		Type:   constant.ChannelTypeMidjourney,
	}

	require.NoError(t, updateVideoSingleTask(ctx, adaptor, channel, "upstream_mj_cas_settle", map[string]*model.Task{
		"upstream_mj_cas_settle": &first,
	}))
	require.NoError(t, updateVideoSingleTask(ctx, adaptor, channel, "upstream_mj_cas_settle", map[string]*model.Task{
		"upstream_mj_cas_settle": &second,
	}))

	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	require.EqualValues(t, model.TaskStatusSuccess, reloaded.Status)
	require.Equal(t, "https://cdn.example/mj.png", reloaded.PrivateData.ResultURL)
	require.Equal(t, initQuota+(preConsumed-actualQuota), getUserQuota(t, userID))
	require.Equal(t, tokenRemain+(preConsumed-actualQuota), getTokenRemainQuota(t, tokenID))
	require.Equal(t, int64(1), countLogs(t))
}

func TestUpdateSunoTaskFromResponseRefundsOnlyCASWinner(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 903, 903, 903
	const initQuota, preConsumed, tokenRemain = 10000, 2000, 5000
	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-token", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.TaskID = "task_public_suno_cas"
	task.PrivateData.UpstreamTaskID = "upstream_suno_cas"
	task.Status = model.TaskStatusInProgress
	require.NoError(t, model.DB.Create(task).Error)

	var first model.Task
	require.NoError(t, model.DB.First(&first, task.ID).Error)
	var second model.Task
	require.NoError(t, model.DB.First(&second, task.ID).Error)
	response := dto.SunoDataResponse{
		TaskID:     "upstream_suno_cas",
		Status:     string(model.TaskStatusFailure),
		FailReason: "upstream failed",
	}

	firstWon, err := updateSunoTaskFromResponse(ctx, &first, response, nil)
	require.NoError(t, err)
	secondWon, err := updateSunoTaskFromResponse(ctx, &second, response, nil)
	require.NoError(t, err)

	require.True(t, firstWon)
	require.False(t, secondWon)
	require.Equal(t, initQuota+preConsumed, getUserQuota(t, userID))
	require.Equal(t, tokenRemain+preConsumed, getTokenRemainQuota(t, tokenID))
	require.Equal(t, int64(1), countLogs(t))
}

func TestUpdateSunoTaskFromResponseSubscriptionRefundsOnlyCASWinner(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID, subscriptionID = 904, 904, 904, 904
	const preConsumed, tokenRemain = 3000, 7000
	const subscriptionUsed int64 = 50000
	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "sk-sub-token", tokenRemain)
	seedChannel(t, channelID)
	seedSubscription(t, subscriptionID, userID, 100000, subscriptionUsed)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceSubscription, subscriptionID)
	task.TaskID = "task_public_suno_sub_cas"
	task.PrivateData.UpstreamTaskID = "upstream_suno_sub_cas"
	task.Status = model.TaskStatusInProgress
	require.NoError(t, model.DB.Create(task).Error)

	var first model.Task
	require.NoError(t, model.DB.First(&first, task.ID).Error)
	var second model.Task
	require.NoError(t, model.DB.First(&second, task.ID).Error)
	response := dto.SunoDataResponse{
		TaskID:     "upstream_suno_sub_cas",
		Status:     string(model.TaskStatusFailure),
		FailReason: "subscription upstream failed",
	}

	firstWon, err := updateSunoTaskFromResponse(ctx, &first, response, nil)
	require.NoError(t, err)
	secondWon, err := updateSunoTaskFromResponse(ctx, &second, response, nil)
	require.NoError(t, err)

	require.True(t, firstWon)
	require.False(t, secondWon)
	require.Equal(t, subscriptionUsed-int64(preConsumed), getSubscriptionUsed(t, subscriptionID))
	require.Equal(t, tokenRemain+preConsumed, getTokenRemainQuota(t, tokenID))
	require.Equal(t, int64(1), countLogs(t))
}

func TestUpdateSunoTaskFromResponseSuccessOnlyCASWinnerSettles(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 905, 905, 905
	seedUser(t, userID, 10000)
	seedToken(t, tokenID, userID, "sk-success-token", 5000)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, 1000, tokenID, BillingSourceWallet, 0)
	task.TaskID = "task_public_suno_success_cas"
	task.PrivateData.UpstreamTaskID = "upstream_suno_success_cas"
	task.PrivateData.BillingContext.PerCallBilling = true
	task.Status = model.TaskStatusInProgress
	require.NoError(t, model.DB.Create(task).Error)

	var first model.Task
	require.NoError(t, model.DB.First(&first, task.ID).Error)
	var second model.Task
	require.NoError(t, model.DB.First(&second, task.ID).Error)
	response := dto.SunoDataResponse{
		TaskID: "upstream_suno_success_cas",
		Status: string(model.TaskStatusSuccess),
	}
	adaptor := &recordingSunoPollingAdaptor{}

	firstWon, err := updateSunoTaskFromResponse(ctx, &first, response, adaptor)
	require.NoError(t, err)
	secondWon, err := updateSunoTaskFromResponse(ctx, &second, response, adaptor)
	require.NoError(t, err)

	require.True(t, firstWon)
	require.False(t, secondWon)
	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	require.EqualValues(t, model.TaskStatusSuccess, reloaded.Status)
	require.Equal(t, "100%", reloaded.Progress)
	require.Equal(t, int64(0), countLogs(t))
}
