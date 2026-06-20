package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	creativeImageTaskIdempotencyScope = "image.task.submit"
	creativeImageTaskActionGenerate   = "image.generate"
)

var (
	creativeImageTaskInsert = func(task *model.Task) error {
		return task.Insert()
	}
	creativeImageTaskCompleteIdempotency = model.CompleteCreativeVideoIdempotencyScoped
	creativeImageTaskFinalizeAccepted    = func(c *gin.Context, task *model.Task) error { return nil }
	creativeImageTaskInsertWithBilling   = creativeImageTaskInsertWithBillingDefault
)

type creativeImageTaskRequest struct {
	Model      string         `json:"model"`
	Prompt     string         `json:"prompt"`
	Images     []string       `json:"images,omitempty"`
	UserParams map[string]any `json:"userParams,omitempty"`
}

type creativeImageTaskMetadata struct {
	Version           int            `json:"version"`
	CreativeManaged   bool           `json:"creativeManaged"`
	BindingId         string         `json:"bindingId"`
	ProviderModelId   string         `json:"providerModelId"`
	PriceModelId      string         `json:"priceModelId"`
	AdapterPreset     string         `json:"adapterPreset"`
	ParameterTemplate string         `json:"parameterTemplate"`
	ChannelId         int            `json:"channelId"`
	UserParams        map[string]any `json:"userParams,omitempty"`
}

type creativeImageTaskPublicMetadata struct {
	Version           int            `json:"version"`
	CreativeManaged   bool           `json:"creativeManaged"`
	BindingId         string         `json:"bindingId"`
	ProviderModelId   string         `json:"providerModelId"`
	PriceModelId      string         `json:"priceModelId"`
	AdapterPreset     string         `json:"adapterPreset"`
	ParameterTemplate string         `json:"parameterTemplate"`
	UserParams        map[string]any `json:"userParams,omitempty"`
}

type creativeImageTaskDTO struct {
	TaskID    string                          `json:"task_id"`
	Status    string                          `json:"status"`
	CreatedAt int64                           `json:"created_at,omitempty"`
	UpdatedAt int64                           `json:"updated_at,omitempty"`
	Progress  string                          `json:"progress,omitempty"`
	Model     string                          `json:"model"`
	Result    map[string]any                  `json:"result,omitempty"`
	Metadata  creativeImageTaskPublicMetadata `json:"metadata"`
}

func CreativeImageTaskSubmitIdempotency() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost {
			c.Next()
			return
		}
		if !creativePrepareImageTaskIdempotency(c) {
			return
		}
		requestID := c.GetString(creativeTaskIdempotencyKeyContextKey)
		userID := c.GetInt("id")
		c.Next()
		if requestID != "" && c.Writer.Status() >= http.StatusBadRequest && !creativeTaskProviderAccepted(c) {
			if err := model.DeleteCreativeVideoIdempotencyScoped(userID, creativeImageTaskIdempotencyScope, requestID); err != nil {
				common.SysError("cleanup failed creative image idempotency error: " + err.Error())
			}
		}
	}
}

func CreativeImageTaskPreviewGate() gin.HandlerFunc {
	return func(c *gin.Context) {
		if service.CreativeMockImageTasksEnabled() {
			c.Next()
			return
		}
		if service.CreativeAdapterPreviewEnabled() {
			if c.Request.Method == http.MethodGet {
				c.Next()
				return
			}
			modelName, _ := creativeImageRequestModel(c)
			if binding, ok, err := service.GetCreativeModelBindingByID(modelName); err == nil && ok && service.CreativeImageLiveAdapterPreset(binding.AdapterPreset) {
				c.Next()
				return
			}
		}
		creativeOpenAIError(c, http.StatusNotFound, "creative image task preview is disabled")
		c.Abort()
	}
}

func CreativeRejectManagedImageBindingSyncRoute() gin.HandlerFunc {
	return func(c *gin.Context) {
		modelName, err := creativeImageRequestModel(c)
		if err != nil {
			creativeOpenAIError(c, http.StatusBadRequest, err.Error())
			c.Abort()
			return
		}
		binding, ok, err := service.GetCreativeModelBindingByID(modelName)
		if err != nil {
			creativeOpenAIError(c, http.StatusBadRequest, "invalid creative model binding config")
			c.Abort()
			return
		}
		if ok && binding.Modality == "image" {
			creativeOpenAIError(c, http.StatusBadRequest, "creative managed image bindings must use /creative/relay/v1/images/tasks")
			c.Abort()
			return
		}
		c.Next()
	}
}

