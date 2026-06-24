package service

import (
	"context"
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
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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

func TestUpdateVideoSingleTaskCreativeMissingStoredKeyFailsClosed(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 911, 911, 911
	const initQuota, preConsumed, tokenRemain = 10000, 2000, 5000
	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-token", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.TaskID = "task_public_missing_key"
	task.PrivateData.UpstreamTaskID = "upstream_missing_key"
	task.PrivateData.IdempotencyKey = "creative-request-id"
	task.PrivateData.Key = ""
	task.Status = model.TaskStatusSubmitted
	require.NoError(t, model.DB.Create(task).Error)

	adaptor := &recordingVideoPollingAdaptor{}
	err := updateVideoSingleTask(ctx, adaptor, &model.Channel{
		Id:     channelID,
		Key:    "sk-fresh-random-key",
		Status: common.ChannelStatusEnabled,
		Type:   constant.ChannelTypeOpenAI,
	}, "upstream_missing_key", map[string]*model.Task{
		"upstream_missing_key": task,
	})

	require.Error(t, err)
	require.Empty(t, adaptor.key, "provider fetch must not use the current channel key for a creative task")
	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	require.EqualValues(t, model.TaskStatusFailure, reloaded.Status)
	require.Equal(t, initQuota+preConsumed, getUserQuota(t, userID))
	require.Equal(t, tokenRemain+preConsumed, getTokenRemainQuota(t, tokenID))
	require.Equal(t, int64(1), countLogs(t))
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

func TestUpdateSunoTasksCreativeMissingStoredKeyFailsClosed(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 912, 912, 912
	const initQuota, preConsumed, tokenRemain = 10000, 1500, 4000
	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-token", tokenRemain)
	baseURL := "https://suno-upstream.example"
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:      channelID,
		Name:    "multi-key-suno",
		Key:     "sk-fresh-a\nsk-fresh-b",
		BaseURL: &baseURL,
		Status:  common.ChannelStatusEnabled,
		Type:    constant.ChannelTypeSunoAPI,
	}).Error)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.TaskID = "task_public_suno_missing_key"
	task.PrivateData.UpstreamTaskID = "upstream_suno_missing_key"
	task.PrivateData.IdempotencyKey = "creative-suno-request-id"
	task.PrivateData.Key = ""
	task.Status = model.TaskStatusSubmitted
	require.NoError(t, model.DB.Create(task).Error)

	adaptor := &recordingSunoPollingAdaptor{}
	originalGetTaskAdaptor := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(platform constant.TaskPlatform) TaskPollingAdaptor {
		require.Equal(t, constant.TaskPlatformSuno, platform)
		return adaptor
	}
	t.Cleanup(func() { GetTaskAdaptorFunc = originalGetTaskAdaptor })

	err := UpdateSunoTasks(ctx, map[int][]string{
		channelID: []string{"upstream_suno_missing_key"},
	}, map[string]*model.Task{
		"upstream_suno_missing_key": task,
	})

	require.NoError(t, err)
	require.Empty(t, adaptor.calls, "provider fetch must not use the current channel key for a creative Suno task")
	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	require.EqualValues(t, model.TaskStatusFailure, reloaded.Status)
	require.Equal(t, initQuota+preConsumed, getUserQuota(t, userID))
	require.Equal(t, tokenRemain+preConsumed, getTokenRemainQuota(t, tokenID))
	require.Equal(t, int64(1), countLogs(t))
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

