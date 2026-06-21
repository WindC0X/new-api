package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
)

const (
	CreativeImageAdapterPresetMock       = "mock_image_task"
	CreativeImageAdapterPresetDuomiLive  = "duomi_image_live"
	CreativeImageAdapterPresetGrsAILive  = "grsai_image_live"
	creativeImageResultContentMaxBytes   = 32 << 20
	creativeImageProviderErrorMaxLength  = 240
	creativeImageProviderDefaultProgress = "0%"
)

var errCreativeImageProviderTerminalMalformed = errors.New("creative image provider terminal response is malformed")

type CreativeImageProviderRequest struct {
	AdapterPreset   string
	Endpoint        string
	Credential      string
	ProviderModelID string
	Prompt          string
	Images          []string
	UserParams      map[string]any
}

type CreativeImageProviderResult struct {
	UpstreamTaskID string
	Status         model.TaskStatus
	Progress       string
	ResultURL      string
	FailReason     string
}

type CreativeImageProviderContent struct {
	ContentType string
	Body        []byte
}

type creativeProviderImageURL struct {
	URL string `json:"url"`
}

type creativeImageProviderAdapter interface {
	submit(ctx context.Context, req CreativeImageProviderRequest) (CreativeImageProviderResult, error)
	poll(ctx context.Context, req CreativeImageProviderRequest, upstreamTaskID string) (CreativeImageProviderResult, error)
}

func CreativeImageLiveAdapterPreset(preset string) bool {
	switch strings.TrimSpace(preset) {
	case CreativeImageAdapterPresetDuomiLive, CreativeImageAdapterPresetGrsAILive:
		return true
	default:
		return false
	}
}

func SubmitCreativeImageProviderTask(ctx context.Context, req CreativeImageProviderRequest) (CreativeImageProviderResult, error) {
	adapter, err := creativeImageAdapterForPreset(req.AdapterPreset)
	if err != nil {
		return CreativeImageProviderResult{}, err
	}
	return adapter.submit(ctx, req)
}

func PollCreativeImageProviderTask(ctx context.Context, req CreativeImageProviderRequest, upstreamTaskID string) (CreativeImageProviderResult, error) {
	adapter, err := creativeImageAdapterForPreset(req.AdapterPreset)
	if err != nil {
		return CreativeImageProviderResult{}, err
	}
	return adapter.poll(ctx, req, upstreamTaskID)
}

func CreativeImageProviderTerminalError(err error) bool {
	return errors.Is(err, errCreativeImageProviderTerminalMalformed)
}

func FetchCreativeImageProviderContent(ctx context.Context, rawURL string) (CreativeImageProviderContent, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return CreativeImageProviderContent{}, errors.New("creative image result is not ready")
	}
	if strings.HasPrefix(rawURL, "mock://") {
		return CreativeImageProviderContent{}, errors.New("mock result is not a provider URL")
	}
	fetchSetting := system_setting.GetFetchSetting()
	if err := common.ValidateURLWithFetchSetting(rawURL, fetchSetting.EnableSSRFProtection, fetchSetting.AllowPrivateIp, fetchSetting.DomainFilterMode, fetchSetting.IpFilterMode, fetchSetting.DomainList, fetchSetting.IpList, fetchSetting.AllowedPorts, fetchSetting.ApplyIPFilterForDomain); err != nil {
		return CreativeImageProviderContent{}, fmt.Errorf("creative image result URL blocked")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return CreativeImageProviderContent{}, errors.New("invalid creative image result URL")
	}
	client := ensureHTTPClient()
	response, err := client.Do(request)
	if err != nil {
		return CreativeImageProviderContent{}, fmt.Errorf("failed to fetch creative image result")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return CreativeImageProviderContent{}, fmt.Errorf("creative image result fetch failed")
	}
	contentType := strings.TrimSpace(response.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if !creativeImageResultContentTypeAllowed(contentType) {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return CreativeImageProviderContent{}, errors.New("creative image result content type is not allowed")
	}
	limited := io.LimitReader(response.Body, creativeImageResultContentMaxBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return CreativeImageProviderContent{}, errors.New("failed to read creative image result")
	}
	if len(body) > creativeImageResultContentMaxBytes {
		return CreativeImageProviderContent{}, errors.New("creative image result is too large")
	}
	return CreativeImageProviderContent{ContentType: contentType, Body: body}, nil
}

func creativeImageResultContentTypeAllowed(contentType string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch mediaType {
	case "image/png", "image/jpeg", "image/jpg", "image/webp", "image/gif", "image/avif":
		return true
	default:
		return false
	}
}