func CreativeRelayImageTaskSubmit(c *gin.Context) {
	if c.GetBool("use_access_token") {
		creativeOpenAIError(c, http.StatusForbidden, "creative relay requires a browser session")
		return
	}
	var request creativeImageTaskRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		creativeOpenAIError(c, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(request.Prompt) == "" {
		creativeOpenAIError(c, http.StatusBadRequest, "prompt is required")
		return
	}
	if len(request.Images) > 0 {
		creativeOpenAIError(c, http.StatusBadRequest, "creative image reference images are not supported yet")
		return
	}
	resolved, err := service.ResolveCreativeImageModelBindingForGroup(request.Model, c.GetString("group"), request.UserParams)
	if err != nil {
		creativeOpenAIError(c, http.StatusBadRequest, err.Error())
		return
	}
	if service.CreativeImageLiveAdapterPreset(resolved.AdapterPreset) {
		creativeRelayImageTaskSubmitLive(c, request, resolved)
		return
	}
	publicTaskID := strings.TrimSpace(c.GetString(creativeTaskPublicTaskIDContextKey))
	if publicTaskID == "" {
		publicTaskID = model.GenerateTaskID()
	}
	creativeMarkTaskProviderAccepted(c)
	now := common.GetTimestamp()
	metadata := creativeImageTaskMetadata{
		Version:           1,
		CreativeManaged:   true,
		BindingId:         resolved.BindingId,
		ProviderModelId:   resolved.ProviderModelId,
		PriceModelId:      resolved.PriceModelId,
		AdapterPreset:     resolved.AdapterPreset,
		ParameterTemplate: resolved.ParameterTemplate,
		ChannelId:         resolved.ChannelId,
		UserParams:        resolved.UserParams,
	}
	task := &model.Task{
		TaskID:     publicTaskID,
		Platform:   constant.TaskPlatformCreativeImage,
		UserId:     c.GetInt("id"),
		Group:      c.GetString("group"),
		ChannelId:  resolved.ChannelId,
		Quota:      0,
		Action:     creativeImageTaskActionGenerate,
		Status:     model.TaskStatusSuccess,
		Progress:   "100%",
		SubmitTime: now,
		StartTime:  now,
		FinishTime: now,
		Properties: model.Properties{
			Input:             request.Prompt,
			OriginModelName:   resolved.BindingId,
			UpstreamModelName: resolved.ProviderModelId,
		},
		PrivateData: model.TaskPrivateData{
			UpstreamTaskID: "mock_" + publicTaskID,
			IdempotencyKey: c.GetString(creativeTaskIdempotencyKeyContextKey),
			ResultURL:      "mock://creative-image/" + publicTaskID + "?token=secret",
			BillingContext: &model.TaskBillingContext{
				OriginModelName:  resolved.PriceModelId,
				PerCallBilling:   true,
				PreConsumedQuota: 0,
			},
		},
	}
	task.SetData(metadata)
	if err := creativeImageTaskInsert(task); err != nil {
		creativeOpenAIError(c, http.StatusInternalServerError, "failed to persist creative image task")
		return
	}
	if requestID := c.GetString(creativeTaskIdempotencyKeyContextKey); requestID != "" {
		if err := creativeImageTaskCompleteIdempotency(c.GetInt("id"), creativeImageTaskIdempotencyScope, requestID, publicTaskID); err != nil {
			creativeOpenAIError(c, http.StatusInternalServerError, "failed to complete creative image idempotency")
			return
		}
	}
	if err := creativeImageTaskFinalizeAccepted(c, task); err != nil {
		creativeOpenAIError(c, http.StatusInternalServerError, "failed to finalize creative image task")
		return
	}
	c.JSON(http.StatusAccepted, creativeImageTaskDTOFromTask(task))
}

func creativeRelayImageTaskSubmitLive(c *gin.Context, request creativeImageTaskRequest, resolved service.CreativeResolvedModelBinding) {
	channel, err := model.GetChannelById(resolved.ChannelId, true)
	if err != nil || channel == nil || channel.Status != common.ChannelStatusEnabled {
		creativeOpenAIError(c, http.StatusBadRequest, "creative image channel is not available")
		return
	}
	selectedCredential, _, keyErr := channel.GetNextEnabledKey()
	if keyErr != nil || strings.TrimSpace(selectedCredential) == "" {
		creativeOpenAIError(c, http.StatusBadRequest, "creative image channel has no available key")
		return
	}
	providerEndpoint := strings.TrimSpace(channel.GetBaseURL())
	if providerEndpoint == "" {
		creativeOpenAIError(c, http.StatusBadRequest, "creative image channel has no provider endpoint")
		return
	}
	relayInfo := creativeImageRelayInfo(c, resolved, channel)
	priceData, err := helper.ModelPriceHelperPerCall(c, relayInfo)
	if err != nil {
		creativeOpenAIError(c, http.StatusBadRequest, err.Error())
		return
	}
	relayInfo.PriceData = priceData
	if !priceData.FreeModel {
		relayInfo.ForcePreConsume = true
		if apiErr := service.PreConsumeBilling(c, priceData.Quota, relayInfo); apiErr != nil {
			creativeOpenAIError(c, apiErr.StatusCode, apiErr.Error())
			return
		}
	}
	providerResult, err := service.SubmitCreativeImageProviderTask(c.Request.Context(), service.CreativeImageProviderRequest{
		AdapterPreset:   resolved.AdapterPreset,
		Endpoint:        providerEndpoint,
		Credential:      selectedCredential,
		ProviderModelID: resolved.ProviderModelId,
		Prompt:          request.Prompt,
		Images:          request.Images,
		UserParams:      resolved.UserParams,
	})
	if err != nil {
		creativeRefundImageSubmitPreConsume(c, relayInfo)
		creativeOpenAIError(c, http.StatusBadGateway, err.Error())
		return
	}
	if strings.TrimSpace(providerResult.UpstreamTaskID) == "" {
		creativeRefundImageSubmitPreConsume(c, relayInfo)
		creativeOpenAIError(c, http.StatusBadGateway, "creative image provider did not accept the task")
		return
	}
	creativeMarkTaskProviderAccepted(c)
	publicTaskID := strings.TrimSpace(c.GetString(creativeTaskPublicTaskIDContextKey))
	if publicTaskID == "" {
		publicTaskID = model.GenerateTaskID()
	}
	now := common.GetTimestamp()
	status := providerResult.Status
	if status == "" || status == model.TaskStatusUnknown {
		status = model.TaskStatusInProgress
	}
	metadata := creativeImageTaskMetadata{
		Version:           1,
		CreativeManaged:   true,
		BindingId:         resolved.BindingId,
		ProviderModelId:   resolved.ProviderModelId,
		PriceModelId:      resolved.PriceModelId,
		AdapterPreset:     resolved.AdapterPreset,
		ParameterTemplate: resolved.ParameterTemplate,
		ChannelId:         resolved.ChannelId,
		UserParams:        resolved.UserParams,
	}
	task := &model.Task{
		TaskID:     publicTaskID,
		Platform:   constant.TaskPlatformCreativeImage,
		UserId:     c.GetInt("id"),
		Group:      c.GetString("group"),
		ChannelId:  resolved.ChannelId,
		Quota:      priceData.Quota,
		Action:     creativeImageTaskActionGenerate,
		Status:     status,
		Progress:   providerResult.Progress,
		SubmitTime: now,
		StartTime:  now,
		Properties: model.Properties{
			Input:             request.Prompt,
			OriginModelName:   resolved.PriceModelId,
			UpstreamModelName: resolved.ProviderModelId,
		},
		PrivateData: model.TaskPrivateData{
			Key:              selectedCredential,
			UpstreamTaskID:   providerResult.UpstreamTaskID,
			ProviderEndpoint: providerEndpoint,
			IdempotencyKey:   c.GetString(creativeTaskIdempotencyKeyContextKey),
			ResultURL:        providerResult.ResultURL,
			BillingSource:    relayInfo.BillingSource,
			SubscriptionId:   relayInfo.SubscriptionId,
			TokenId:          relayInfo.TokenId,
			BillingContext: &model.TaskBillingContext{
				ModelPrice:       priceData.ModelPrice,
				GroupRatio:       priceData.GroupRatioInfo.GroupRatio,
				ModelRatio:       priceData.ModelRatio,
				OtherRatios:      priceData.OtherRatios,
				OriginModelName:  resolved.PriceModelId,
				PerCallBilling:   true,
				PreConsumedQuota: service.TaskSubmitPreConsumedQuota(relayInfo),
			},
		},
	}
	if status == model.TaskStatusSuccess || status == model.TaskStatusFailure {
		task.FinishTime = now
	}
	if providerResult.FailReason != "" {
		task.FailReason = providerResult.FailReason
	}
	if task.Progress == "" {
		task.Progress = "0%"
	}
	task.SetData(metadata)
	outboxOperation := model.TaskBillingOutboxOperationSubmitSettle
	outboxActualQuota := task.Quota
	outboxPreConsumedQuota := service.TaskSubmitPreConsumedQuota(relayInfo)
	outboxReason := "submit settle"
	if status == model.TaskStatusFailure {
		outboxOperation = model.TaskBillingOutboxOperationTerminalRefund
		outboxActualQuota = 0
		outboxReason = "submit terminal failure"
	}
	outbox, err := creativeImageTaskInsertWithBilling(task, outboxOperation, outboxActualQuota, outboxPreConsumedQuota, outboxReason)
	if err != nil {
		creativeRefundImageSubmitPreConsume(c, relayInfo)
		creativeOpenAIError(c, http.StatusInternalServerError, "failed to persist creative image task")
		return
	}
	if requestID := c.GetString(creativeTaskIdempotencyKeyContextKey); requestID != "" {
		if err := creativeImageTaskCompleteIdempotency(c.GetInt("id"), creativeImageTaskIdempotencyScope, requestID, publicTaskID); err != nil {
			creativeOpenAIError(c, http.StatusInternalServerError, "failed to complete creative image idempotency")
			return
		}
	}
	if outbox != nil {
		if err := service.ProcessTaskBillingOutbox(c.Request.Context(), outbox); err != nil {
			common.SysError("creative image submit billing outbox pending: " + err.Error())
		}
	}
	if err := creativeImageTaskFinalizeAccepted(c, task); err != nil {
		creativeOpenAIError(c, http.StatusInternalServerError, "failed to finalize creative image task")
		return
	}
	c.JSON(http.StatusAccepted, creativeImageTaskDTOFromTask(task))
}

func creativeImageTaskInsertWithBillingDefault(task *model.Task, operation string, actualQuota int, preConsumedQuota int, reason string) (*model.TaskBillingOutbox, error) {
	var outbox *model.TaskBillingOutbox
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(task).Error; err != nil {
			return err
		}
		var err error
		outbox, err = model.EnqueueTaskBillingOutboxTx(tx, task, operation, actualQuota, preConsumedQuota, reason)
		return err
	})
	return outbox, err
}

