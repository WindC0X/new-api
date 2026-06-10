package mj

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

const ChannelName = "midjourney"

var ModelList = []string{"mj_imagine"}

type TaskAdaptor struct {
	taskcommon.BaseBilling
	ChannelType int
}

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	if info != nil {
		a.ChannelType = info.ChannelType
	}
}

func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) *dto.TaskError {
	var req dto.MidjourneyRequest
	if err := common.UnmarshalBodyReusable(c, &req); err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return service.TaskErrorWrapperLocal(fmt.Errorf("prompt_is_required"), "invalid_request", http.StatusBadRequest)
	}
	req.Action = constant.MjActionImagine
	req.NotifyHook = ""
	info.Action = constant.MjActionImagine
	c.Set("task_request", &req)
	return nil
}

func (a *TaskAdaptor) BuildRequestURL(info *relaycommon.RelayInfo) (string, error) {
	baseURL := strings.TrimRight(info.ChannelBaseUrl, "/")
	if baseURL == "" {
		return "", fmt.Errorf("channel base url is empty")
	}
	return baseURL + "/mj/submit/imagine", nil
}

func (a *TaskAdaptor) BuildRequestHeader(c *gin.Context, req *http.Request, info *relaycommon.RelayInfo) error {
	contentType := c.Request.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	req.Header.Set("Content-Type", contentType)
	if accept := c.Request.Header.Get("Accept"); accept != "" {
		req.Header.Set("Accept", accept)
	}
	setMJAPISecretHeader(req.Header, info.ApiKey)
	return nil
}

func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	value, ok := c.Get("task_request")
	if !ok {
		return nil, fmt.Errorf("task_request not found in context")
	}
	req, ok := value.(*dto.MidjourneyRequest)
	if !ok || req == nil {
		return nil, fmt.Errorf("task_request has invalid type")
	}
	body, err := common.Marshal(req)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(body), nil
}

func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	return channel.DoTaskApiRequest(a, c, info, requestBody)
}

func (a *TaskAdaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (taskID string, taskData []byte, taskErr *dto.TaskError) {
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		taskErr = service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
		return
	}
	_ = resp.Body.Close()

	var upstream dto.MidjourneyResponse
	if err := common.Unmarshal(responseBody, &upstream); err != nil {
		taskErr = service.TaskErrorWrapper(fmt.Errorf("%w: %s", err, string(responseBody)), "unmarshal_response_body_failed", http.StatusInternalServerError)
		return
	}
	if upstream.Result == "" {
		taskErr = service.TaskErrorWrapperLocal(fmt.Errorf("upstream task id is empty"), "invalid_response", http.StatusInternalServerError)
		return
	}
	if upstream.Code != 1 && upstream.Code != 21 && upstream.Code != 22 {
		description := strings.TrimSpace(upstream.Description)
		if description == "" {
			description = "midjourney submit failed"
		}
		taskErr = service.TaskErrorWrapperLocal(fmt.Errorf("%s", description), fmt.Sprintf("%d", upstream.Code), http.StatusBadRequest)
		return
	}

	status := string(model.TaskStatusSubmitted)
	progress := "0%"
	imageURL := ""
	if props, ok := upstream.Properties.(map[string]any); ok {
		if value, ok := props["status"].(string); ok && strings.TrimSpace(value) != "" {
			status = strings.TrimSpace(value)
		}
		if value, ok := props["imageUrl"].(string); ok {
			imageURL = strings.TrimSpace(value)
		}
		if status == string(model.TaskStatusSuccess) {
			progress = "100%"
		}
	}

	requestPrompt := ""
	if value, ok := c.Get("task_request"); ok {
		if req, ok := value.(*dto.MidjourneyRequest); ok && req != nil {
			requestPrompt = req.Prompt
		}
	}

	public := dto.MidjourneyResponse{
		Code:        1,
		Description: upstream.Description,
		Result:      info.PublicTaskID,
	}
	if public.Description == "" {
		public.Description = "success"
	}
	c.JSON(http.StatusOK, public)

	localTask := dto.MidjourneyDto{
		MjId:        info.PublicTaskID,
		Action:      constant.MjActionImagine,
		Prompt:      requestPrompt,
		Description: upstream.Description,
		ImageUrl:    imageURL,
		Status:      status,
		Progress:    progress,
	}
	taskData, err = common.Marshal(localTask)
	if err != nil {
		taskErr = service.TaskErrorWrapper(err, "marshal_task_data_failed", http.StatusInternalServerError)
		return
	}
	return upstream.Result, taskData, nil
}

func (a *TaskAdaptor) GetModelList() []string {
	return ModelList
}

func (a *TaskAdaptor) GetChannelName() string {
	return ChannelName
}

func (a *TaskAdaptor) FetchTask(baseUrl, key string, body map[string]any, proxy string) (*http.Response, error) {
	taskID, ok := body["task_id"].(string)
	if !ok || strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("invalid task_id")
	}
	requestURL := fmt.Sprintf("%s/mj/task/%s/fetch", strings.TrimRight(baseUrl, "/"), taskID)
	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	setMJAPISecretHeader(req.Header, key)
	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, fmt.Errorf("new proxy http client failed: %w", err)
	}
	return client.Do(req)
}

func (a *TaskAdaptor) ParseTaskResult(respBody []byte) (*relaycommon.TaskInfo, error) {
	var task dto.MidjourneyDto
	if err := common.Unmarshal(respBody, &task); err != nil {
		return nil, fmt.Errorf("unmarshal midjourney task result failed: %w", err)
	}
	status := normalizeStatus(task.Status)
	result := &relaycommon.TaskInfo{
		TaskID:   task.MjId,
		Status:   string(status),
		Url:      strings.TrimSpace(task.ImageUrl),
		Reason:   strings.TrimSpace(task.FailReason),
		Progress: strings.TrimSpace(task.Progress),
	}
	if result.Status == model.TaskStatusFailure && result.Reason == "" {
		result.Reason = strings.TrimSpace(task.Description)
	}
	return result, nil
}

func setMJAPISecretHeader(header http.Header, key string) {
	secret := strings.TrimSpace(key)
	secret = strings.TrimPrefix(secret, "Bearer ")
	secret = strings.TrimPrefix(secret, "bearer ")
	header.Del("Authorization")
	if secret != "" {
		header.Set("mj-api-secret", secret)
	}
}

func normalizeStatus(status string) model.TaskStatus {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "", "NOT_START", "SUBMITTED":
		return model.TaskStatusSubmitted
	case "QUEUED", "PENDING":
		return model.TaskStatusQueued
	case "IN_PROGRESS", "PROCESSING", "RUNNING":
		return model.TaskStatusInProgress
	case "SUCCESS", "FINISHED", "COMPLETED":
		return model.TaskStatusSuccess
	case "FAILURE", "FAILED", "CANCELED", "CANCELLED":
		return model.TaskStatusFailure
	default:
		return model.TaskStatusUnknown
	}
}