func creativeImageAdapterForPreset(preset string) (creativeImageProviderAdapter, error) {
	switch strings.TrimSpace(preset) {
	case CreativeImageAdapterPresetDuomiLive:
		return duomiCreativeImageAdapter{}, nil
	case CreativeImageAdapterPresetGrsAILive:
		return grsAICreativeImageAdapter{}, nil
	default:
		return nil, fmt.Errorf("creative image adapter preset is not supported")
	}
}

type duomiCreativeImageAdapter struct{}

func (duomiCreativeImageAdapter) submit(ctx context.Context, req CreativeImageProviderRequest) (CreativeImageProviderResult, error) {
	body := map[string]any{
		"model":  req.ProviderModelID,
		"prompt": req.Prompt,
	}
	if value := creativeDuomiSizeParam(req.UserParams); value != "" {
		body["size"] = value
	}
	if value := creativeStringParam(req.UserParams, "quality"); value != "" && value != "auto" {
		body["quality"] = value
	}
	if len(req.Images) > 0 {
		body["image"] = req.Images
	}
	raw, err := creativeImageProviderJSON(ctx, http.MethodPost, creativeJoinURL(req.Endpoint, "/v1/images/generations?async=true"), req.Credential, body)
	if err != nil {
		return CreativeImageProviderResult{}, err
	}
	return parseDuomiCreativeImageResult(raw, true)
}

func (duomiCreativeImageAdapter) poll(ctx context.Context, req CreativeImageProviderRequest, upstreamTaskID string) (CreativeImageProviderResult, error) {
	upstreamTaskID = strings.TrimSpace(upstreamTaskID)
	if upstreamTaskID == "" {
		return CreativeImageProviderResult{}, errors.New("creative image upstream task id is required")
	}
	raw, err := creativeImageProviderJSON(ctx, http.MethodGet, creativeJoinURL(req.Endpoint, "/v1/tasks/"+url.PathEscape(upstreamTaskID)), req.Credential, nil)
	if err != nil {
		return CreativeImageProviderResult{}, err
	}
	return parseDuomiCreativeImageResult(raw, false)
}

type grsAICreativeImageAdapter struct{}

func creativeDuomiSizeParam(params map[string]any) string {
	size := creativeStringParam(params, "size")
	switch size {
	case "21:9":
		// Duomi documents custom widthxheight sizes but does not list raw 21:9
		// as a size enum. Keep the UI aspect option while sending a documented
		// custom size that is divisible by 16 and within the provider pixel budget.
		return "1792x768"
	default:
		return size
	}
}

func (grsAICreativeImageAdapter) submit(ctx context.Context, req CreativeImageProviderRequest) (CreativeImageProviderResult, error) {
	body := map[string]any{
		"model":     req.ProviderModelID,
		"prompt":    req.Prompt,
		"replyType": "async",
	}
	if value := creativeGrsAIAspectRatioParam(req); value != "" {
		body["aspectRatio"] = value
	}
	if value := creativeGrsAIImageSizeParam(req); value != "" {
		body["imageSize"] = value
	}
	if len(req.Images) > 0 {
		body["images"] = req.Images
	}
	raw, err := creativeImageProviderJSON(ctx, http.MethodPost, creativeJoinURL(req.Endpoint, "/v1/api/generate"), "Bearer "+strings.TrimSpace(req.Credential), body)
	if err != nil {
		return CreativeImageProviderResult{}, err
	}
	return parseGrsAICreativeImageResult(raw, true)
}

func (grsAICreativeImageAdapter) poll(ctx context.Context, req CreativeImageProviderRequest, upstreamTaskID string) (CreativeImageProviderResult, error) {
	upstreamTaskID = strings.TrimSpace(upstreamTaskID)
	if upstreamTaskID == "" {
		return CreativeImageProviderResult{}, errors.New("creative image upstream task id is required")
	}
	raw, err := creativeImageProviderJSON(ctx, http.MethodGet, creativeJoinURL(req.Endpoint, "/v1/api/result?id="+url.QueryEscape(upstreamTaskID)), "Bearer "+strings.TrimSpace(req.Credential), nil)
	if err != nil {
		return CreativeImageProviderResult{}, err
	}
	return parseGrsAICreativeImageResult(raw, false)
}

func creativeGrsAIAspectRatioParam(req CreativeImageProviderRequest) string {
	aspectRatio := creativeStringParam(req.UserParams, "aspectRatio")
	if aspectRatio == "" {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(req.ProviderModelID), "gpt-image-2") || strings.EqualFold(strings.TrimSpace(req.ProviderModelID), "gpt-image-2-vip") {
		imageSize := creativeStringParam(req.UserParams, "imageSize")
		if mapped := creativeGrsAIGPTImagePixelAspectRatio(req.ProviderModelID, aspectRatio, imageSize); mapped != "" {
			return mapped
		}
	}
	return aspectRatio
}