func creativeRefundImageSubmitPreConsume(c *gin.Context, relayInfo *relaycommon.RelayInfo) {
	if relayInfo == nil {
		return
	}
	if relayInfo.Billing != nil {
		relayInfo.Billing.Refund(c)
		return
	}
	service.ReturnPreConsumedQuota(c, relayInfo)
}

func creativeImageRelayInfo(c *gin.Context, resolved service.CreativeResolvedModelBinding, channel *model.Channel) *relaycommon.RelayInfo {
	group := c.GetString("group")
	_ = channel
	info := &relaycommon.RelayInfo{
		UserId:          c.GetInt("id"),
		UserGroup:       group,
		UsingGroup:      group,
		TokenId:         c.GetInt("token_id"),
		RequestId:       c.GetString(creativeTaskIdempotencyKeyContextKey),
		StartTime:       time.Now(),
		OriginModelName: resolved.PriceModelId,
		RequestURLPath:  c.Request.URL.Path,
		IsPlayground:    true,
		UserQuota:       common.GetContextKeyInt(c, constant.ContextKeyUserQuota),
	}
	if userSetting, ok := common.GetContextKeyType[dto.UserSetting](c, constant.ContextKeyUserSetting); ok {
		info.UserSetting = userSetting
	} else if userSetting, err := model.GetUserSetting(info.UserId, true); err == nil {
		info.UserSetting = userSetting
	}
	return info
}

