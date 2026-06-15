package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

const (
	creativeImageTaskIdempotencyScope = "image.task.submit"
	creativeImageTaskActionGenerate   = "image.generate"
)

var creativeImageTaskInsert = func(task *model.Task) error {
	return task.Insert()
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
	ChannelId         int            `json:"-"`
	UserParams        map[string]any `json:"userParams,omitempty"`
}

type creativeImageTaskDTO struct {
	TaskID    string                    `json:"task_id"`
	Status    string                    `json:"status"`
	CreatedAt int64                     `json:"created_at,omitempty"`
	UpdatedAt int64                     `json:"updated_at,omitempty"`
	Progress  string                    `json:"progress,omitempty"`
	Model     string                    `json:"model"`
	Result    map[string]any            `json:"result,omitempty"`
	Metadata  creativeImageTaskMetadata `json:"metadata"`
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
	resolved, err := service.ResolveCreativeImageModelBindingForGroup(request.Model, c.GetString("group"), request.UserParams)
	if err != nil {
		creativeOpenAIError(c, http.StatusBadRequest, err.Error())
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
		ChannelId:         0,
		UserParams:        resolved.UserParams,
	}
	task := &model.Task{
		TaskID:     publicTaskID,
		Platform:   constant.TaskPlatformCreativeImage,
		UserId:     c.GetInt("id"),
		Group:      c.GetString("group"),
		ChannelId:  0,
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
		if err := model.CompleteCreativeVideoIdempotencyScoped(c.GetInt("id"), creativeImageTaskIdempotencyScope, requestID, publicTaskID); err != nil {
			creativeOpenAIError(c, http.StatusInternalServerError, "failed to complete creative image idempotency")
			return
		}
	}
	c.JSON(http.StatusAccepted, creativeImageTaskDTOFromTask(task))
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
	c.JSON(http.StatusOK, creativeImageTaskDTOFromTask(task))
}

func CreativeRelayImageTaskContent(c *gin.Context) {
	if c.GetBool("use_access_token") {
		creativeOpenAIError(c, http.StatusForbidden, "creative relay requires a browser session")
		return
	}
	_, ok, err := creativeGetOwnedImageTask(c, c.Param("task_id"))
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
	c.Data(http.StatusOK, "image/png", creativeMockPNG())
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
		Metadata:  metadata,
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
