package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// TaskPollingAdaptor 定义轮询所需的最小适配器接口，避免 service -> relay 的循环依赖
type TaskPollingAdaptor interface {
	Init(info *relaycommon.RelayInfo)
	FetchTask(baseURL string, key string, body map[string]any, proxy string) (*http.Response, error)
	ParseTaskResult(body []byte) (*relaycommon.TaskInfo, error)
	// AdjustBillingOnComplete 在任务到达终态（成功/失败）时由轮询循环调用。
	// 返回正数触发差额结算（补扣/退还），返回 0 保持预扣费金额不变。
	AdjustBillingOnComplete(task *model.Task, taskResult *relaycommon.TaskInfo) int
}

// GetTaskAdaptorFunc 由 main 包注入，用于获取指定平台的任务适配器。
// 打破 service -> relay -> relay/channel -> service 的循环依赖。
var GetTaskAdaptorFunc func(platform constant.TaskPlatform) TaskPollingAdaptor

// sweepTimedOutTasks 在主轮询之前独立清理超时任务。
// 每次最多处理 100 条，剩余的下个周期继续处理。
// 使用 per-task CAS (UpdateWithStatus) 防止覆盖被正常轮询已推进的任务。
func sweepTimedOutTasks(ctx context.Context) {
	if constant.TaskTimeoutMinutes <= 0 {
		return
	}
	cutoff := time.Now().Unix() - int64(constant.TaskTimeoutMinutes)*60
	tasks := model.GetTimedOutUnfinishedTasks(cutoff, 100)
	if len(tasks) == 0 {
		return
	}

	const legacyTaskCutoff int64 = 1740182400 // 2026-02-22 00:00:00 UTC
	reason := fmt.Sprintf("任务超时（%d分钟）", constant.TaskTimeoutMinutes)
	legacyReason := "任务超时（旧系统遗留任务，不进行退款，请联系管理员）"
	now := time.Now().Unix()
	timedOutCount := 0

	for _, task := range tasks {
		isLegacy := task.SubmitTime > 0 && task.SubmitTime < legacyTaskCutoff

		if !isLegacy {
			won, err := failTaskWithCASAndRefund(ctx, task, reason)
			if err != nil {
				logger.LogError(ctx, fmt.Sprintf("sweepTimedOutTasks CAS update error for task %s: %v", task.TaskID, err))
				continue
			}
			if !won {
				logger.LogInfo(ctx, fmt.Sprintf("sweepTimedOutTasks: task %s already transitioned, skip", task.TaskID))
				continue
			}
			timedOutCount++
			continue
		}

		oldStatus := task.Status
		task.Status = model.TaskStatusFailure
		task.Progress = "100%"
		task.FinishTime = now
		if isLegacy {
			task.FailReason = legacyReason
		} else {
			task.FailReason = reason
		}

		won, err := task.UpdateWithStatus(oldStatus)
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("sweepTimedOutTasks CAS update error for task %s: %v", task.TaskID, err))
			continue
		}
		if !won {
			logger.LogInfo(ctx, fmt.Sprintf("sweepTimedOutTasks: task %s already transitioned, skip", task.TaskID))
			continue
		}
		timedOutCount++
	}

	if timedOutCount > 0 {
		logger.LogInfo(ctx, fmt.Sprintf("sweepTimedOutTasks: timed out %d tasks", timedOutCount))
	}
}

// TaskPollingLoop 主轮询循环，每 15 秒检查一次未完成的任务
func TaskPollingLoop() {
	for {
		time.Sleep(time.Duration(15) * time.Second)
		common.SysLog("任务进度轮询开始")
		ctx := context.TODO()
		sweepTimedOutTasks(ctx)
		ProcessPendingTaskBillingOutboxes(ctx, 100)
		assetRuntime := CurrentCreativeAssetRuntime()
		assetRuntime.ProcessPendingLifecycleOutboxes(ctx, 100)
		assetRuntime.ProcessPendingDeletes(ctx, 100)
		allTasks := model.GetAllUnFinishSyncTasks(constant.TaskQueryLimit)
		platformTask := make(map[constant.TaskPlatform][]*model.Task)
		for _, t := range allTasks {
			platformTask[t.Platform] = append(platformTask[t.Platform], t)
		}
		for platform, tasks := range platformTask {
			if len(tasks) == 0 {
				continue
			}
			taskChannelM, taskM, nullTasks := collectPollingTaskBuckets(tasks)
			if len(nullTasks) > 0 {
				updated := markTasksFailedWithCASAndRefund(ctx, nullTasks, "upstream task id is empty")
				logger.LogInfo(ctx, fmt.Sprintf("Fix null task_id task success: %d/%d", updated, len(nullTasks)))
			}
			if len(taskChannelM) == 0 {
				continue
			}

			DispatchPlatformUpdate(platform, taskChannelM, taskM)
		}
		common.SysLog("任务进度轮询完成")
	}
}