func creativeGrsAIImageSizeParam(req CreativeImageProviderRequest) string {
	modelID := strings.TrimSpace(req.ProviderModelID)
	if modelID == "gpt-image-2" || modelID == "gpt-image-2-vip" {
		// GrsAI GPT image APIs encode the selected resolution as the aspectRatio
		// pixel value. Nano-banana keeps imageSize as a separate provider field.
		return ""
	}
	return creativeStringParam(req.UserParams, "imageSize")
}

func creativeGrsAIGPTImagePixelAspectRatio(modelID string, aspectRatio string, imageSize string) string {
	aspectRatio = strings.TrimSpace(aspectRatio)
	imageSize = strings.TrimSpace(imageSize)
	if aspectRatio == "" || aspectRatio == "auto" {
		return ""
	}
	if strings.Contains(aspectRatio, "x") {
		return aspectRatio
	}
	if imageSize == "" {
		imageSize = "1K"
	}
	if strings.TrimSpace(modelID) == "gpt-image-2" && imageSize != "1K" {
		imageSize = "1K"
	}
	if table, ok := creativeGrsAIGPTImageAspectRatioPixels[strings.TrimSpace(modelID)]; ok {
		if sizes, ok := table[aspectRatio]; ok {
			return sizes[imageSize]
		}
	}
	return aspectRatio
}

var creativeGrsAIGPTImageAspectRatioPixels = map[string]map[string]map[string]string{
	"gpt-image-2": {
		"1:1":  {"1K": "1024x1024"},
		"16:9": {"1K": "1672x941"},
		"9:16": {"1K": "941x1672"},
		"4:3":  {"1K": "1443x1090"},
		"3:4":  {"1K": "1090x1443"},
		"3:2":  {"1K": "1536x1024"},
		"2:3":  {"1K": "1024x1536"},
		"5:4":  {"1K": "1408x1120"},
		"4:5":  {"1K": "1120x1408"},
		"21:9": {"1K": "1920x832"},
		"9:21": {"1K": "832x1920"},
		"1:2":  {"1K": "896x1792"},
		"2:1":  {"1K": "1792x896"},
	},
	"gpt-image-2-vip": {
		"1:1":  {"1K": "1024x1024", "2K": "2048x2048", "4K": "2880x2880"},
		"16:9": {"1K": "1280x720", "2K": "2048x1152", "4K": "3840x2160"},
		"9:16": {"1K": "720x1280", "2K": "1152x2048", "4K": "2160x3840"},
		"4:3":  {"1K": "1152x864", "2K": "2304x1728", "4K": "3264x2448"},
		"3:4":  {"1K": "864x1152", "2K": "1728x2304", "4K": "2448x3264"},
		"3:2":  {"1K": "1536x1024", "2K": "2048x1360", "4K": "3504x2336"},
		"2:3":  {"1K": "1024x1536", "2K": "1360x2048", "4K": "2336x3504"},
		"5:4":  {"1K": "1120x896", "2K": "2240x1792", "4K": "3200x2560"},
		"4:5":  {"1K": "896x1120", "2K": "1792x2240", "4K": "2560x3200"},
		"21:9": {"1K": "1456x624", "2K": "2912x1248", "4K": "3840x1648"},
		"9:21": {"1K": "624x1456", "2K": "1248x2912", "4K": "1648x3840"},
		"1:3":  {"1K": "688x2048", "2K": "1280x3840", "4K": "1280x3840"},
		"3:1":  {"1K": "2048x688", "2K": "3840x1280", "4K": "3840x1280"},
		"1:2":  {"1K": "768x1536", "2K": "1536x3072", "4K": "1920x3840"},
		"2:1":  {"1K": "1536x768", "2K": "3072x1536", "4K": "3840x1920"},
	},
}

func creativeImageProviderJSON(ctx context.Context, method string, target string, authHeader string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		payload, err := common.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, errors.New("invalid creative image provider endpoint")
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(authHeader) != "" {
		request.Header.Set("Authorization", strings.TrimSpace(authHeader))
	}
	response, err := ensureHTTPClient().Do(request)
	if err != nil {
		return nil, errors.New("creative image provider request failed")
	}
	defer response.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if readErr != nil {
		return nil, errors.New("creative image provider response read failed")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("creative image provider returned status %d", response.StatusCode)
	}
	return raw, nil
}

func creativeJoinURL(endpoint string, path string) string {
	return strings.TrimRight(strings.TrimSpace(endpoint), "/") + path
}

