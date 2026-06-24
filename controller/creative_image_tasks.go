package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
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
)

const (
	creativeImageTaskIdempotencyScope              = model.CreativeImageTaskIdempotencyScopeSubmit
	creativeImageTaskActionGenerate                = "image.generate"
	creativeImageProviderSubmitDefaultTimeout      = 120 * time.Second
	creativeImageProviderSubmitUnrecoverableReason = "creative image provider submit is unrecoverable: no provider task id was persisted after the submit recovery window; pre-consume refund queued"
)

var (
	creativeImageTaskInsert = func(task *model.Task) error {
		return task.Insert()
	}
	creativeImageTaskCompleteIdempotency = model.CompleteCreativeVideoIdempotencyScoped
	creativeImageTaskFinalizeAccepted    = func(c *gin.Context, task *model.Task) error { return nil }
	creativeImageTaskUpdateWithBilling   = creativeImageTaskUpdateWithBillingDefault
)

func creativeImageProviderSubmitContext(requestCtx context.Context) (context.Context, context.CancelFunc) {
	baseCtx := context.Background()
	if requestCtx != nil {
		baseCtx = context.WithoutCancel(requestCtx)
	}
	timeout := creativeImageProviderSubmitDefaultTimeout
	if common.RelayTimeout > 0 {
		timeout = time.Duration(common.RelayTimeout) * time.Second
	}
	return context.WithTimeout(baseCtx, timeout)
}

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
	TargetWidth       int            `json:"targetWidth,omitempty"`
	TargetHeight      int            `json:"targetHeight,omitempty"`
	TargetAspectRatio string         `json:"targetAspectRatio,omitempty"`
	TargetResolution  string         `json:"targetResolution,omitempty"`
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
	TargetWidth       int            `json:"targetWidth,omitempty"`
	TargetHeight      int            `json:"targetHeight,omitempty"`
	TargetAspectRatio string         `json:"targetAspectRatio,omitempty"`
	TargetResolution  string         `json:"targetResolution,omitempty"`
}