func creativeImageRequestModel(c *gin.Context) (string, error) {
	if c == nil || c.Request == nil || c.Request.Method == http.MethodGet {
		return "", nil
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return "", err
	}
	body, err := storage.Bytes()
	if err != nil {
		return "", err
	}
	if _, seekErr := storage.Seek(0, io.SeekStart); seekErr == nil {
		c.Request.Body = io.NopCloser(storage)
	}
	var payload map[string]any
	if err := common.Unmarshal(body, &payload); err != nil {
		return "", nil
	}
	modelName, _ := payload["model"].(string)
	return strings.TrimSpace(modelName), nil
}

func CreativeRelayImageTaskFetch(c *gin.Context) {
	if c.GetBool("use_access_token") {
		creativeOpenAIError(c, http.StatusForbidden, "creative relay requires a browser session")
		return
	}
	task, ok, err := creativeGetOwnedImageTask(c, c.Param("task_id"))
	if err != nil {
		creativeOpenAIError(c, http.StatusInternalServerError, "failed to fetch creative image task")
		return
	}
	if !ok {
		creativeOpenAIError(c, http.StatusNotFound, "creative image task was not found")
		return
	}
	if err := creativeReconcileLiveImageTask(c, task); err != nil {
		common.SysError("creative image task reconcile failed: " + err.Error())
	}
	c.JSON(http.StatusOK, creativeImageTaskDTOFromTask(task))
}