func TestUpdateVideoTasksChannelMissingFailsEachTaskWithCASAndRefund(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, missingChannelID = 913, 913, 913
	const initQuota, preConsumed, tokenRemain = 10000, 1800, 5000
	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-token", tokenRemain)

	task := makeTask(userID, missingChannelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.TaskID = "task_public_channel_missing"
	task.PrivateData.UpstreamTaskID = "upstream_channel_missing"
	task.PrivateData.Key = "sk-selected-key"
	task.Status = model.TaskStatusInProgress
	require.NoError(t, model.DB.Create(task).Error)

	err := UpdateVideoTasks(ctx, constant.TaskPlatform("openai"), map[int][]string{
		missingChannelID: []string{"upstream_channel_missing"},
	}, map[string]*model.Task{
		"upstream_channel_missing": task,
	})

	require.NoError(t, err)
	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	require.EqualValues(t, model.TaskStatusFailure, reloaded.Status)
	require.Equal(t, initQuota+preConsumed, getUserQuota(t, userID))
	require.Equal(t, tokenRemain+preConsumed, getTokenRemainQuota(t, tokenID))
	require.Equal(t, int64(1), countLogs(t))
}

func TestMarkTasksFailedWithCASAndRefundNullUpstreamOnlyOnce(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 914, 914, 914
	const initQuota, preConsumed, tokenRemain = 10000, 1200, 4000
	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-token", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.TaskID = "task_public_null_upstream"
	task.PrivateData.UpstreamTaskID = ""
	task.Status = model.TaskStatusSubmitted
	require.NoError(t, model.DB.Create(task).Error)

	var first model.Task
	require.NoError(t, model.DB.First(&first, task.ID).Error)
	var second model.Task
	require.NoError(t, model.DB.First(&second, task.ID).Error)

	require.Equal(t, 1, markTasksFailedWithCASAndRefund(ctx, []*model.Task{&first}, "upstream task id is empty"))
	require.Equal(t, 0, markTasksFailedWithCASAndRefund(ctx, []*model.Task{&second}, "upstream task id is empty"))

	require.Equal(t, initQuota+preConsumed, getUserQuota(t, userID))
	require.Equal(t, tokenRemain+preConsumed, getTokenRemainQuota(t, tokenID))
	require.Equal(t, int64(1), countLogs(t))
}

func TestCollectPollingTaskBucketsCreativeMissingUpstreamDoesNotFallbackToPublicTaskID(t *testing.T) {
	task := makeTask(1, 2, 1000, 0, BillingSourceWallet, 0)
	task.TaskID = "task_public_should_not_be_upstream"
	task.PrivateData.IdempotencyKey = "creative-request-id"
	task.PrivateData.UpstreamTaskID = ""

	taskChannelM, taskM, nullTasks, ambiguousExpiredTasks := collectPollingTaskBuckets([]*model.Task{task})

	require.Empty(t, taskChannelM)
	require.Empty(t, taskM)
	require.Len(t, nullTasks, 1)
	require.Empty(t, ambiguousExpiredTasks)
	require.Equal(t, task.TaskID, nullTasks[0].TaskID)
}

func TestCollectPollingTaskBucketsSkipsCreativeImageSubmitInFlight(t *testing.T) {
	task := makeTask(1, 2, 1000, 0, BillingSourceWallet, 0)
	task.Platform = constant.TaskPlatformCreativeImage
	task.TaskID = "task_public_submit_in_flight"
	task.Status = model.TaskStatusSubmitted
	task.PrivateData.IdempotencyKey = "creative-request-id"
	task.PrivateData.ProviderSubmitInFlight = true
	task.PrivateData.ProviderSubmitInFlightAt = common.GetTimestamp()
	task.PrivateData.UpstreamTaskID = ""

	taskChannelM, taskM, nullTasks, ambiguousExpiredTasks := collectPollingTaskBuckets([]*model.Task{task})

	require.Empty(t, taskChannelM)
	require.Empty(t, taskM)
	require.Empty(t, nullTasks)
	require.Empty(t, ambiguousExpiredTasks)
}

func TestCollectPollingTaskBucketsExpiresCreativeImageSubmitInFlight(t *testing.T) {
	task := makeTask(1, 2, 1000, 0, BillingSourceWallet, 0)
	task.Platform = constant.TaskPlatformCreativeImage
	task.TaskID = "task_public_submit_in_flight_expired"
	task.Status = model.TaskStatusSubmitted
	task.PrivateData.IdempotencyKey = "creative-request-id"
	task.PrivateData.ProviderSubmitInFlight = true
	task.PrivateData.ProviderSubmitInFlightAt = common.GetTimestamp() - creativeImageSubmitInFlightTimeoutSeconds - 1
	task.PrivateData.UpstreamTaskID = ""

	taskChannelM, taskM, nullTasks, ambiguousExpiredTasks := collectPollingTaskBuckets([]*model.Task{task})

	require.Empty(t, taskChannelM)
	require.Empty(t, taskM)
	require.Len(t, nullTasks, 1)
	require.Empty(t, ambiguousExpiredTasks)
	require.Equal(t, task.TaskID, nullTasks[0].TaskID)
}

func TestCollectPollingTaskBucketsSkipsAmbiguousCreativeImageSubmit(t *testing.T) {
	task := makeTask(1, 2, 1000, 0, BillingSourceWallet, 0)
	task.Platform = constant.TaskPlatformCreativeImage
	task.TaskID = "task_public_ambiguous_submit"
	task.PrivateData.IdempotencyKey = "creative-request-id"
	task.PrivateData.ProviderSubmitAmbiguous = true
	task.PrivateData.ProviderSubmitAmbiguousAt = common.GetTimestamp()
	task.PrivateData.UpstreamTaskID = ""

	taskChannelM, taskM, nullTasks, ambiguousExpiredTasks := collectPollingTaskBuckets([]*model.Task{task})

	require.Empty(t, taskChannelM)
	require.Empty(t, taskM)
	require.Empty(t, nullTasks)
	require.Empty(t, ambiguousExpiredTasks)
}

func TestCollectPollingTaskBucketsExpiresAmbiguousCreativeImageSubmit(t *testing.T) {
	task := makeTask(1, 2, 1000, 0, BillingSourceWallet, 0)
	task.Platform = constant.TaskPlatformCreativeImage
	task.TaskID = "task_public_ambiguous_submit_expired"
	task.PrivateData.IdempotencyKey = "creative-request-id"
	task.PrivateData.ProviderSubmitAmbiguous = true
	task.PrivateData.ProviderSubmitAmbiguousAt = common.GetTimestamp() - creativeImageAmbiguousSubmitTimeoutSeconds - 1
	task.PrivateData.UpstreamTaskID = ""

	taskChannelM, taskM, nullTasks, ambiguousExpiredTasks := collectPollingTaskBuckets([]*model.Task{task})

	require.Empty(t, taskChannelM)
	require.Empty(t, taskM)
	require.Empty(t, nullTasks)
	require.Len(t, ambiguousExpiredTasks, 1)
	require.Equal(t, task.TaskID, ambiguousExpiredTasks[0].TaskID)
}

func TestFailTaskWithCASAndRefundMarksCreativeImageSubmitUnrecoverable(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	const userID, tokenID, channelID = 918, 918, 918
	const initQuota, preConsumed, tokenRemain = 10000, 1200, 4000
	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-token-unrecoverable", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.Platform = constant.TaskPlatformCreativeImage
	task.TaskID = "task_public_submit_unrecoverable"
	task.Status = model.TaskStatusInProgress
	task.PrivateData.ProviderSubmitAmbiguous = true
	task.PrivateData.ProviderSubmitAmbiguousAt = common.GetTimestamp() - creativeImageAmbiguousSubmitTimeoutSeconds - 1
	task.PrivateData.UpstreamTaskID = ""
	require.NoError(t, model.DB.Create(task).Error)

	won, err := failTaskWithCASAndRefund(ctx, task, creativeImageProviderSubmitUnrecoverableReason)
	require.NoError(t, err)
	require.True(t, won)

	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	require.EqualValues(t, model.TaskStatusFailure, reloaded.Status)
	require.Contains(t, reloaded.FailReason, "unrecoverable")
	require.True(t, reloaded.PrivateData.ProviderSubmitUnrecoverable)
	require.NotZero(t, reloaded.PrivateData.ProviderSubmitUnrecoverableAt)
	require.Equal(t, initQuota+preConsumed, getUserQuota(t, userID))
	require.Equal(t, tokenRemain+preConsumed, getTokenRemainQuota(t, tokenID))
}

func TestSweepTimedOutTasksDefersCreativeProviderSubmitStates(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	previousTimeout := constant.TaskTimeoutMinutes
	constant.TaskTimeoutMinutes = 1
	t.Cleanup(func() { constant.TaskTimeoutMinutes = previousTimeout })

	const userID, tokenID, channelID = 915, 915, 915
	const initQuota, preConsumed, tokenRemain = 10000, 1200, 4000
	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-token", tokenRemain)
	seedChannel(t, channelID)

	oldSubmitTime := common.GetTimestamp() - int64(constant.TaskTimeoutMinutes*60) - 60
	freshProviderSubmitAt := common.GetTimestamp()
	inFlight := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	inFlight.Platform = constant.TaskPlatformCreativeImage
	inFlight.TaskID = "task_public_sweep_in_flight"
	inFlight.Status = model.TaskStatusSubmitted
	inFlight.SubmitTime = oldSubmitTime
	inFlight.Progress = "0%"
	inFlight.PrivateData.ProviderSubmitInFlight = true
	inFlight.PrivateData.ProviderSubmitInFlightAt = freshProviderSubmitAt
	inFlight.PrivateData.UpstreamTaskID = ""
	require.NoError(t, model.DB.Create(inFlight).Error)

	ambiguous := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	ambiguous.Platform = constant.TaskPlatformCreativeImage
	ambiguous.TaskID = "task_public_sweep_ambiguous"
	ambiguous.Status = model.TaskStatusSubmitted
	ambiguous.SubmitTime = oldSubmitTime
	ambiguous.Progress = "0%"
	ambiguous.PrivateData.ProviderSubmitAmbiguous = true
	ambiguous.PrivateData.ProviderSubmitAmbiguousAt = freshProviderSubmitAt
	ambiguous.PrivateData.UpstreamTaskID = ""
	require.NoError(t, model.DB.Create(ambiguous).Error)

	sweepTimedOutTasks(ctx)

	var reloadedInFlight model.Task
	require.NoError(t, model.DB.First(&reloadedInFlight, inFlight.ID).Error)
	require.EqualValues(t, model.TaskStatusSubmitted, reloadedInFlight.Status)
	require.Empty(t, reloadedInFlight.FailReason)

	var reloadedAmbiguous model.Task
	require.NoError(t, model.DB.First(&reloadedAmbiguous, ambiguous.ID).Error)
	require.EqualValues(t, model.TaskStatusSubmitted, reloadedAmbiguous.Status)
	require.Empty(t, reloadedAmbiguous.FailReason)

	require.Equal(t, initQuota, getUserQuota(t, userID))
	require.Equal(t, tokenRemain, getTokenRemainQuota(t, tokenID))
	require.Equal(t, int64(0), countLogs(t))
}

func TestDispatchPlatformUpdateCreativeImagePollsLiveTaskToSuccess(t *testing.T) {
	truncate(t)
	installCreativeAssetRuntimeForPollingTest(t)
	previousClient := httpClient
	defer func() { httpClient = previousClient }()
	fetchSetting := system_setting.GetFetchSetting()
	previousSSRFProtection := fetchSetting.EnableSSRFProtection
	fetchSetting.EnableSSRFProtection = false
	t.Cleanup(func() { fetchSetting.EnableSSRFProtection = previousSSRFProtection })

	const userID, tokenID, channelID = 921, 921, 921
	seedUser(t, userID, 10000)
	seedToken(t, tokenID, userID, "sk-token", 5000)

	var seenAuth string
	imageHits := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tasks/dm-bg-1":
			seenAuth = r.Header.Get("Authorization")
			require.Equal(t, "/v1/tasks/dm-bg-1", r.URL.RequestURI())
			_, _ = w.Write([]byte(`{"id":"dm-bg-1","state":"succeeded","data":{"images":[{"url":"` + providerResultURL(r, "/background.png?signature=secret") + `"}]}}`))
		case "/background.png":
			imageHits++
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	httpClient = provider.Client()

	baseURL := provider.URL
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:      channelID,
		Name:    "creative-live",
		Key:     "fresh-channel-key-should-not-be-used",
		BaseURL: &baseURL,
		Status:  common.ChannelStatusEnabled,
		Type:    constant.ChannelTypeOpenAI,
	}).Error)

	task := makeTask(userID, channelID, 2000, tokenID, BillingSourceWallet, 0)
	task.Platform = constant.TaskPlatformCreativeImage
	task.TaskID = "task_public_creative_background"
	task.Status = model.TaskStatusSubmitted
	task.Progress = "0%"
	task.Action = "image.generate"
	task.PrivateData.UpstreamTaskID = "dm-bg-1"
	task.PrivateData.Key = "duomi-original-key"
	task.PrivateData.ProviderEndpoint = baseURL
	task.PrivateData.IdempotencyKey = "creative-request-id"
	task.SetData(creativeImagePollingMetadata{
		Version:           1,
		CreativeManaged:   true,
		BindingId:         "duomi:gpt-image-2:live",
		ProviderModelId:   "gpt-image-2",
		PriceModelId:      "duomi-gpt-image-2-price",
		AdapterPreset:     CreativeImageAdapterPresetDuomiLive,
		ParameterTemplate: "duomi_gpt_image",
		ChannelId:         channelID,
		UserParams:        map[string]any{"aspectRatio": "1:1", "imageSize": "1K"},
	})
	require.NoError(t, model.DB.Create(task).Error)

	originalGetTaskAdaptor := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = func(platform constant.TaskPlatform) TaskPollingAdaptor {
		t.Fatalf("creative_image polling must not use generic video task adaptors, got %s", platform)
		return nil
	}
	t.Cleanup(func() { GetTaskAdaptorFunc = originalGetTaskAdaptor })

	DispatchPlatformUpdate(constant.TaskPlatformCreativeImage, map[int][]string{
		channelID: []string{"dm-bg-1"},
	}, map[string]*model.Task{
		"dm-bg-1": task,
	})

	require.Equal(t, "duomi-original-key", seenAuth)
	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	require.EqualValues(t, model.TaskStatusSuccess, reloaded.Status)
	require.Equal(t, "100%", reloaded.Progress)
	assetID, ok := CreativeAssetContentURLAssetID(reloaded.PrivateData.ResultURL)
	require.True(t, ok, "polling success should persist a durable creative asset URL, got %s", reloaded.PrivateData.ResultURL)
	require.NotEmpty(t, assetID)
	require.Equal(t, 1, imageHits)

	var outbox model.TaskBillingOutbox
	require.NoError(t, model.DB.Where("task_row_id = ? AND operation = ?", task.ID, model.TaskBillingOutboxOperationTerminalSettle).First(&outbox).Error)
	require.Equal(t, model.TaskBillingOutboxStatusDone, outbox.Status)
}