func collectPollingTaskBuckets(tasks []*model.Task) (map[int][]string, map[string]*model.Task, []*model.Task) {
	taskChannelM := make(map[int][]string)
	taskM := make(map[string]*model.Task)
	nullTasks := make([]*model.Task, 0)
	for _, task := range tasks {
		upstreamID := pollingUpstreamTaskID(task)
		if upstreamID == "" {
			nullTasks = append(nullTasks, task)
			continue
		}
		taskM[upstreamID] = task
		taskChannelM[task.ChannelId] = append(taskChannelM[task.ChannelId], upstreamID)
	}
	return taskChannelM, taskM, nullTasks
}

func pollingUpstreamTaskID(task *model.Task) string {
	if task == nil {
		return ""
	}
	if creativeTaskRequiresStoredKey(task) {
		return strings.TrimSpace(task.PrivateData.UpstreamTaskID)
	}
	return strings.TrimSpace(task.GetUpstreamTaskID())
}

// DispatchPlatformUpdate 按平台分发轮询更新
func DispatchPlatformUpdate(platform constant.TaskPlatform, taskChannelM map[int][]string, taskM map[string]*model.Task) {
	switch platform {
	case constant.TaskPlatformMidjourney:
		if err := UpdateVideoTasks(context.Background(), platform, taskChannelM, taskM); err != nil {
			common.SysLog(fmt.Sprintf("UpdateMidjourneyTasks fail: %s", err))
		}
	case constant.TaskPlatformSuno:
		_ = UpdateSunoTasks(context.Background(), taskChannelM, taskM)
	default:
		if err := UpdateVideoTasks(context.Background(), platform, taskChannelM, taskM); err != nil {
			common.SysLog(fmt.Sprintf("UpdateVideoTasks fail: %s", err))
		}
	}
}

// UpdateSunoTasks 按渠道更新所有 Suno 任务
func UpdateSunoTasks(ctx context.Context, taskChannelM map[int][]string, taskM map[string]*model.Task) error {
	for channelId, taskIds := range taskChannelM {
		err := updateSunoTasks(ctx, channelId, taskIds, taskM)
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("渠道 #%d 更新异步任务失败: %s", channelId, err.Error()))
		}
	}
	return nil
}

func updateSunoTasks(ctx context.Context, channelId int, taskIds []string, taskM map[string]*model.Task) error {
	logger.LogInfo(ctx, fmt.Sprintf("渠道 #%d 未完成的任务有: %d", channelId, len(taskIds)))
	if len(taskIds) == 0 {
		return nil
	}
	ch, err := model.CacheGetChannel(channelId)
	if err != nil {
		common.SysLog(fmt.Sprintf("CacheGetChannel: %v", err))
		markSunoTasksFailed(ctx, taskIds, taskM, fmt.Sprintf("获取渠道信息失败，请联系管理员，渠道ID：%d", channelId))
		return err
	}
	adaptor := GetTaskAdaptorFunc(constant.TaskPlatformSuno)
	if adaptor == nil {
		return errors.New("adaptor not found")
	}
	proxy := ch.GetSetting().Proxy
	baseURL := ch.GetBaseURL()
	taskIDsByKey := make(map[string][]string)
	for _, upstreamID := range taskIds {
		task := taskM[upstreamID]
		if task == nil {
			logger.LogError(ctx, fmt.Sprintf("Suno task %s not found in taskM", upstreamID))
			continue
		}
		key, keyErr := selectedPollingKeyForTask(ctx, task, ch, "Suno")
		if keyErr != nil {
			reason := "missing submit-time selected key"
			logger.LogWarn(ctx, fmt.Sprintf("Suno task %s missing selected key, fail closed: %s", task.TaskID, keyErr.Error()))
			if _, err := failTaskWithCASAndRefund(ctx, task, reason); err != nil {
				logger.LogError(ctx, fmt.Sprintf("Suno task %s selected key failure CAS update failed: %s", task.TaskID, err.Error()))
			}
			continue
		}
		taskIDsByKey[key] = append(taskIDsByKey[key], upstreamID)
	}
	for key, keyedTaskIds := range taskIDsByKey {
		if err := fetchAndUpdateSunoTaskBatch(ctx, adaptor, baseURL, key, keyedTaskIds, proxy, taskM); err != nil {
			return err
		}
	}
	return nil
}