func creativeStringParam(params map[string]any, key string) string {
	if params == nil {
		return ""
	}
	raw, ok := params[key]
	if !ok || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func parseDuomiCreativeImageResult(raw []byte, requireAcceptedID bool) (CreativeImageProviderResult, error) {
	var payload struct {
		Id       string `json:"id"`
		State    string `json:"state"`
		Progress int    `json:"progress"`
		Error    any    `json:"error"`
		Message  any    `json:"message"`
		Data     struct {
			Images []creativeProviderImageURL `json:"images"`
		} `json:"data"`
	}
	if err := common.Unmarshal(raw, &payload); err != nil {
		return CreativeImageProviderResult{}, errors.New("invalid creative image provider response")
	}
	id := strings.TrimSpace(payload.Id)
	state := strings.ToLower(strings.TrimSpace(payload.State))
	if requireAcceptedID && id == "" {
		return CreativeImageProviderResult{}, errors.New("creative image provider did not return a task id")
	}
	if state == "" && id != "" {
		state = "running"
	}
	result := CreativeImageProviderResult{
		UpstreamTaskID: id,
		Status:         creativeImageStatusFromProvider(state),
		Progress:       creativeProgressString(payload.Progress, state),
		FailReason:     creativeSanitizeProviderError(payload.Error, payload.Message),
	}
	if result.Status == model.TaskStatusSuccess {
		result.ResultURL = firstProviderImageURL(payload.Data.Images)
		if result.ResultURL == "" {
			return CreativeImageProviderResult{}, fmt.Errorf("%w: creative image provider succeeded without result", errCreativeImageProviderTerminalMalformed)
		}
	}
	if result.Status == model.TaskStatusFailure && result.FailReason == "" {
		result.FailReason = "creative image provider task failed"
	}
	return result, nil
}

func parseGrsAICreativeImageResult(raw []byte, requireAcceptedID bool) (CreativeImageProviderResult, error) {
	var payload struct {
		Id       string                     `json:"id"`
		Status   string                     `json:"status"`
		Progress int                        `json:"progress"`
		Error    any                        `json:"error"`
		Message  any                        `json:"message"`
		Results  []creativeProviderImageURL `json:"results"`
	}
	if err := common.Unmarshal(raw, &payload); err != nil {
		return CreativeImageProviderResult{}, errors.New("invalid creative image provider response")
	}
	id := strings.TrimSpace(payload.Id)
	status := strings.ToLower(strings.TrimSpace(payload.Status))
	if requireAcceptedID && id == "" {
		return CreativeImageProviderResult{}, errors.New("creative image provider did not return a task id")
	}
	result := CreativeImageProviderResult{
		UpstreamTaskID: id,
		Status:         creativeImageStatusFromProvider(status),
		Progress:       creativeProgressString(payload.Progress, status),
		FailReason:     creativeSanitizeProviderError(payload.Error, payload.Message),
	}
	if result.Status == model.TaskStatusSuccess {
		result.ResultURL = firstProviderImageURL(payload.Results)
		if result.ResultURL == "" {
			return CreativeImageProviderResult{}, fmt.Errorf("%w: creative image provider succeeded without result", errCreativeImageProviderTerminalMalformed)
		}
	}
	if result.Status == model.TaskStatusFailure && result.FailReason == "" {
		result.FailReason = "creative image provider task failed"
	}
	return result, nil
}

func firstProviderImageURL(items []creativeProviderImageURL) string {
	for _, item := range items {
		if url := strings.TrimSpace(item.URL); url != "" {
			return url
		}
	}
	return ""
}

func creativeImageStatusFromProvider(status string) model.TaskStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "succeeded", "success", "completed", "complete":
		return model.TaskStatusSuccess
	case "failed", "failure", "error", "violation", "cancelled", "canceled":
		return model.TaskStatusFailure
	case "queued", "pending":
		return model.TaskStatusSubmitted
	case "running", "processing", "in_progress", "":
		return model.TaskStatusInProgress
	default:
		return model.TaskStatusInProgress
	}
}

func creativeProgressString(progress int, status string) string {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	if progress == 0 {
		switch creativeImageStatusFromProvider(status) {
		case model.TaskStatusSuccess:
			progress = 100
		case model.TaskStatusFailure:
			progress = 0
		default:
			return creativeImageProviderDefaultProgress
		}
	}
	return fmt.Sprintf("%d%%", progress)
}

func creativeSanitizeProviderError(values ...any) string {
	for _, value := range values {
		text := strings.TrimSpace(fmt.Sprint(value))
		if text == "" || text == "<nil>" {
			continue
		}
		if CreativeSensitiveStringValue(text) {
			return "creative image provider task failed"
		}
		if len(text) > creativeImageProviderErrorMaxLength {
			text = text[:creativeImageProviderErrorMaxLength]
		}
		return text
	}
	return ""
}