func CreativeRelayImageTaskContent(c *gin.Context) {
	if c.GetBool("use_access_token") {
		creativeOpenAIError(c, http.StatusForbidden, "creative relay requires a browser session")
		return
	}
	task, ok, err := creativeGetOwnedImageTask(c, c.Param("task_id"))
	if err != nil {
		creativeOpenAIError(c, http.StatusInternalServerError, "failed to fetch creative image task")
		return
	}
	if !ok {
		creativeOpenAIError(c, http.StatusNotFound, "creative image task was not found")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("Pragma", "no-cache")
	c.Header("X-Content-Type-Options", "nosniff")
	if task.Status != model.TaskStatusSuccess {
		creativeOpenAIError(c, http.StatusConflict, "creative image task is not completed")
		return
	}
	var metadata creativeImageTaskMetadata
	_ = task.GetData(&metadata)
	if service.CreativeImageLiveAdapterPreset(metadata.AdapterPreset) {
		content, err := service.FetchCreativeImageProviderContent(c.Request.Context(), task.PrivateData.ResultURL)
		if err != nil {
			creativeOpenAIError(c, http.StatusBadGateway, "failed to fetch creative image result")
			return
		}
		c.Data(http.StatusOK, content.ContentType, content.Body)
		return
	}
	c.Data(http.StatusOK, "image/png", creativeMockPNG())
}

func creativeReconcileLiveImageTask(c *gin.Context, task *model.Task) error {
	if task == nil || task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailure {
		return nil
	}
	var metadata creativeImageTaskMetadata
	if err := task.GetData(&metadata); err != nil || !metadata.CreativeManaged || !service.CreativeImageLiveAdapterPreset(metadata.AdapterPreset) {
		return nil
	}
	if strings.TrimSpace(task.PrivateData.UpstreamTaskID) == "" || strings.TrimSpace(task.PrivateData.Key) == "" {
		return creativeFailClosedImageTask(c, task, "creative image task is missing provider affinity")
	}
	providerEndpoint := strings.TrimSpace(task.PrivateData.ProviderEndpoint)
	if providerEndpoint == "" {
		return creativeFailClosedImageTask(c, task, "creative image task is missing provider endpoint affinity")
	}
	channel, err := model.GetChannelById(metadata.ChannelId, false)
	if err != nil || channel == nil {
		return creativeFailClosedImageTask(c, task, "creative image channel is no longer available")
	}
	if strings.TrimSpace(channel.GetBaseURL()) != providerEndpoint {
		return creativeFailClosedImageTask(c, task, "creative image provider endpoint affinity changed")
	}
	result, err := service.PollCreativeImageProviderTask(c.Request.Context(), service.CreativeImageProviderRequest{
		AdapterPreset:   metadata.AdapterPreset,
		Endpoint:        providerEndpoint,
		Credential:      task.PrivateData.Key,
		ProviderModelID: metadata.ProviderModelId,
		UserParams:      metadata.UserParams,
	}, task.PrivateData.UpstreamTaskID)
	if err != nil {
		if service.CreativeImageProviderTerminalError(err) {
			return creativeFailClosedImageTask(c, task, "creative image provider terminal result is invalid")
		}
		return err
	}
	task.Progress = result.Progress
	if task.Progress == "" {
		task.Progress = "0%"
	}
	if result.Status != model.TaskStatusSuccess && result.Status != model.TaskStatusFailure {
		fromStatus := task.Status
		task.Status = result.Status
		task.UpdatedAt = common.GetTimestamp()
		update := model.DB.Model(&model.Task{}).
			Where("id = ? AND status = ?", task.ID, fromStatus).
			Updates(map[string]any{
				"status":     task.Status,
				"progress":   task.Progress,
				"updated_at": task.UpdatedAt,
			})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected == 0 {
			return creativeReloadImageTask(task)
		}
		return nil
	}
	fromStatus := task.Status
	task.Status = result.Status
	task.FinishTime = common.GetTimestamp()
	task.FailReason = result.FailReason
	task.PrivateData.ResultURL = result.ResultURL
	if task.Status == model.TaskStatusSuccess {
		task.Progress = "100%"
		won, outbox, err := task.UpdateWithStatusAndBillingOutbox(fromStatus, model.TaskBillingOutboxOperationTerminalSettle, task.Quota, task.Quota, "creative image terminal success")
		if err != nil {
			return err
		}
		if won && outbox != nil {
			_ = service.ProcessTaskBillingOutbox(c.Request.Context(), outbox)
		}
		if !won {
			return creativeReloadImageTask(task)
		}
		return nil
	}
	won, outbox, err := task.UpdateWithStatusAndBillingOutbox(fromStatus, model.TaskBillingOutboxOperationTerminalRefund, 0, task.Quota, "creative image terminal failure")
	if err != nil {
		return err
	}
	if won && outbox != nil {
		_ = service.ProcessTaskBillingOutbox(c.Request.Context(), outbox)
	}
	if !won {
		return creativeReloadImageTask(task)
	}
	return nil
}

func creativeFailClosedImageTask(c *gin.Context, task *model.Task, reason string) error {
	if task == nil || task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailure {
		return nil
	}
	fromStatus := task.Status
	task.Status = model.TaskStatusFailure
	task.FailReason = reason
	task.FinishTime = common.GetTimestamp()
	won, outbox, err := task.UpdateWithStatusAndBillingOutbox(fromStatus, model.TaskBillingOutboxOperationTerminalRefund, 0, task.Quota, reason)
	if err != nil {
		return err
	}
	if won && outbox != nil {
		_ = service.ProcessTaskBillingOutbox(c.Request.Context(), outbox)
	}
	if !won {
		return creativeReloadImageTask(task)
	}
	return nil
}

func creativeReloadImageTask(task *model.Task) error {
	if task == nil || task.ID == 0 {
		return nil
	}
	var fresh model.Task
	if err := model.DB.Where("id = ?", task.ID).First(&fresh).Error; err != nil {
		return err
	}
	*task = fresh
	return nil
}

func creativePrepareImageTaskIdempotency(c *gin.Context) bool {
	requestID := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if requestID == "" {
		requestID = strings.TrimSpace(c.GetHeader("X-Creative-Request-Id"))
	}
	if requestID == "" {
		creativeOpenAIError(c, http.StatusBadRequest, "creative image task submit requires Idempotency-Key")
		c.Abort()
		return false
	}
	if len(requestID) > 128 {
		creativeOpenAIError(c, http.StatusBadRequest, "Idempotency-Key is too long")
		c.Abort()
		return false
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		creativeOpenAIError(c, http.StatusBadRequest, err.Error())
		c.Abort()
		return false
	}
	body, err := storage.Bytes()
	if err != nil {
		creativeOpenAIError(c, http.StatusBadRequest, err.Error())
		c.Abort()
		return false
	}
	if _, seekErr := storage.Seek(0, io.SeekStart); seekErr == nil {
		c.Request.Body = io.NopCloser(storage)
	}
	sum := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(sum[:])
	record, existed, err := model.PrepareCreativeVideoIdempotencyScoped(c.GetInt("id"), creativeImageTaskIdempotencyScope, requestID, payloadHash)
	if err != nil {
		creativeOpenAIError(c, http.StatusInternalServerError, "failed to prepare idempotency record")
		c.Abort()
		return false
	}
	if existed {
		if record.PayloadHash != payloadHash {
			creativeOpenAIError(c, http.StatusConflict, "Idempotency-Key conflicts with a different payload")
			c.Abort()
			return false
		}
		if strings.TrimSpace(record.TaskID) == "" {
			creativeOpenAIError(c, http.StatusConflict, "creative image task request is still being prepared")
			c.Abort()
			return false
		}
		if task, ok, taskErr := model.GetByTaskId(c.GetInt("id"), record.TaskID); taskErr == nil && ok && task != nil {
			c.JSON(http.StatusOK, creativeImageTaskDTOFromTask(task))
			c.Abort()
			return false
		}
		creativeOpenAIError(c, http.StatusConflict, "creative image task request is still being prepared")
		c.Abort()
		return false
	}
	c.Set(creativeTaskPublicTaskIDContextKey, record.TaskID)
	c.Set(creativeTaskIdempotencyKeyContextKey, requestID)
	c.Set(creativeTaskIdempotencyScopeContextKey, creativeImageTaskIdempotencyScope)
	return true
}

func creativeGetOwnedImageTask(c *gin.Context, taskID string) (*model.Task, bool, error) {
	task, ok, err := model.GetByTaskId(c.GetInt("id"), strings.TrimSpace(taskID))
	if err != nil || !ok || task == nil {
		return nil, ok, err
	}
	if task.Platform != constant.TaskPlatformCreativeImage {
		return nil, false, nil
	}
	var metadata creativeImageTaskMetadata
	if err := task.GetData(&metadata); err != nil || !metadata.CreativeManaged {
		return nil, false, nil
	}
	return task, true, nil
}

func creativeImageTaskDTOFromTask(task *model.Task) creativeImageTaskDTO {
	var metadata creativeImageTaskMetadata
	_ = task.GetData(&metadata)
	result := map[string]any{}
	if task.Status == model.TaskStatusSuccess {
		result["url"] = creativeImageTaskContentURL(task.TaskID)
		result["mimeType"] = "image/png"
	}
	return creativeImageTaskDTO{
		TaskID:    task.TaskID,
		Status:    task.Status.ToVideoStatus(),
		CreatedAt: task.CreatedAt,
		UpdatedAt: task.UpdatedAt,
		Progress:  task.Progress,
		Model:     metadata.BindingId,
		Result:    result,
		Metadata:  creativeImageTaskPublicMetadataFromMetadata(metadata),
	}
}

func creativeImageTaskPublicMetadataFromMetadata(metadata creativeImageTaskMetadata) creativeImageTaskPublicMetadata {
	return creativeImageTaskPublicMetadata{
		Version:           metadata.Version,
		CreativeManaged:   metadata.CreativeManaged,
		BindingId:         metadata.BindingId,
		ProviderModelId:   metadata.ProviderModelId,
		PriceModelId:      metadata.PriceModelId,
		AdapterPreset:     metadata.AdapterPreset,
		ParameterTemplate: metadata.ParameterTemplate,
		UserParams:        metadata.UserParams,
	}
}

func creativeImageTaskContentURL(taskID string) string {
	return creativeBrokerBaseURL + "/images/tasks/" + strings.TrimSpace(taskID) + "/content"
}

func creativeMockPNG() []byte {
	// 1x1 transparent PNG.
	data, _ := hex.DecodeString("89504e470d0a1a0a0000000d49484452000000010000000108060000001f15c4890000000a49444154789c6360000002000100ffff03000006000557bfab9d0000000049454e44ae426082")
	return data
}