func selectedPollingKeyForTask(ctx context.Context, task *model.Task, ch *model.Channel, provider string) (string, error) {
	if task == nil {
		return "", errors.New("task is nil")
	}
	if key := strings.TrimSpace(task.PrivateData.Key); key != "" {
		return key, nil
	}
	if creativeTaskRequiresStoredKey(task) {
		return "", fmt.Errorf("%s creative task %s has no stored selected key", provider, task.TaskID)
	}
	if ch == nil || strings.TrimSpace(ch.Key) == "" {
		return "", fmt.Errorf("%s legacy task %s has no channel fallback key", provider, task.TaskID)
	}
	logger.LogWarn(ctx, fmt.Sprintf("%s legacy task %s missing stored selected key; using channel fallback key", provider, task.TaskID))
	return ch.Key, nil
}

func creativeTaskRequiresStoredKey(task *model.Task) bool {
	return task != nil && strings.TrimSpace(task.PrivateData.IdempotencyKey) != ""
}

func terminalBillingOutboxArgs(adaptor TaskPollingAdaptor, task *model.Task, taskResult *relaycommon.TaskInfo, shouldRefund bool, shouldSettle bool) (operation string, actualQuota int, reason string) {
	if task == nil {
		return "", 0, ""
	}
	if shouldRefund {
		return model.TaskBillingOutboxOperationTerminalRefund, 0, task.FailReason
	}
	if !shouldSettle {
		return "", 0, ""
	}
	if bc := task.PrivateData.BillingContext; bc != nil && bc.PerCallBilling {
		return "", 0, ""
	}
	if adaptor != nil {
		if quota := adaptor.AdjustBillingOnComplete(task, taskResult); quota > 0 {
			return model.TaskBillingOutboxOperationTerminalSettle, quota, "adaptor计费调整"
		}
	}
	if taskResult != nil && taskResult.TotalTokens > 0 {
		if quota, ok := CalculateTaskQuotaByTokens(task, taskResult.TotalTokens); ok && quota > 0 {
			return model.TaskBillingOutboxOperationTerminalSettle, quota, "token重算"
		}
	}
	return "", 0, ""
}

func fetchAndUpdateSunoTaskBatch(ctx context.Context, adaptor TaskPollingAdaptor, baseURL string, key string, taskIds []string, proxy string, taskM map[string]*model.Task) error {
	resp, err := adaptor.FetchTask(baseURL, key, map[string]any{
		"ids": taskIds,
	}, proxy)
	if err != nil {
		common.SysLog(fmt.Sprintf("Get Task Do req error: %v", err))
		return err
	}
	if resp == nil {
		return errors.New("Get Task returned nil response")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logger.LogError(ctx, fmt.Sprintf("Get Task status code: %d", resp.StatusCode))
		return fmt.Errorf("Get Task status code: %d", resp.StatusCode)
	}
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		common.SysLog(fmt.Sprintf("Get Suno Task parse body error: %v", err))
		return err
	}
	var responseItems dto.TaskResponse[[]dto.SunoDataResponse]
	err = common.Unmarshal(responseBody, &responseItems)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("Get Suno Task parse body error2: %v, body: %s", err, redactTaskResponseBodyForLog(responseBody)))
		return err
	}
	if !responseItems.IsSuccess() {
		common.SysLog(fmt.Sprintf("Suno batch fetch failed for %d tasks: %s", len(taskIds), redactTaskResponseBodyForLog(responseBody)))
		return fmt.Errorf("Suno batch fetch failed: %s", responseItems.Message)
	}

	for _, responseItem := range responseItems.Data {
		task := taskM[responseItem.TaskID]
		if task == nil {
			logger.LogError(ctx, fmt.Sprintf("Suno response task %s not found in taskM", responseItem.TaskID))
			continue
		}
		if _, err := updateSunoTaskFromResponse(ctx, task, responseItem, adaptor); err != nil {
			common.SysLog("UpdateSunoTask task error: " + err.Error())
		}
	}
	return nil
}