func TestDispatchPlatformUpdateCreativeImageMissingStoredKeyWithoutIdempotencyFailsClosed(t *testing.T) {
	truncate(t)
	installCreativeAssetRuntimeForPollingTest(t)
	previousClient := httpClient
	defer func() { httpClient = previousClient }()
	fetchSetting := system_setting.GetFetchSetting()
	previousSSRFProtection := fetchSetting.EnableSSRFProtection
	fetchSetting.EnableSSRFProtection = false
	t.Cleanup(func() { fetchSetting.EnableSSRFProtection = previousSSRFProtection })

	const userID, tokenID, channelID = 922, 922, 922
	const initQuota, preConsumed, tokenRemain = 10000, 2000, 5000
	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-token", tokenRemain)

	providerHits := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerHits++
		http.Error(w, "must not poll with fallback key", http.StatusUnauthorized)
	}))
	defer provider.Close()
	httpClient = provider.Client()

	baseURL := provider.URL
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:      channelID,
		Name:    "creative-live",
		Key:     "fresh-channel-key-must-not-be-used",
		BaseURL: &baseURL,
		Status:  common.ChannelStatusEnabled,
		Type:    constant.ChannelTypeOpenAI,
	}).Error)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.Platform = constant.TaskPlatformCreativeImage
	task.TaskID = "task_public_creative_missing_key_no_idempotency"
	task.Status = model.TaskStatusSubmitted
	task.Progress = "0%"
	task.Action = "image.generate"
	task.PrivateData.UpstreamTaskID = "dm-bg-missing-key"
	task.PrivateData.Key = ""
	task.PrivateData.ProviderEndpoint = baseURL
	task.PrivateData.IdempotencyKey = ""
	task.SetData(creativeImagePollingMetadata{
		Version:           1,
		CreativeManaged:   true,
		BindingId:         "duomi:gpt-image-2:live",
		ProviderModelId:   "gpt-image-2",
		PriceModelId:      "duomi-gpt-image-2-price",
		AdapterPreset:     CreativeImageAdapterPresetDuomiLive,
		ParameterTemplate: "duomi_gpt_image",
		ChannelId:         channelID,
	})
	require.NoError(t, model.DB.Create(task).Error)

	DispatchPlatformUpdate(constant.TaskPlatformCreativeImage, map[int][]string{
		channelID: []string{"dm-bg-missing-key"},
	}, map[string]*model.Task{
		"dm-bg-missing-key": task,
	})

	require.Equal(t, 0, providerHits, "creative live polling must fail closed before using the current channel key")
	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	require.EqualValues(t, model.TaskStatusFailure, reloaded.Status)
	require.Equal(t, initQuota+preConsumed, getUserQuota(t, userID))
	require.Equal(t, tokenRemain+preConsumed, getTokenRemainQuota(t, tokenID))
}

func providerResultURL(r *http.Request, path string) string {
	return "http://" + r.Host + path
}

func installCreativeAssetRuntimeForPollingTest(t *testing.T) {
	t.Helper()
	require.NoError(t, model.DB.AutoMigrate(&model.CreativeAsset{}, &model.CreativeAssetQuota{}, &model.CreativeDocumentAssetRef{}, &model.CreativeAssetLifecycleOutbox{}))
	require.NoError(t, model.DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&model.CreativeDocumentAssetRef{}).Error)
	require.NoError(t, model.DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&model.CreativeAssetLifecycleOutbox{}).Error)
	require.NoError(t, model.DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&model.CreativeAssetQuota{}).Error)
	require.NoError(t, model.DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&model.CreativeAsset{}).Error)
	runtime, err := NewCreativeAssetRuntime(CreativeAssetConfig{
		Enabled:                   true,
		RolloutMode:               CreativeAssetRolloutLocal,
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
	SetCreativeAssetRuntimeForTest(t, runtime)
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