type creativeImageTaskDTO struct {
	TaskID     string                          `json:"task_id"`
	Status     string                          `json:"status"`
	CreatedAt  int64                           `json:"created_at,omitempty"`
	UpdatedAt  int64                           `json:"updated_at,omitempty"`
	Progress   string                          `json:"progress,omitempty"`
	Model      string                          `json:"model"`
	Result     map[string]any                  `json:"result,omitempty"`
	FailReason string                          `json:"fail_reason,omitempty"`
	Error      map[string]any                  `json:"error,omitempty"`
	Metadata   creativeImageTaskPublicMetadata `json:"metadata"`
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
		if requestID != "" && c.Writer.Status() >= http.StatusBadRequest && !creativeTaskProviderAccepted(c) && !creativeTaskDurableCreated(c) {
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
		if c.Request != nil && c.Request.Method == http.MethodGet {
			c.Next()
			return
		}
		modelName, _ := creativeImageRequestModel(c)
		if binding, ok, err := service.GetCreativeModelBindingByID(modelName); err == nil && ok && service.CreativeImageLiveAdapterPreset(binding.AdapterPreset) {
			c.Next()
			return
		}
		if service.CreativeAdapterPreviewEnabled() {
			c.Next()
			return
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
		shouldReject, err := service.ShouldRejectCreativeManagedImageBindingSyncRoute(modelName, c.GetString("group"))
		if err != nil {
			creativeOpenAIError(c, http.StatusBadRequest, "invalid creative model binding config")
			c.Abort()
			return
		}
		if shouldReject {
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
	targetMetadata := service.ResolveCreativeImageTargetMetadata(resolved.AdapterPreset, resolved.ProviderModelId, resolved.UserParams)
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
		TargetWidth:       targetMetadata.Width,
		TargetHeight:      targetMetadata.Height,
		TargetAspectRatio: targetMetadata.AspectRatio,
		TargetResolution:  targetMetadata.Resolution,
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
	publicTaskID := strings.TrimSpace(c.GetString(creativeTaskPublicTaskIDContextKey))
	if publicTaskID == "" {
		publicTaskID = model.GenerateTaskID()
	}
	targetMetadata := service.ResolveCreativeImageTargetMetadata(resolved.AdapterPreset, resolved.ProviderModelId, resolved.UserParams)
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
		TargetWidth:       targetMetadata.Width,
		TargetHeight:      targetMetadata.Height,
		TargetAspectRatio: targetMetadata.AspectRatio,
		TargetResolution:  targetMetadata.Resolution,
	}
	task := &model.Task{
		TaskID:     publicTaskID,
		Platform:   constant.TaskPlatformCreativeImage,
		UserId:     c.GetInt("id"),
		Group:      c.GetString("group"),
		ChannelId:  resolved.ChannelId,
		Quota:      priceData.Quota,
		Action:     creativeImageTaskActionGenerate,
		Status:     model.TaskStatusSubmitted,
		Progress:   "0%",
		SubmitTime: now,
		StartTime:  now,
		Properties: model.Properties{
			Input:             request.Prompt,
			OriginModelName:   resolved.PriceModelId,
			UpstreamModelName: resolved.ProviderModelId,
		},
		PrivateData: model.TaskPrivateData{
			Key:                      selectedCredential,
			ProviderEndpoint:         providerEndpoint,
			IdempotencyKey:           c.GetString(creativeTaskIdempotencyKeyContextKey),
			ProviderSubmitInFlight:   true,
			ProviderSubmitInFlightAt: now,
			BillingSource:            relayInfo.BillingSource,
			SubscriptionId:           relayInfo.SubscriptionId,
			TokenId:                  relayInfo.TokenId,
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
	task.SetData(metadata)
	if err := creativeImageTaskInsert(task); err != nil {
		creativeRefundImageSubmitPreConsume(c, relayInfo)
		creativeOpenAIError(c, http.StatusInternalServerError, "failed to persist creative image task")
		return
	}
	creativeMarkTaskDurableCreated(c)

	providerSubmitCtx, cancelProviderSubmit := creativeImageProviderSubmitContext(c.Request.Context())
	defer cancelProviderSubmit()
	providerResult, err := service.SubmitCreativeImageProviderTask(providerSubmitCtx, service.CreativeImageProviderRequest{
		AdapterPreset:   resolved.AdapterPreset,
		Endpoint:        providerEndpoint,
		Credential:      selectedCredential,
		ProviderModelID: resolved.ProviderModelId,
		Prompt:          request.Prompt,
		Images:          request.Images,
		UserParams:      resolved.UserParams,
	})
	if err != nil {
		if service.CreativeImageProviderAmbiguousSubmitError(err) {
			if persistErr := creativeMarkImageTaskSubmitAmbiguous(task, "creative image provider submit status is ambiguous"); persistErr != nil {
				common.SysError("creative image ambiguous submit persist failed: " + persistErr.Error())
				creativeOpenAIError(c, http.StatusInternalServerError, "failed to persist creative image task")
				return
			}
			if requestID := c.GetString(creativeTaskIdempotencyKeyContextKey); requestID != "" {
				if completeErr := creativeImageTaskCompleteIdempotency(c.GetInt("id"), creativeImageTaskIdempotencyScope, requestID, publicTaskID); completeErr != nil {
					creativeOpenAIError(c, http.StatusInternalServerError, "failed to complete creative image idempotency")
					return
				}
			}
			c.JSON(http.StatusAccepted, creativeImageTaskDTOFromTask(task))
			return
		}
		reason := creativeImageTaskSafeFailReason(err.Error())
		if reason == "" {
			reason = "creative image provider submit failed"
		}
		if failErr := creativeFailClosedImageTask(c, task, reason); failErr != nil {
			common.SysError("creative image submit failure refund failed: " + failErr.Error())
		}
		creativeOpenAIError(c, http.StatusBadGateway, reason)
		return
	}
	if strings.TrimSpace(providerResult.UpstreamTaskID) == "" {
		reason := "creative image provider did not accept the task"
		if failErr := creativeFailClosedImageTask(c, task, reason); failErr != nil {
			common.SysError("creative image submit rejection refund failed: " + failErr.Error())
		}
		creativeOpenAIError(c, http.StatusBadGateway, reason)
		return
	}
	creativeMarkTaskProviderAccepted(c)

	status := providerResult.Status
	if status == "" || status == model.TaskStatusUnknown {
		status = model.TaskStatusInProgress
	}
	resultURL := strings.TrimSpace(providerResult.ResultURL)
	if status == model.TaskStatusSuccess {
		materializedURL, materializeErr := service.MaterializeCreativeImageProviderResult(c.Request.Context(), task.UserId, task.TaskID, metadata.BindingId, metadata.ProviderModelId, resultURL)
		if materializeErr != nil {
			common.SysError("creative image submit success materialize pending: " + materializeErr.Error())
			status = model.TaskStatusInProgress
			resultURL = ""
			if providerResult.Progress == "" || providerResult.Progress == "100%" {
				providerResult.Progress = "99%"
			}
		} else {
			resultURL = materializedURL
		}
	}
	fromStatus := task.Status
	task.Status = status
	task.Progress = providerResult.Progress
	task.PrivateData.UpstreamTaskID = providerResult.UpstreamTaskID
	task.PrivateData.ProviderSubmitInFlight = false
	task.PrivateData.ProviderSubmitInFlightAt = 0
	task.PrivateData.ResultURL = resultURL
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
	won, outbox, err := creativeImageTaskUpdateWithBilling(task, fromStatus, outboxOperation, outboxActualQuota, outboxPreConsumedQuota, outboxReason)
	if err != nil {
		if freshTask, ok, loadErr := model.GetByTaskId(c.GetInt("id"), publicTaskID); loadErr == nil && ok && freshTask != nil {
			if failErr := creativeFailClosedImageTask(c, freshTask, "failed to persist creative image provider acceptance"); failErr != nil {
				common.SysError("creative image accepted failure refund failed: " + failErr.Error())
			}
		}
		creativeOpenAIError(c, http.StatusInternalServerError, "failed to persist creative image task")
		return
	}
	if !won {
		if err := creativeReloadImageTask(task); err != nil {
			creativeOpenAIError(c, http.StatusInternalServerError, "failed to reload creative image task")
			return
		}
		c.JSON(http.StatusAccepted, creativeImageTaskDTOFromTask(task))
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

func creativeImageTaskUpdateWithBillingDefault(task *model.Task, fromStatus model.TaskStatus, operation string, actualQuota int, preConsumedQuota int, reason string) (bool, *model.TaskBillingOutbox, error) {
	return task.UpdateWithStatusAndBillingOutbox(fromStatus, operation, actualQuota, preConsumedQuota, reason)
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
		if _, err := creativeReadyImageTaskAssetRuntime(); err != nil {
			creativeAssetError(c, err)
			return
		}
		if assetID, ok := creativeImageTaskAssetContentURLAssetID(task.PrivateData.ResultURL); ok {
			creativeServeImageTaskAssetContent(c, assetID)
			return
		}
		content, err := service.FetchCreativeImageProviderContent(c.Request.Context(), task.PrivateData.ResultURL)
		if err != nil {
			creativeOpenAIError(c, http.StatusBadGateway, "failed to fetch creative image result")
			return
		}
		assetURL, err := creativeMaterializeImageTaskContent(c, task, metadata, content)
		if err != nil {
			common.SysError("creative image content materialize failed: " + err.Error())
			creativeAssetError(c, err)
			return
		}
		if strings.TrimSpace(c.GetHeader("Range")) != "" {
			if assetID, ok := creativeImageTaskAssetContentURLAssetID(assetURL); ok {
				creativeServeImageTaskAssetContent(c, assetID)
				return
			}
		}
		c.Data(http.StatusOK, content.ContentType, content.Body)
		return
	}
	c.Data(http.StatusOK, "image/png", creativeMockPNG())
}

func creativeImageTaskAssetContentURLAssetID(value string) (string, bool) {
	return service.CreativeAssetContentURLAssetID(value)
}

func creativeServeImageTaskAssetContent(c *gin.Context, assetID string) {
	runtime, err := creativeReadyImageTaskAssetRuntime()
	if err != nil {
		creativeAssetError(c, err)
		return
	}
	content, err := runtime.OpenContent(c.Request.Context(), c.GetInt("id"), assetID, c.GetHeader("Range"))
	if err != nil {
		if errors.Is(err, service.ErrCreativeAssetRangeNotSatisfiable) {
			creativeSetPrivateContentHeaders(c)
			c.Header("Content-Range", "bytes */*")
			c.Status(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		creativeAssetError(c, err)
		return
	}
	defer content.Body.Close()
	creativeSetPrivateContentHeaders(c)
	c.Header("Content-Type", content.MimeType)
	c.Header("Accept-Ranges", "bytes")
	length := content.RangeEnd - content.RangeStart + 1
	if length < 0 {
		length = 0
	}
	c.Header("Content-Length", strconv.FormatInt(length, 10))
	if content.ContentRange != "" {
		c.Header("Content-Range", content.ContentRange)
	}
	c.Status(content.StatusCode)
	_, _ = io.Copy(c.Writer, content.Body)
}

func creativeReadyImageTaskAssetRuntime() (*service.CreativeAssetRuntime, error) {
	runtime := service.CurrentCreativeAssetRuntime()
	if ok, reason := runtime.Status(); !ok {
		if strings.TrimSpace(reason) == "" {
			return nil, service.ErrCreativeAssetDisabled
		}
		return nil, fmt.Errorf("%w: %s", service.ErrCreativeAssetDisabled, reason)
	}
	return runtime, nil
}

func creativeMaterializeImageTaskContent(c *gin.Context, task *model.Task, metadata creativeImageTaskMetadata, content service.CreativeImageProviderContent) (string, error) {
	if task == nil || len(content.Body) == 0 {
		return "", fmt.Errorf("%w: creative image result content is empty", service.ErrCreativeAssetInvalid)
	}
	assetURL, err := service.MaterializeCreativeImageProviderContent(c.Request.Context(), task.UserId, task.TaskID, metadata.BindingId, metadata.ProviderModelId, content)
	if err != nil {
		return "", err
	}
	if err := creativePersistImageTaskResultURL(task, task.PrivateData.ResultURL, assetURL); err != nil {
		return "", err
	}
	return assetURL, nil
}

func creativePersistImageTaskResultURL(task *model.Task, oldURL string, newURL string) error {
	if task == nil || task.ID == 0 {
		return nil
	}
	oldURL = strings.TrimSpace(oldURL)
	newURL = strings.TrimSpace(newURL)
	if newURL == "" || newURL == oldURL {
		return nil
	}
	var fresh model.Task
	if err := model.DB.Where("id = ? AND user_id = ?", task.ID, task.UserId).First(&fresh).Error; err != nil {
		return err
	}
	if fresh.Status != model.TaskStatusSuccess {
		*task = fresh
		return nil
	}
	if currentAssetID, ok := creativeImageTaskAssetContentURLAssetID(fresh.PrivateData.ResultURL); ok {
		task.PrivateData.ResultURL = "/creative/api/assets/" + currentAssetID + "/content"
		return nil
	}
	if strings.TrimSpace(fresh.PrivateData.ResultURL) != oldURL {
		*task = fresh
		return nil
	}
	fresh.PrivateData.ResultURL = newURL
	fresh.UpdatedAt = common.GetTimestamp()
	result := model.DB.Model(&model.Task{}).
		Where("id = ? AND user_id = ? AND status = ?", fresh.ID, fresh.UserId, model.TaskStatusSuccess).
		Updates(map[string]any{
			"private_data": fresh.PrivateData,
			"updated_at":   fresh.UpdatedAt,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		task.PrivateData = fresh.PrivateData
		task.UpdatedAt = fresh.UpdatedAt
	}
	return nil
}

func creativeReconcileLiveImageTask(c *gin.Context, task *model.Task) error {
	if task == nil {
		return nil
	}
	if task.Status == model.TaskStatusSuccess {
		return creativeRepairSuccessfulLiveImageTaskResultURL(c, task)
	}
	if task.Status == model.TaskStatusFailure {
		return nil
	}
	var metadata creativeImageTaskMetadata
	if err := task.GetData(&metadata); err != nil || !metadata.CreativeManaged || !service.CreativeImageLiveAdapterPreset(metadata.AdapterPreset) {
		return nil
	}
	if strings.TrimSpace(task.PrivateData.Key) == "" {
		return creativeFailClosedImageTask(c, task, "creative image task is missing provider affinity")
	}
	if strings.TrimSpace(task.PrivateData.UpstreamTaskID) == "" {
		if task.PrivateData.ProviderSubmitInFlight {
			if service.CreativeImageSubmitInFlightExpired(task, common.GetTimestamp()) {
				return creativeFailClosedImageTask(c, task, creativeImageProviderSubmitUnrecoverableReason)
			}
			return nil
		}
		if task.PrivateData.ProviderSubmitAmbiguous {
			if service.CreativeImageAmbiguousSubmitExpired(task, common.GetTimestamp()) {
				return creativeFailClosedImageTask(c, task, creativeImageProviderSubmitUnrecoverableReason)
			}
			return nil
		}
		return creativeFailClosedImageTask(c, task, "creative image task is missing provider affinity")
	}
	providerEndpoint := strings.TrimSpace(task.PrivateData.ProviderEndpoint)
	if providerEndpoint == "" {
		return creativeFailClosedImageTask(c, task, "creative image task is missing provider endpoint affinity")
	}
	channel, err := model.GetChannelById(metadata.ChannelId, false)
	if err != nil || channel == nil {
		if err != nil && !model.IsChannelNotFoundError(err) {
			return err
		}
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
	resultURL := strings.TrimSpace(result.ResultURL)
	if result.Status == model.TaskStatusSuccess {
		assetURL, materializeErr := service.MaterializeCreativeImageProviderResult(c.Request.Context(), task.UserId, task.TaskID, metadata.BindingId, metadata.ProviderModelId, resultURL)
		if materializeErr != nil {
			if task.Progress == "100%" {
				task.Progress = "99%"
			}
			common.SysError("creative image task materialize failed: " + materializeErr.Error())
			task.Status = model.TaskStatusInProgress
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
		resultURL = assetURL
	}
	task.Status = result.Status
	task.FinishTime = common.GetTimestamp()
	task.FailReason = result.FailReason
	task.PrivateData.ResultURL = resultURL
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

func creativeRepairSuccessfulLiveImageTaskResultURL(c *gin.Context, task *model.Task) error {
	var metadata creativeImageTaskMetadata
	if err := task.GetData(&metadata); err != nil || !metadata.CreativeManaged || !service.CreativeImageLiveAdapterPreset(metadata.AdapterPreset) {
		return nil
	}
	resultURL := strings.TrimSpace(task.PrivateData.ResultURL)
	if resultURL == "" {
		return nil
	}
	if _, ok := creativeImageTaskAssetContentURLAssetID(resultURL); ok {
		return nil
	}
	assetURL, err := service.MaterializeCreativeImageProviderResult(c.Request.Context(), task.UserId, task.TaskID, metadata.BindingId, metadata.ProviderModelId, resultURL)
	if err != nil {
		return err
	}
	return creativePersistImageTaskResultURL(task, resultURL, assetURL)
}

func creativeMarkImageTaskSubmitAmbiguous(task *model.Task, reason string) error {
	if task == nil || task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailure {
		return nil
	}
	fromStatus := task.Status
	now := common.GetTimestamp()
	task.Status = model.TaskStatusInProgress
	if task.Progress == "" || task.Progress == "0" {
		task.Progress = "0%"
	}
	task.FailReason = reason
	task.UpdatedAt = now
	task.PrivateData.ProviderSubmitInFlight = false
	task.PrivateData.ProviderSubmitInFlightAt = 0
	task.PrivateData.ProviderSubmitAmbiguous = true
	task.PrivateData.ProviderSubmitAmbiguousAt = now
	update := model.DB.Model(&model.Task{}).
		Where("id = ? AND status = ?", task.ID, fromStatus).
		Updates(map[string]any{
			"status":       task.Status,
			"progress":     task.Progress,
			"fail_reason":  task.FailReason,
			"updated_at":   task.UpdatedAt,
			"private_data": task.PrivateData,
		})
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected == 0 {
		return creativeReloadImageTask(task)
	}
	return nil
}

func creativeFailClosedImageTask(c *gin.Context, task *model.Task, reason string) error {
	if task == nil || task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailure {
		return nil
	}
	fromStatus := task.Status
	submitWasUnresolved := strings.TrimSpace(task.PrivateData.UpstreamTaskID) == "" &&
		(task.PrivateData.ProviderSubmitInFlight || task.PrivateData.ProviderSubmitAmbiguous)
	task.Status = model.TaskStatusFailure
	task.FailReason = reason
	task.FinishTime = common.GetTimestamp()
	task.PrivateData.ProviderSubmitInFlight = false
	task.PrivateData.ProviderSubmitInFlightAt = 0
	if submitWasUnresolved {
		task.PrivateData.ProviderSubmitUnrecoverable = true
		task.PrivateData.ProviderSubmitUnrecoverableAt = task.FinishTime
	}
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
		contentURL := creativeImageTaskContentURL(task.TaskID)
		result["url"] = contentURL
		result["contentUrl"] = contentURL
		if metadata.TargetWidth > 0 {
			result["targetWidth"] = metadata.TargetWidth
		}
		if metadata.TargetHeight > 0 {
			result["targetHeight"] = metadata.TargetHeight
		}
		if metadata.TargetAspectRatio != "" {
			result["targetAspectRatio"] = metadata.TargetAspectRatio
		}
		if metadata.TargetResolution != "" {
			result["targetResolution"] = metadata.TargetResolution
		}
	}
	failReason := ""
	var publicError map[string]any
	if task.Status == model.TaskStatusFailure {
		failReason = creativeImageTaskSafeFailReason(task.FailReason)
		if failReason != "" {
			publicError = map[string]any{
				"message": failReason,
				"type":    "creative_image_task_failed",
			}
		}
	}
	return creativeImageTaskDTO{
		TaskID:     task.TaskID,
		Status:     task.Status.ToVideoStatus(),
		CreatedAt:  task.CreatedAt,
		UpdatedAt:  task.UpdatedAt,
		Progress:   task.Progress,
		Model:      metadata.BindingId,
		Result:     result,
		FailReason: failReason,
		Error:      publicError,
		Metadata:   creativeImageTaskPublicMetadataFromMetadata(metadata),
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
		TargetWidth:       metadata.TargetWidth,
		TargetHeight:      metadata.TargetHeight,
		TargetAspectRatio: metadata.TargetAspectRatio,
		TargetResolution:  metadata.TargetResolution,
	}
}

func creativeImageTaskSafeFailReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ""
	}
	if creativeImageTaskFailReasonSensitive(reason) {
		return "creative image task failed"
	}
	if len(reason) > 240 {
		reason = reason[:240]
	}
	return reason
}

func creativeImageTaskFailReasonSensitive(reason string) bool {
	lower := strings.ToLower(strings.TrimSpace(reason))
	if lower == "" {
		return false
	}
	if strings.Contains(lower, "://") || strings.HasPrefix(lower, "data:") {
		return true
	}
	for _, marker := range []string{
		"bearer ",
		"sk-",
		"x-amz-",
		"x-oss-",
		"signature=",
		"credential=",
		"expires=",
		"cookie=",
		"set-cookie:",
		"csrf=",
		"nonce=",
		"api_key=",
		"apikey=",
		"access_key=",
		"accesskey=",
		"object_key=",
		"objectkey=",
		"secret=",
		"token=",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func creativeImageTaskContentURL(taskID string) string {
	return creativeBrokerBaseURL + "/images/tasks/" + strings.TrimSpace(taskID) + "/content"
}

func creativeMockPNG() []byte {
	// 1x1 transparent PNG.
	data, _ := hex.DecodeString("89504e470d0a1a0a0000000d49484452000000010000000108060000001f15c4890000000a49444154789c6360000002000100ffff03000006000557bfab9d0000000049454e44ae426082")
	return data
}