func markTasksFailedWithCASAndRefund(ctx context.Context, tasks []*model.Task, reason string) int {
	updated := 0
	for _, task := range tasks {
		if task == nil {
			continue
		}
		won, err := failTaskWithCASAndRefund(ctx, task, reason)
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("task %s failure CAS update failed: %s", task.TaskID, err.Error()))
			continue
		}
		if won {
			updated++
		}
	}
	return updated
}

func failTaskWithCASAndRefund(ctx context.Context, task *model.Task, reason string) (bool, error) {
	if task == nil {
		return false, errors.New("task is nil")
	}
	if task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailure {
		return false, nil
	}
	snap := task.Snapshot()
	task.Status = model.TaskStatusFailure
	task.Progress = "100%"
	if task.FinishTime == 0 {
		task.FinishTime = time.Now().Unix()
	}
	task.FailReason = reason
	var outbox *model.TaskBillingOutbox
	var err error
	if task.Quota != 0 {
		var won bool
		won, outbox, err = task.UpdateWithStatusAndBillingOutbox(snap.Status, model.TaskBillingOutboxOperationTerminalRefund, 0, task.Quota, reason)
		if err != nil || !won {
			return won, err
		}
		if err := ProcessTaskBillingOutbox(ctx, outbox); err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("task %s refund pending in outbox #%d: %s", task.TaskID, outbox.ID, err.Error()))
		}
		return true, nil
	}
	return task.UpdateWithStatus(snap.Status)
}

func markSunoTasksFailed(ctx context.Context, taskIds []string, taskM map[string]*model.Task, reason string) {
	for _, upstreamID := range taskIds {
		task := taskM[upstreamID]
		if task == nil {
			continue
		}
		won, err := failTaskWithCASAndRefund(ctx, task, reason)
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("Suno task %s failure CAS update failed: %s", task.TaskID, err.Error()))
			continue
		}
		if !won {
			logger.LogWarn(ctx, fmt.Sprintf("Suno task %s already transitioned by another process, skip failure refund", task.TaskID))
		}
	}
}

func updateSunoTaskFromResponse(ctx context.Context, task *model.Task, responseItem dto.SunoDataResponse, adaptor TaskPollingAdaptor) (bool, error) {
	if task == nil {
		return false, errors.New("task is nil")
	}
	if !taskNeedsUpdate(task, responseItem) {
		return false, nil
	}

	snap := task.Snapshot()
	if responseItem.Status != "" {
		task.Status = model.TaskStatus(responseItem.Status)
	}
	if responseItem.FailReason != "" {
		task.FailReason = responseItem.FailReason
	}
	if responseItem.SubmitTime != 0 {
		task.SubmitTime = responseItem.SubmitTime
	}
	if responseItem.StartTime != 0 {
		task.StartTime = responseItem.StartTime
	}
	if responseItem.FinishTime != 0 {
		task.FinishTime = responseItem.FinishTime
	}
	if len(responseItem.Data) > 0 {
		task.Data = responseItem.Data
	}

	shouldRefund := false
	shouldSettle := false
	switch task.Status {
	case model.TaskStatusFailure:
		logger.LogInfo(ctx, task.TaskID+" 构建失败，"+task.FailReason)
		task.Progress = "100%"
		if task.FinishTime == 0 {
			task.FinishTime = time.Now().Unix()
		}
		if task.Quota != 0 {
			shouldRefund = true
		}
	case model.TaskStatusSuccess:
		task.Progress = "100%"
		if task.FinishTime == 0 {
			task.FinishTime = time.Now().Unix()
		}
		shouldSettle = true
	}

	isDone := task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailure
	if isDone && snap.Status != task.Status {
		operation, actualQuota, reason := terminalBillingOutboxArgs(adaptor, task, &relaycommon.TaskInfo{
			TaskID:   task.TaskID,
			Status:   string(task.Status),
			Reason:   task.FailReason,
			Progress: task.Progress,
		}, shouldRefund, shouldSettle)
		var outbox *model.TaskBillingOutbox
		var won bool
		var err error
		if operation != "" {
			won, outbox, err = task.UpdateWithStatusAndBillingOutbox(snap.Status, operation, actualQuota, task.Quota, reason)
		} else {
			won, err = task.UpdateWithStatus(snap.Status)
		}
		if err != nil {
			return false, err
		}
		if !won {
			logger.LogWarn(ctx, fmt.Sprintf("Suno task %s already transitioned by another process, skip billing", task.TaskID))
			return false, nil
		}
		if outbox != nil {
			if err := ProcessTaskBillingOutbox(ctx, outbox); err != nil {
				logger.LogWarn(ctx, fmt.Sprintf("Suno task %s billing pending in outbox #%d: %s", task.TaskID, outbox.ID, err.Error()))
			}
		} else if shouldSettle && adaptor != nil {
			if err := settleTaskBillingOnComplete(ctx, adaptor, task, &relaycommon.TaskInfo{
				TaskID:   task.TaskID,
				Status:   string(task.Status),
				Reason:   task.FailReason,
				Progress: task.Progress,
			}); err != nil {
				logger.LogWarn(ctx, fmt.Sprintf("Suno task %s settle failed without outbox: %s", task.TaskID, err.Error()))
			}
		}
		return true, nil
	}

	if !snap.Equal(task.Snapshot()) {
		won, err := task.UpdateWithStatus(snap.Status)
		if err != nil {
			return false, err
		}
		return won, nil
	}
	return false, nil
}

// taskNeedsUpdate 检查 Suno 任务是否需要更新
func taskNeedsUpdate(oldTask *model.Task, newTask dto.SunoDataResponse) bool {
	if oldTask.SubmitTime != newTask.SubmitTime {
		return true
	}
	if oldTask.StartTime != newTask.StartTime {
		return true
	}
	if oldTask.FinishTime != newTask.FinishTime {
		return true
	}
	if string(oldTask.Status) != newTask.Status {
		return true
	}
	if oldTask.FailReason != newTask.FailReason {
		return true
	}

	if (oldTask.Status == model.TaskStatusFailure || oldTask.Status == model.TaskStatusSuccess) && oldTask.Progress != "100%" {
		return true
	}

	oldData, _ := common.Marshal(oldTask.Data)
	newData, _ := common.Marshal(newTask.Data)

	sort.Slice(oldData, func(i, j int) bool {
		return oldData[i] < oldData[j]
	})
	sort.Slice(newData, func(i, j int) bool {
		return newData[i] < newData[j]
	})

	if string(oldData) != string(newData) {
		return true
	}
	return false
}

// UpdateVideoTasks 按渠道更新所有视频任务
func UpdateVideoTasks(ctx context.Context, platform constant.TaskPlatform, taskChannelM map[int][]string, taskM map[string]*model.Task) error {
	for channelId, taskIds := range taskChannelM {
		if err := updateVideoTasks(ctx, platform, channelId, taskIds, taskM); err != nil {
			logger.LogError(ctx, fmt.Sprintf("Channel #%d failed to update video async tasks: %s", channelId, err.Error()))
		}
	}
	return nil
}

func updateVideoTasks(ctx context.Context, platform constant.TaskPlatform, channelId int, taskIds []string, taskM map[string]*model.Task) error {
	logger.LogInfo(ctx, fmt.Sprintf("Channel #%d pending video tasks: %d", channelId, len(taskIds)))
	if len(taskIds) == 0 {
		return nil
	}
	cacheGetChannel, err := model.CacheGetChannel(channelId)
	if err != nil {
		var failedTasks []*model.Task
		for _, upstreamID := range taskIds {
			if t, ok := taskM[upstreamID]; ok {
				failedTasks = append(failedTasks, t)
			}
		}
		markTasksFailedWithCASAndRefund(ctx, failedTasks, fmt.Sprintf("Failed to get channel info, channel ID: %d", channelId))
		return fmt.Errorf("CacheGetChannel failed: %w", err)
	}
	adaptor := GetTaskAdaptorFunc(platform)
	if adaptor == nil {
		return fmt.Errorf("video adaptor not found")
	}
	info := &relaycommon.RelayInfo{}
	info.ChannelMeta = &relaycommon.ChannelMeta{
		ChannelBaseUrl: cacheGetChannel.GetBaseURL(),
	}
	info.ApiKey = cacheGetChannel.Key
	adaptor.Init(info)
	for _, taskId := range taskIds {
		if err := updateVideoSingleTask(ctx, adaptor, cacheGetChannel, taskId, taskM); err != nil {
			logger.LogError(ctx, fmt.Sprintf("Failed to update video task %s: %s", taskId, err.Error()))
		}
		// sleep 1 second between each task to avoid hitting rate limits of upstream platforms
		time.Sleep(1 * time.Second)
	}
	return nil
}

func updateVideoSingleTask(ctx context.Context, adaptor TaskPollingAdaptor, ch *model.Channel, taskId string, taskM map[string]*model.Task) error {
	baseURL := constant.ChannelBaseURLs[ch.Type]
	if ch.GetBaseURL() != "" {
		baseURL = ch.GetBaseURL()
	}
	proxy := ch.GetSetting().Proxy

	task := taskM[taskId]
	if task == nil {
		logger.LogError(ctx, fmt.Sprintf("Task %s not found in taskM", taskId))
		return fmt.Errorf("task %s not found", taskId)
	}
	key, keyErr := selectedPollingKeyForTask(ctx, task, ch, "video")
	if keyErr != nil {
		reason := "missing submit-time selected key"
		if _, err := failTaskWithCASAndRefund(ctx, task, reason); err != nil {
			return fmt.Errorf("selected key fail-closed update failed for task %s: %w", taskId, err)
		}
		return keyErr
	}
	resp, err := adaptor.FetchTask(baseURL, key, map[string]any{
		"task_id": task.GetUpstreamTaskID(),
		"action":  task.Action,
	}, proxy)
	if err != nil {
		return fmt.Errorf("fetchTask failed for task %s: %w", taskId, err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("readAll failed for task %s: %w", taskId, err)
	}

	logger.LogDebug(ctx, "updateVideoSingleTask response: %s", redactTaskResponseBodyForLog(responseBody))

	snap := task.Snapshot()

	taskResult := &relaycommon.TaskInfo{}
	// try parse as New API response format
	var responseItems dto.TaskResponse[model.Task]
	if err = common.Unmarshal(responseBody, &responseItems); err == nil && responseItems.IsSuccess() {
		logger.LogDebug(ctx, "updateVideoSingleTask parsed as new api response format: %+v", responseItems)
		t := responseItems.Data
		taskResult.TaskID = t.TaskID
		taskResult.Status = string(t.Status)
		taskResult.Url = t.GetResultURL()
		taskResult.Progress = t.Progress
		taskResult.Reason = t.FailReason
		task.Data = t.Data
	} else if taskResult, err = adaptor.ParseTaskResult(responseBody); err != nil {
		return fmt.Errorf("parseTaskResult failed for task %s: %w", taskId, err)
	}

	task.Data = redactVideoResponseBody(responseBody)

	logger.LogDebug(ctx, "updateVideoSingleTask taskResult: %+v", taskResult)

	now := time.Now().Unix()
	if taskResult.Status == "" {
		//taskResult = relaycommon.FailTaskInfo("upstream returned empty status")
		errorResult := &dto.GeneralErrorResponse{}
		if err = common.Unmarshal(responseBody, &errorResult); err == nil {
			openaiError := errorResult.TryToOpenAIError()
			if openaiError != nil {
				// 返回规范的 OpenAI 错误格式，提取错误信息，判断错误是否为任务失败
				if openaiError.Code == "429" {
					// 429 错误通常表示请求过多或速率限制，暂时不认为是任务失败，保持原状态等待下一轮轮询
					return nil
				}

				// 其他错误认为是任务失败，记录错误信息并更新任务状态
				taskResult = relaycommon.FailTaskInfo("upstream returned error")
			} else {
				// unknown error format, log original response
				logger.LogError(ctx, fmt.Sprintf("Task %s returned empty status with unrecognized error format, response: %s", taskId, redactTaskResponseBodyForLog(responseBody)))
				taskResult = relaycommon.FailTaskInfo("upstream returned unrecognized message")
			}
		}
	}

	shouldRefund := false
	shouldSettle := false
	quota := task.Quota

	task.Status = model.TaskStatus(taskResult.Status)
	switch taskResult.Status {
	case model.TaskStatusSubmitted:
		task.Progress = taskcommon.ProgressSubmitted
	case model.TaskStatusQueued:
		task.Progress = taskcommon.ProgressQueued
	case model.TaskStatusInProgress:
		task.Progress = taskcommon.ProgressInProgress
		if task.StartTime == 0 {
			task.StartTime = now
		}
	case model.TaskStatusSuccess:
		task.Progress = taskcommon.ProgressComplete
		if task.FinishTime == 0 {
			task.FinishTime = now
		}
		if strings.HasPrefix(taskResult.Url, "data:") {
			if task.Platform == constant.TaskPlatformMidjourney {
				// Creative MJ only exposes the owner-scoped image proxy when an image URL exists.
				task.PrivateData.ResultURL = ""
			} else {
				// data: URI (e.g. Vertex base64 encoded video) — keep in Data, not in ResultURL
				task.PrivateData.ResultURL = taskcommon.BuildProxyURL(task.TaskID)
			}
		} else if taskResult.Url != "" {
			// Direct upstream URL (e.g. Kling, Ali, Doubao, etc.)
			task.PrivateData.ResultURL = taskResult.Url
		} else if task.Platform == constant.TaskPlatformMidjourney {
			// Do not synthesize a generic /v1/videos proxy for MJ image tasks.
			task.PrivateData.ResultURL = ""
		} else {
			// No URL from adaptor — construct proxy URL using public task ID
			task.PrivateData.ResultURL = taskcommon.BuildProxyURL(task.TaskID)
		}
		shouldSettle = true
	case model.TaskStatusFailure:
		logger.LogJson(ctx, fmt.Sprintf("Task %s failed", taskId), task)
		task.Status = model.TaskStatusFailure
		task.Progress = taskcommon.ProgressComplete
		if task.FinishTime == 0 {
			task.FinishTime = now
		}
		task.FailReason = taskResult.Reason
		logger.LogInfo(ctx, fmt.Sprintf("Task %s failed: %s", task.TaskID, task.FailReason))
		taskResult.Progress = taskcommon.ProgressComplete
		if quota != 0 {
			shouldRefund = true
		}
	default:
		return fmt.Errorf("unknown task status %s for task %s", taskResult.Status, task.TaskID)
	}
	if taskResult.Progress != "" {
		task.Progress = taskResult.Progress
	}

	isDone := task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailure
	if isDone && snap.Status != task.Status {
		operation, actualQuota, reason := terminalBillingOutboxArgs(adaptor, task, taskResult, shouldRefund, shouldSettle)
		var outbox *model.TaskBillingOutbox
		var won bool
		var err error
		if operation != "" {
			won, outbox, err = task.UpdateWithStatusAndBillingOutbox(snap.Status, operation, actualQuota, task.Quota, reason)
		} else {
			won, err = task.UpdateWithStatus(snap.Status)
		}
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("UpdateWithStatus failed for task %s: %s", task.TaskID, err.Error()))
			shouldRefund = false
			shouldSettle = false
		} else if !won {
			logger.LogWarn(ctx, fmt.Sprintf("Task %s already transitioned by another process, skip billing", task.TaskID))
			shouldRefund = false
			shouldSettle = false
		} else if outbox != nil {
			if err := ProcessTaskBillingOutbox(ctx, outbox); err != nil {
				logger.LogWarn(ctx, fmt.Sprintf("Task %s billing pending in outbox #%d: %s", task.TaskID, outbox.ID, err.Error()))
			}
			shouldRefund = false
			shouldSettle = false
		}
	} else if !snap.Equal(task.Snapshot()) {
		if _, err := task.UpdateWithStatus(snap.Status); err != nil {
			logger.LogError(ctx, fmt.Sprintf("Failed to update task %s: %s", task.TaskID, err.Error()))
		}
	} else {
		// No changes, skip update
		logger.LogDebug(ctx, "No update needed for task %s", task.TaskID)
	}

	if shouldSettle {
		if err := settleTaskBillingOnComplete(ctx, adaptor, task, taskResult); err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("Task %s settle failed without outbox: %s", task.TaskID, err.Error()))
		}
	}
	if shouldRefund {
		if err := RefundTaskQuota(ctx, task, task.FailReason); err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("Task %s refund failed without outbox: %s", task.TaskID, err.Error()))
		}
	}

	return nil
}

func redactVideoResponseBody(body []byte) []byte {
	var m map[string]any
	if err := common.Unmarshal(body, &m); err != nil {
		return body
	}
	resp, _ := m["response"].(map[string]any)
	if resp != nil {
		delete(resp, "bytesBase64Encoded")
		if v, ok := resp["video"].(string); ok {
			resp["video"] = truncateBase64(v)
		}
		if vs, ok := resp["videos"].([]any); ok {
			for i := range vs {
				if vm, ok := vs[i].(map[string]any); ok {
					delete(vm, "bytesBase64Encoded")
				}
			}
		}
	}
	b, err := common.Marshal(m)
	if err != nil {
		return body
	}
	return b
}

func redactTaskResponseBodyForLog(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var value any
	if err := common.Unmarshal(body, &value); err != nil {
		return fmt.Sprintf("[unparseable response body length=%d]", len(body))
	}
	value = redactTaskResponseLogValue(value, "")
	redacted, err := common.Marshal(value)
	if err != nil {
		return fmt.Sprintf("[redacted response body length=%d]", len(body))
	}
	return truncateTaskResponseLogString(string(redacted))
}

func redactTaskResponseLogValue(value any, key string) any {
	if taskResponseLogKeyIsSensitive(key) {
		return "[redacted]"
	}
	switch typed := value.(type) {
	case map[string]any:
		for childKey, childValue := range typed {
			typed[childKey] = redactTaskResponseLogValue(childValue, childKey)
		}
		return typed
	case []any:
		for i, child := range typed {
			typed[i] = redactTaskResponseLogValue(child, key)
		}
		return typed
	case string:
		trimmed := strings.TrimSpace(typed)
		if strings.HasPrefix(trimmed, "http://") ||
			strings.HasPrefix(trimmed, "https://") ||
			strings.HasPrefix(trimmed, "data:") ||
			len(trimmed) > 128 {
			return "[redacted]"
		}
		return typed
	default:
		return typed
	}
}

func taskResponseLogKeyIsSensitive(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.ReplaceAll(normalized, "_", "")
	normalized = strings.ReplaceAll(normalized, "-", "")
	switch {
	case normalized == "":
		return false
	case strings.Contains(normalized, "url"),
		strings.Contains(normalized, "uri"),
		strings.Contains(normalized, "base64"),
		strings.Contains(normalized, "apikey"),
		strings.Contains(normalized, "authorization"),
		strings.Contains(normalized, "token"),
		strings.Contains(normalized, "secret"),
		strings.Contains(normalized, "cookie"),
		strings.Contains(normalized, "signature"),
		strings.Contains(normalized, "signed"):
		return true
	default:
		return false
	}
}

func truncateTaskResponseLogString(value string) string {
	const maxResponseLogLength = 2048
	if len(value) <= maxResponseLogLength {
		return value
	}
	return value[:maxResponseLogLength] + "...[truncated]"
}

func truncateBase64(s string) string {
	const maxKeep = 256
	if len(s) <= maxKeep {
		return s
	}
	return s[:maxKeep] + "..."
}

// settleTaskBillingOnComplete 任务完成时的统一计费调整。
// 优先级：1. adaptor.AdjustBillingOnComplete 返回正数 → 使用 adaptor 计算的额度
//
//  2. taskResult.TotalTokens > 0 → 按 token 重算
//  3. 都不满足 → 保持预扣额度不变
func settleTaskBillingOnComplete(ctx context.Context, adaptor TaskPollingAdaptor, task *model.Task, taskResult *relaycommon.TaskInfo) error {
	// 0. 按次计费的任务不做差额结算
	if bc := task.PrivateData.BillingContext; bc != nil && bc.PerCallBilling {
		logger.LogInfo(ctx, fmt.Sprintf("任务 %s 按次计费，跳过差额结算", task.TaskID))
		return nil
	}
	// 1. 优先让 adaptor 决定最终额度
	if actualQuota := adaptor.AdjustBillingOnComplete(task, taskResult); actualQuota > 0 {
		return RecalculateTaskQuota(ctx, task, actualQuota, "adaptor计费调整")
	}
	// 2. 回退到 token 重算
	if taskResult.TotalTokens > 0 {
		return RecalculateTaskQuotaByTokens(ctx, task, taskResult.TotalTokens)
	}
	// 3. 无调整，保持预扣额度
	return nil
}
