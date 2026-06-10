package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const creativeBrokerBaseURL = "/creative/relay/v1"

const (
	creativeVideoPublicTaskIDContextKey    = "creative_video_public_task_id"
	creativeVideoIdempotencyKeyContextKey  = "creative_video_idempotency_key"
	creativeVideoContentContextKey         = "creative_video_content"
	creativeTaskPublicTaskIDContextKey     = "creative_task_public_task_id"
	creativeTaskIdempotencyKeyContextKey   = "creative_task_idempotency_key"
	creativeTaskIdempotencyScopeContextKey = "creative_task_idempotency_scope"
)

const (
	creativeSunoSubmitScopeMusic  = "suno.submit.music"
	creativeSunoSubmitScopeLyrics = "suno.submit.lyrics"
)

var creativeVideoRelayEnabled atomic.Bool

func init() {
	creativeVideoRelayEnabled.Store(loadCreativeVideoRelayEnabledFromEnv())
}

func loadCreativeVideoRelayEnabledFromEnv() bool {
	return common.GetEnvOrDefaultBool("CREATIVE_VIDEO_RELAY_ENABLED", false)
}

func SetCreativeVideoRelayEnabledForTest(enabled bool) func() {
	previous := creativeVideoRelayEnabled.Load()
	creativeVideoRelayEnabled.Store(enabled)
	return func() { creativeVideoRelayEnabled.Store(previous) }
}

func CreativeVideoRelayGate() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !creativeVideoRelayEnabled.Load() {
			creativeOpenAIError(c, http.StatusNotFound, "creative video relay is disabled")
			c.Abort()
			return
		}
		c.Next()
	}
}

func CreativeVideoSubmitIdempotency() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodPost {
			if !creativePrepareVideoSubmitIdempotency(c) {
				return
			}
			requestID := c.GetString(creativeVideoIdempotencyKeyContextKey)
			userID := c.GetInt("id")
			c.Next()
			if requestID != "" && c.Writer.Status() >= http.StatusBadRequest {
				if err := model.DeleteCreativeVideoIdempotency(userID, requestID); err != nil {
					common.SysError("cleanup failed creative video idempotency error: " + err.Error())
				}
			}
			return
		}
		c.Next()
	}
}

func CreativeSunoSubmitGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		modelName, scope, ok := creativeSunoModelAndScope(c.Param("action"))
		if !ok {
			creativeOpenAIError(c, http.StatusBadRequest, "invalid Suno action")
			c.Abort()
			return
		}
		if field, err := creativeSunoForbiddenSubmitField(c); err != nil {
			creativeOpenAIError(c, http.StatusBadRequest, err.Error())
			c.Abort()
			return
		} else if field != "" {
			creativeOpenAIError(c, http.StatusBadRequest, "forbidden field "+field)
			c.Abort()
			return
		}

		c.Set(middleware.ContextKeyCreativeRelayModelOverride, modelName)
		if !creativePrepareSunoSubmitIdempotency(c, scope) {
			return
		}
		requestID := c.GetString(creativeTaskIdempotencyKeyContextKey)
		userID := c.GetInt("id")
		c.Next()
		if requestID != "" && c.Writer.Status() >= http.StatusBadRequest {
			if err := model.DeleteCreativeVideoIdempotencyScoped(userID, scope, requestID); err != nil {
				common.SysError("cleanup failed creative Suno idempotency error: " + err.Error())
			}
		}
	}
}

func CreativeBootstrap(c *gin.Context) {
	if !creativeRequireSession(c) {
		return
	}
	csrfToken, nonce, err := middleware.EnsureCreativeSessionAuthMaterial(c)
	if err != nil {
		creativeAPIError(c, http.StatusInternalServerError, "failed to prepare creative session auth")
		return
	}
	models, catalogVersion, err := creativeModelsForUser(c)
	if err != nil {
		creativeAPIError(c, http.StatusInternalServerError, err.Error())
		return
	}
	preference, _, err := model.GetCreativeModelPreference(c.GetInt("id"))
	if err != nil {
		creativeAPIError(c, http.StatusInternalServerError, err.Error())
		return
	}
	preferenceValue, err := model.CreativeModelPreferenceValueFromJSON(preference.PreferenceJSON)
	if err != nil {
		creativeAPIError(c, http.StatusInternalServerError, "stored preference is invalid")
		return
	}
	assetSyncEnabled, assetSyncDisabledReason := service.CurrentCreativeAssetRuntime().Status()

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"auth": gin.H{
				"mode":      "session-broker",
				"csrfToken": csrfToken,
				"nonce":     nonce,
			},
			"profile": gin.H{
				"brokerBaseUrl": creativeBrokerBaseURL,
			},
			"catalogVersion": catalogVersion,
			"models":         models,
			"assetSync": gin.H{
				"enabled":        assetSyncEnabled,
				"disabledReason": assetSyncDisabledReason,
			},
			"assetSyncEnabled": assetSyncEnabled,
			"modelPreference": gin.H{
				"revision":   preference.Revision,
				"preference": preferenceValue,
			},
		},
	})
}

func CreativeListModels(c *gin.Context) {
	if !creativeRequireSession(c) {
		return
	}
	models, catalogVersion, err := creativeModelsForUser(c)
	if err != nil {
		creativeAPIError(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success":        true,
		"object":         "list",
		"data":           models,
		"catalogVersion": catalogVersion,
	})
}

func CreativeGetModelPreference(c *gin.Context) {
	if !creativeRequireSession(c) {
		return
	}
	preference, _, err := model.GetCreativeModelPreference(c.GetInt("id"))
	if err != nil {
		creativeAPIError(c, http.StatusInternalServerError, err.Error())
		return
	}
	value, err := model.CreativeModelPreferenceValueFromJSON(preference.PreferenceJSON)
	if err != nil {
		creativeAPIError(c, http.StatusInternalServerError, "stored preference is invalid")
		return
	}
	creativeAPISuccess(c, gin.H{
		"revision":   preference.Revision,
		"preference": value,
	})
}

func CreativePatchModelPreference(c *gin.Context) {
	if !creativeRequireSession(c) {
		return
	}
	payload, ok := creativeReadSafeJSONBody(c)
	if !ok {
		return
	}
	baseRevision, ok := creativeRequiredInt(payload, "baseRevision")
	if !ok {
		creativeAPIError(c, http.StatusBadRequest, "baseRevision is required")
		return
	}

	preferenceSource := payload
	if nested, exists := payload["preference"]; exists {
		nestedMap, ok := nested.(map[string]any)
		if !ok {
			creativeAPIError(c, http.StatusBadRequest, "preference must be an object")
			return
		}
		preferenceSource = nestedMap
	}
	safePreference, err := model.SanitizeCreativeModelPreference(preferenceSource)
	if err != nil {
		creativeAPIError(c, http.StatusBadRequest, err.Error())
		return
	}
	updated, conflict, err := model.PatchCreativeModelPreference(c.GetInt("id"), baseRevision, safePreference)
	if err != nil {
		creativeAPIError(c, http.StatusInternalServerError, err.Error())
		return
	}
	value, err := model.CreativeModelPreferenceValueFromJSON(updated.PreferenceJSON)
	if err != nil {
		creativeAPIError(c, http.StatusInternalServerError, "stored preference is invalid")
		return
	}
	status := http.StatusOK
	if conflict {
		status = http.StatusConflict
	}
	c.JSON(status, gin.H{
		"success": !conflict,
		"data": gin.H{
			"revision":   updated.Revision,
			"preference": value,
		},
		"message": creativeConflictMessage(conflict),
	})
}

func CreativeListDocuments(c *gin.Context) {
	if !creativeRequireSession(c) {
		return
	}
	documents, err := model.ListCreativeDocuments(c.GetInt("id"))
	if err != nil {
		creativeAPIError(c, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]gin.H, 0, len(documents))
	for i := range documents {
		items = append(items, creativeDocumentResponse(&documents[i], false))
	}
	creativeAPISuccess(c, gin.H{"documents": items})
}

func CreativeCreateDocument(c *gin.Context) {
	if !creativeRequireSession(c) {
		return
	}
	payload, ok := creativeReadSafeJSONBody(c)
	if !ok {
		return
	}
	clientMutationId := creativeString(payload["clientMutationId"])
	if existing, exists, err := model.GetCreativeDocumentByClientMutationId(c.GetInt("id"), clientMutationId); err != nil {
		creativeAPIError(c, http.StatusInternalServerError, err.Error())
		return
	} else if exists {
		creativeAPISuccess(c, gin.H{"document": creativeDocumentResponse(existing, true)})
		return
	}

	documentId := creativeString(payload["id"])
	if documentId == "" {
		documentId = creativeString(payload["documentId"])
	}
	if documentId != "" {
		if existing, exists, err := model.GetCreativeDocument(c.GetInt("id"), documentId); err != nil {
			creativeAPIError(c, http.StatusInternalServerError, err.Error())
			return
		} else if exists {
			c.JSON(http.StatusConflict, gin.H{"success": false, "message": "document already exists", "data": gin.H{"document": creativeDocumentResponse(existing, true)}})
			return
		}
	}
	snapshotJSON, err := model.NormalizeCreativeJSONValue(payload["snapshot"], "{}")
	if err != nil {
		creativeAPIError(c, http.StatusBadRequest, err.Error())
		return
	}
	metadataJSON, err := model.NormalizeCreativeJSONValue(payload["metadata"], "{}")
	if err != nil {
		creativeAPIError(c, http.StatusBadRequest, err.Error())
		return
	}
	assetIds, err := model.ValidateCreativeDocumentAssetRefs(c.GetInt("id"), snapshotJSON, metadataJSON, creativeAPIRequestOrigin(c))
	if err != nil {
		creativeAPIError(c, http.StatusBadRequest, err.Error())
		return
	}
	document, err := model.CreateCreativeDocument(&model.CreativeDocument{
		UserId:           c.GetInt("id"),
		DocumentId:       documentId,
		Title:            creativeTitle(payload["title"]),
		SnapshotJSON:     snapshotJSON,
		MetadataJSON:     metadataJSON,
		ClientMutationId: clientMutationId,
	})
	if err != nil {
		creativeAPIError(c, http.StatusInternalServerError, err.Error())
		return
	}
	if err := model.RefreshCreativeDocumentAssetRefs(c.GetInt("id"), document.DocumentId, assetIds); err != nil {
		creativeAPIError(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusCreated, gin.H{"success": true, "message": "", "data": gin.H{"document": creativeDocumentResponse(document, true)}})
}

func CreativeGetDocument(c *gin.Context) {
	if !creativeRequireSession(c) {
		return
	}
	document, exists, err := model.GetCreativeDocument(c.GetInt("id"), c.Param("id"))
	if err != nil {
		creativeAPIError(c, http.StatusInternalServerError, err.Error())
		return
	}
	if !exists {
		creativeAPIError(c, http.StatusNotFound, "document not found")
		return
	}
	creativeAPISuccess(c, gin.H{"document": creativeDocumentResponse(document, true)})
}

func CreativeUpdateDocument(c *gin.Context) {
	if !creativeRequireSession(c) {
		return
	}
	payload, ok := creativeReadSafeJSONBody(c)
	if !ok {
		return
	}
	baseRevision, ok := creativeRequiredInt(payload, "baseRevision")
	if !ok {
		creativeAPIError(c, http.StatusBadRequest, "baseRevision is required")
		return
	}
	patch := model.CreativeDocumentPatch{ClientMutationId: creativeString(payload["clientMutationId"])}
	if _, exists := payload["title"]; exists {
		title := creativeTitle(payload["title"])
		patch.Title = &title
	}
	if value, exists := payload["snapshot"]; exists {
		snapshotJSON, err := model.NormalizeCreativeJSONValue(value, "{}")
		if err != nil {
			creativeAPIError(c, http.StatusBadRequest, err.Error())
			return
		}
		patch.SnapshotJSON = &snapshotJSON
	}
	if value, exists := payload["metadata"]; exists {
		metadataJSON, err := model.NormalizeCreativeJSONValue(value, "{}")
		if err != nil {
			creativeAPIError(c, http.StatusBadRequest, err.Error())
			return
		}
		patch.MetadataJSON = &metadataJSON
	}
	current, exists, err := model.GetCreativeDocument(c.GetInt("id"), c.Param("id"))
	if err != nil {
		creativeAPIError(c, http.StatusInternalServerError, err.Error())
		return
	}
	if !exists {
		creativeAPIError(c, http.StatusNotFound, "document not found")
		return
	}
	targetSnapshotJSON := current.SnapshotJSON
	if patch.SnapshotJSON != nil {
		targetSnapshotJSON = *patch.SnapshotJSON
	}
	targetMetadataJSON := current.MetadataJSON
	if patch.MetadataJSON != nil {
		targetMetadataJSON = *patch.MetadataJSON
	}
	assetIds, err := model.ValidateCreativeDocumentAssetRefs(c.GetInt("id"), targetSnapshotJSON, targetMetadataJSON, creativeAPIRequestOrigin(c))
	if err != nil {
		creativeAPIError(c, http.StatusBadRequest, err.Error())
		return
	}
	document, conflict, err := model.UpdateCreativeDocumentSnapshot(c.GetInt("id"), c.Param("id"), baseRevision, patch)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			creativeAPIError(c, http.StatusNotFound, "document not found")
			return
		}
		creativeAPIError(c, http.StatusInternalServerError, err.Error())
		return
	}
	if conflict {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "revision conflict", "data": gin.H{"document": creativeDocumentResponse(document, true)}})
		return
	}
	if err := model.RefreshCreativeDocumentAssetRefs(c.GetInt("id"), document.DocumentId, assetIds); err != nil {
		creativeAPIError(c, http.StatusInternalServerError, err.Error())
		return
	}
	creativeAPISuccess(c, gin.H{"document": creativeDocumentResponse(document, true)})
}

func CreativeDeleteDocument(c *gin.Context) {
	if !creativeRequireSession(c) {
		return
	}
	baseRevision := creativeDeleteBaseRevision(c)
	document, found, conflict, err := model.DeleteCreativeDocument(c.GetInt("id"), c.Param("id"), baseRevision)
	if err != nil {
		creativeAPIError(c, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		creativeAPIError(c, http.StatusNotFound, "document not found")
		return
	}
	if conflict {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "revision conflict", "data": gin.H{"document": creativeDocumentResponse(document, true)}})
		return
	}
	if err := model.DeleteCreativeDocumentAssetRefs(c.GetInt("id"), c.Param("id")); err != nil {
		creativeAPIError(c, http.StatusInternalServerError, err.Error())
		return
	}
	creativeAPISuccess(c, gin.H{"id": c.Param("id")})
}

func CreativeRejectForbiddenRelayFields() gin.HandlerFunc {
	return func(c *gin.Context) {
		if field, forbidden := creativeForbiddenRelayHeader(c); forbidden {
			creativeOpenAIError(c, http.StatusBadRequest, "forbidden field "+field)
			c.Abort()
			return
		}

		if field, forbidden := creativeForbiddenRelayQuery(c); forbidden {
			creativeOpenAIError(c, http.StatusBadRequest, "forbidden field "+field)
			c.Abort()
			return
		}

		if creativeUnsafeMethod(c.Request.Method) {
			if field, err := creativeForbiddenRelayBodyField(c); err != nil {
				creativeOpenAIError(c, http.StatusBadRequest, err.Error())
				c.Abort()
				return
			} else if field != "" {
				creativeOpenAIError(c, http.StatusBadRequest, "forbidden field "+field)
				c.Abort()
				return
			}
		}

		c.Next()
	}
}

func CreativeRelayChatCompletions(c *gin.Context) {
	creativeRelayWithSessionBroker(c, types.RelayFormatOpenAI)
}

func CreativeRelayImagesGenerations(c *gin.Context) {
	creativeRelayWithSessionBroker(c, types.RelayFormatOpenAIImage)
}

func CreativeRelayVideos(c *gin.Context) {
	if !creativeSetupSessionBrokerToken(c) {
		return
	}
	RelayTask(c)
}

func CreativeRelayVideoFetch(c *gin.Context) {
	if !creativeSetupSessionBrokerToken(c) {
		return
	}
	RelayTaskFetch(c)
}

func CreativeRelaySunoSubmit(c *gin.Context) {
	if !creativeSetupSessionBrokerToken(c) {
		return
	}
	RelayTask(c)
}

func CreativeRelaySunoFetch(c *gin.Context) {
	if !creativeSetupSessionBrokerToken(c) {
		return
	}
	RelayTaskFetch(c)
}

func CreativeRelayVideoContent(c *gin.Context) {
	if !creativeSetupSessionBrokerToken(c) {
		return
	}
	c.Set(creativeVideoContentContextKey, true)
	VideoProxy(c)
}

func creativeRelayWithSessionBroker(c *gin.Context, relayFormat types.RelayFormat) {
	if !creativeSetupSessionBrokerToken(c) {
		return
	}
	Relay(c, relayFormat)
}

func creativeSetupSessionBrokerToken(c *gin.Context) bool {
	if c.GetBool("use_access_token") {
		creativeOpenAIError(c, http.StatusForbidden, "creative relay requires a browser session")
		return false
	}
	usingGroup := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	if usingGroup == "" {
		usingGroup = c.GetString("group")
	}
	userId := c.GetInt("id")
	tempToken := &model.Token{
		UserId: userId,
		Name:   fmt.Sprintf("creative-session-broker-%s", usingGroup),
		Group:  usingGroup,
	}
	if err := middleware.SetupContextForToken(c, tempToken); err != nil {
		creativeOpenAIError(c, http.StatusForbidden, err.Error())
		return false
	}
	return true
}

func creativePrepareVideoSubmitIdempotency(c *gin.Context) bool {
	requestID := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if requestID == "" {
		requestID = strings.TrimSpace(c.GetHeader("X-Creative-Request-Id"))
	}
	if requestID == "" {
		creativeOpenAIError(c, http.StatusBadRequest, "creative video submit requires Idempotency-Key")
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
	record, existed, err := model.PrepareCreativeVideoIdempotency(c.GetInt("id"), requestID, payloadHash)
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
			creativeOpenAIError(c, http.StatusConflict, "creative video request is still being prepared")
			c.Abort()
			return false
		}
		if task, ok, taskErr := model.GetByTaskId(c.GetInt("id"), record.TaskID); taskErr == nil && ok && task != nil {
			c.JSON(http.StatusOK, task.ToOpenAIVideo())
			c.Abort()
			return false
		}
		creativeOpenAIError(c, http.StatusConflict, "creative video request is still being prepared")
		c.Abort()
		return false
	}
	c.Set(creativeVideoPublicTaskIDContextKey, record.TaskID)
	c.Set(creativeVideoIdempotencyKeyContextKey, requestID)
	c.Set(creativeTaskPublicTaskIDContextKey, record.TaskID)
	c.Set(creativeTaskIdempotencyKeyContextKey, requestID)
	c.Set(creativeTaskIdempotencyScopeContextKey, model.CreativeVideoIdempotencyScopeVideoSubmit)
	return true
}

func creativePrepareSunoSubmitIdempotency(c *gin.Context, scope string) bool {
	requestID := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if requestID == "" {
		requestID = strings.TrimSpace(c.GetHeader("X-Creative-Request-Id"))
	}
	if requestID == "" {
		creativeOpenAIError(c, http.StatusBadRequest, "creative Suno submit requires Idempotency-Key")
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
	record, existed, err := model.PrepareCreativeVideoIdempotencyScoped(c.GetInt("id"), scope, requestID, payloadHash)
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
			creativeOpenAIError(c, http.StatusConflict, "creative Suno request is still being prepared")
			c.Abort()
			return false
		}
		if task, ok, taskErr := model.GetByTaskId(c.GetInt("id"), record.TaskID); taskErr == nil && ok && task != nil {
			c.JSON(http.StatusOK, dto.TaskResponse[string]{
				Code: dto.TaskSuccessCode,
				Data: task.TaskID,
			})
			c.Abort()
			return false
		}
		creativeOpenAIError(c, http.StatusConflict, "creative Suno request is still being prepared")
		c.Abort()
		return false
	}
	c.Set(creativeTaskPublicTaskIDContextKey, record.TaskID)
	c.Set(creativeTaskIdempotencyKeyContextKey, requestID)
	c.Set(creativeTaskIdempotencyScopeContextKey, scope)
	return true
}

func creativeSunoModelAndScope(action string) (string, string, bool) {
	switch strings.TrimSpace(action) {
	case "music":
		return "suno_music", creativeSunoSubmitScopeMusic, true
	case "lyrics":
		return "suno_lyrics", creativeSunoSubmitScopeLyrics, true
	default:
		return "", "", false
	}
}

func creativeSunoForbiddenSubmitField(c *gin.Context) (string, error) {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return "", err
	}
	body, err := storage.Bytes()
	if err != nil {
		return "", err
	}
	defer func() {
		if _, seekErr := storage.Seek(0, io.SeekStart); seekErr == nil {
			c.Request.Body = io.NopCloser(storage)
		}
	}()
	if len(strings.TrimSpace(string(body))) == 0 {
		return "", nil
	}

	contentType := strings.ToLower(strings.TrimSpace(c.GetHeader("Content-Type")))
	switch {
	case strings.Contains(contentType, "multipart/form-data"):
		form, err := common.ParseMultipartFormReusable(c)
		if err != nil {
			return "", err
		}
		defer form.RemoveAll()
		for key := range form.Value {
			if creativeNormalizeRelayFieldName(key) == "model" {
				return key, nil
			}
		}
		for key := range form.File {
			if creativeNormalizeRelayFieldName(key) == "model" {
				return key, nil
			}
		}
	case strings.Contains(contentType, "application/x-www-form-urlencoded"):
		values, err := url.ParseQuery(string(body))
		if err != nil {
			return "", err
		}
		for key := range values {
			if creativeNormalizeRelayFieldName(key) == "model" {
				return key, nil
			}
		}
	case strings.Contains(contentType, "json") || contentType == "":
		var payload map[string]any
		if err := common.Unmarshal(body, &payload); err != nil {
			return "", err
		}
		for key := range payload {
			if creativeNormalizeRelayFieldName(key) == "model" {
				return key, nil
			}
		}
	}
	return "", nil
}

func creativeModelsForUser(c *gin.Context) ([]dto.OpenAIModels, string, error) {
	userCache, err := model.GetUserCache(c.GetInt("id"))
	if err != nil {
		return nil, "", err
	}
	modelNames, ownerGroups := service.GetUserCreativeModelPool(userCache.Group)
	ownerByModel := map[string]string{}
	if len(ownerGroups) > 0 {
		ownerByModel = getPreferredModelOwners(modelNames, ownerGroups)
	}
	models := make([]dto.OpenAIModels, 0, len(modelNames))
	for _, modelName := range modelNames {
		models = append(models, buildOpenAIModel(modelName, ownerByModel))
	}
	encoded, err := common.Marshal(models)
	if err != nil {
		return nil, "", err
	}
	return models, common.Sha1(encoded), nil
}

func creativeRequireSession(c *gin.Context) bool {
	if c.GetBool("use_access_token") {
		creativeAPIError(c, http.StatusForbidden, "creative API requires a browser session")
		return false
	}
	return true
}

func creativeReadSafeJSONBody(c *gin.Context) (map[string]any, bool) {
	payload, err := creativeReadJSONMap(c)
	if err != nil {
		creativeAPIError(c, http.StatusBadRequest, err.Error())
		return nil, false
	}
	if field, forbidden := containsCreativeForbiddenField(payload); forbidden {
		creativeAPIError(c, http.StatusBadRequest, "forbidden field "+field)
		return nil, false
	}
	return payload, true
}

func creativeReadJSONMap(c *gin.Context) (map[string]any, error) {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, err
	}
	body, err := storage.Bytes()
	if err != nil {
		return nil, err
	}
	defer func() {
		if _, seekErr := storage.Seek(0, io.SeekStart); seekErr == nil {
			c.Request.Body = io.NopCloser(storage)
		}
	}()
	if strings.TrimSpace(string(body)) == "" {
		return map[string]any{}, nil
	}
	var payload map[string]any
	if err := common.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if payload == nil {
		payload = map[string]any{}
	}
	return payload, nil
}

func creativeUnsafeMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

func creativeForbiddenRelayHeader(c *gin.Context) (string, bool) {
	if c == nil || c.Request == nil {
		return "", false
	}
	for key, values := range c.Request.Header {
		if !creativeForbiddenRelayHeaderKey(key) {
			continue
		}
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				return key, true
			}
		}
	}
	return "", false
}

func creativeForbiddenRelayQuery(c *gin.Context) (string, bool) {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return "", false
	}
	for key, values := range c.Request.URL.Query() {
		if !creativeForbiddenRelayQueryKey(key) {
			continue
		}
		_ = values
		return key, true
	}
	return "", false
}

func creativeForbiddenRelayBodyField(c *gin.Context) (string, error) {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return "", err
	}
	body, err := storage.Bytes()
	if err != nil {
		return "", err
	}
	defer func() {
		if _, seekErr := storage.Seek(0, io.SeekStart); seekErr == nil {
			c.Request.Body = io.NopCloser(storage)
		}
	}()
	if len(strings.TrimSpace(string(body))) == 0 {
		return "", nil
	}

	contentType := strings.ToLower(strings.TrimSpace(c.GetHeader("Content-Type")))
	switch {
	case strings.Contains(contentType, "multipart/form-data"):
		form, err := common.ParseMultipartFormReusable(c)
		if err != nil {
			return "", err
		}
		defer form.RemoveAll()
		for key := range form.Value {
			if creativeForbiddenRelayFormKey(key) {
				return key, nil
			}
		}
		for key := range form.File {
			if creativeForbiddenRelayFileKey(key) {
				return key, nil
			}
		}
		return "", nil
	case strings.Contains(contentType, "application/x-www-form-urlencoded"):
		values, err := url.ParseQuery(string(body))
		if err != nil {
			return "", err
		}
		for key := range values {
			if creativeForbiddenRelayFormKey(key) {
				return key, nil
			}
		}
		return "", nil
	case strings.Contains(contentType, "json") || contentType == "":
		payload, err := creativeReadJSONMap(c)
		if err != nil {
			return "", err
		}
		if field, forbidden := containsCreativeForbiddenRelayBodyField(payload); forbidden {
			return field, nil
		}
		return "", nil
	default:
		return "", nil
	}
}

func containsCreativeForbiddenRelayBodyField(value any) (string, bool) {
	return containsCreativeForbiddenRelayBodyFieldAt(value, "")
}

func containsCreativeForbiddenRelayBodyFieldAt(value any, path string) (string, bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			if creativeForbiddenRelayBodyKey(key, path == "") {
				return childPath, true
			}
			if found, ok := containsCreativeForbiddenRelayBodyFieldAt(child, childPath); ok {
				return found, true
			}
		}
	case []any:
		for i, child := range typed {
			childPath := fmt.Sprintf("%s[%d]", path, i)
			if path == "" {
				childPath = fmt.Sprintf("[%d]", i)
			}
			if found, ok := containsCreativeForbiddenRelayBodyFieldAt(child, childPath); ok {
				return found, true
			}
		}
	case string:
		if creativeStringLooksLikeSecret(typed) {
			if path == "" {
				return "value", true
			}
			return path, true
		}
	}
	return "", false
}

func containsCreativeForbiddenField(value any) (string, bool) {
	return containsCreativeForbiddenFieldAt(value, "")
}

func containsCreativeForbiddenFieldAt(value any, path string) (string, bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			if creativeForbiddenKey(key) {
				return childPath, true
			}
			if found, ok := containsCreativeForbiddenFieldAt(child, childPath); ok {
				return found, true
			}
		}
	case []any:
		for i, child := range typed {
			childPath := fmt.Sprintf("%s[%d]", path, i)
			if path == "" {
				childPath = fmt.Sprintf("[%d]", i)
			}
			if found, ok := containsCreativeForbiddenFieldAt(child, childPath); ok {
				return found, true
			}
		}
	case string:
		if creativeStringLooksLikeSecret(typed) {
			if path == "" {
				return "value", true
			}
			return path, true
		}
	}
	return "", false
}

func creativeForbiddenKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.ReplaceAll(normalized, "_", "")
	normalized = strings.ReplaceAll(normalized, "-", "")
	if strings.HasPrefix(normalized, "upstream") {
		return true
	}
	switch normalized {
	case "apikey", "apikeys", "authorization", "bearer", "bearertoken", "baseurl", "channel", "channelid", "channeloverride", "channeltype", "provider", "providerid", "provideroverride", "providertype", "token", "accesstoken", "refreshtoken", "idtoken", "internaltoken", "secret", "secretkey", "sourceurl", "objectkey", "bucketurl", "signedurl", "presignedurl", "accesskeyid", "secretaccesskey", "s3endpoint", "storagebackend":
		return true
	default:
		return false
	}
}

func creativeForbiddenRelayHeaderKey(key string) bool {
	normalized := creativeNormalizeRelayFieldName(key)
	if strings.Contains(normalized, "apikey") ||
		strings.Contains(normalized, "authorization") ||
		strings.Contains(normalized, "baseurl") ||
		strings.HasSuffix(normalized, "token") {
		return true
	}
	return creativeForbiddenRelayBodyNormalizedKey(normalized)
}

func creativeForbiddenRelayQueryKey(key string) bool {
	return creativeForbiddenRelayFieldPathKey(key, false)
}

func creativeForbiddenRelayFormKey(key string) bool {
	return creativeForbiddenRelayFieldPathKey(key, true)
}

func creativeForbiddenRelayFileKey(key string) bool {
	return creativeForbiddenRelayFieldPathKey(key, false)
}

func creativeForbiddenRelayBodyKey(key string, topLevel bool) bool {
	return creativeForbiddenRelayFieldPathKey(key, topLevel)
}

func creativeForbiddenRelayFieldPathKey(key string, allowTopLevelModel bool) bool {
	normalized := creativeNormalizeRelayFieldName(key)
	if allowTopLevelModel && normalized == "model" {
		return false
	}
	if creativeForbiddenRelayBodyNormalizedKey(normalized) {
		return true
	}
	for _, segment := range creativeRelayFieldSegments(key) {
		if allowTopLevelModel && segment == "model" && len(creativeRelayFieldSegments(key)) == 1 {
			continue
		}
		if creativeForbiddenRelayBodyNormalizedKey(segment) {
			return true
		}
	}
	return false
}

func creativeForbiddenRelayBodyNormalizedKey(normalized string) bool {
	if normalized == "" {
		return false
	}
	if strings.HasPrefix(normalized, "upstream") ||
		strings.HasPrefix(strings.TrimPrefix(normalized, "x"), "upstream") ||
		strings.Contains(normalized, "apikey") ||
		strings.Contains(normalized, "accesstoken") {
		return true
	}
	switch normalized {
	case "apikey", "apikeys", "apitoken", "key", "authorization", "proxyauthorization", "bearer", "bearertoken",
		"baseurl", "xbaseurl", "upstreambaseurl", "provider", "xprovider", "providerid", "xproviderid", "providername", "xprovidername", "provideroverride", "xprovideroverride", "providertype",
		"channel", "xchannel", "channelid", "xchannelid", "channeloverride", "xchanneloverride", "channeltype",
		"group", "xgroup", "groupid", "xgroupid", "model", "xmodel", "modelid", "xmodelid", "modeloverride", "xmodeloverride",
		"endpoint", "xendpoint", "url", "xurl", "proxy", "xproxy", "headers", "requestheaders",
		"token", "xtoken", "accesstoken", "xaccesstoken", "refreshtoken", "idtoken", "internaltoken",
		"secret", "secretkey", "sourceurl", "objectkey", "bucketurl", "signedurl",
		"presignedurl", "accesskeyid", "secretaccesskey", "s3endpoint", "storagebackend", "organization", "openaiorganization":
		return true
	default:
		return false
	}
}

func creativeRelayFieldSegments(key string) []string {
	parts := strings.FieldsFunc(key, func(r rune) bool {
		switch r {
		case '.', '[', ']', '(', ')', '{', '}', '/', '\\', ':', ';', ',', ' ', '\t', '\n', '\r':
			return true
		default:
			return false
		}
	})
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		segment := creativeNormalizeRelayFieldName(part)
		if segment != "" {
			segments = append(segments, segment)
		}
	}
	return segments
}

func creativeNormalizeRelayFieldName(key string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(key)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func creativeStringLooksLikeSecret(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	return creativeContainsSkStyleSecret(value) || creativeContainsBearerSecret(value) || creativeStringLooksCredentialedURL(value)
}

func creativeStringLooksCredentialedURL(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	if !strings.Contains(lower, "http://") && !strings.Contains(lower, "https://") {
		return false
	}
	for _, marker := range []string{
		"x-amz-signature",
		"x-amz-credential",
		"awsaccesskeyid",
		"signature=",
		"token=",
		"access_token=",
		"api_key=",
		"apikey=",
		"secret=",
		"expires=",
		"sig=",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func creativeContainsSkStyleSecret(value string) bool {
	lower := strings.ToLower(value)
	for searchFrom := 0; searchFrom < len(value); {
		idx := strings.Index(lower[searchFrom:], "sk-")
		if idx < 0 {
			return false
		}
		idx += searchFrom
		if idx > 0 && creativeSecretTokenChar(value[idx-1]) {
			searchFrom = idx + len("sk-")
			continue
		}
		if len(creativeTokenFrom(value[idx:])) >= 16 {
			return true
		}
		searchFrom = idx + len("sk-")
	}
	return false
}

func creativeContainsBearerSecret(value string) bool {
	lower := strings.ToLower(value)
	for searchFrom := 0; searchFrom < len(value); {
		idx := strings.Index(lower[searchFrom:], "bearer")
		if idx < 0 {
			return false
		}
		idx += searchFrom
		afterBearer := idx + len("bearer")
		if idx > 0 && creativeSecretTokenChar(value[idx-1]) {
			searchFrom = afterBearer
			continue
		}
		if afterBearer >= len(value) || !creativeWhitespace(value[afterBearer]) {
			searchFrom = afterBearer
			continue
		}
		tokenStart := afterBearer
		for tokenStart < len(value) && creativeWhitespace(value[tokenStart]) {
			tokenStart++
		}
		if creativeBearerTokenLooksSecret(creativeTokenFrom(value[tokenStart:])) {
			return true
		}
		searchFrom = afterBearer
	}
	return false
}

func creativeBearerTokenLooksSecret(token string) bool {
	if len(token) < 12 {
		return false
	}
	if strings.HasPrefix(strings.ToLower(token), "sk-") {
		return true
	}
	hasLower := false
	hasUpper := false
	hasDigit := false
	hasSymbol := false
	for i := 0; i < len(token); i++ {
		ch := token[i]
		switch {
		case ch >= 'a' && ch <= 'z':
			hasLower = true
		case ch >= 'A' && ch <= 'Z':
			hasUpper = true
		case ch >= '0' && ch <= '9':
			hasDigit = true
		default:
			hasSymbol = true
		}
	}
	return (hasDigit && (hasLower || hasUpper)) ||
		(hasSymbol && (hasLower || hasUpper || hasDigit) && len(token) >= 16) ||
		(hasLower && hasUpper && len(token) >= 16)
}

func creativeTokenFrom(value string) string {
	end := 0
	for end < len(value) && creativeSecretTokenChar(value[end]) {
		end++
	}
	return value[:end]
}

func creativeSecretTokenChar(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') ||
		(ch >= 'A' && ch <= 'Z') ||
		(ch >= '0' && ch <= '9') ||
		ch == '_' || ch == '-' || ch == '.' || ch == '~' || ch == '+' || ch == '/' || ch == '='
}

func creativeWhitespace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r'
}

func creativeRequiredInt(payload map[string]any, key string) (int, bool) {
	value, exists := payload[key]
	if !exists {
		return 0, false
	}
	parsed, ok := creativeInt(value)
	return parsed, ok
}

func creativeInt(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		return int(v), v == float64(int(v))
	case int:
		return v, true
	case int64:
		return int(v), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(v))
		return parsed, err == nil
	default:
		return 0, false
	}
}

func creativeDeleteBaseRevision(c *gin.Context) *int {
	if query := strings.TrimSpace(c.Query("baseRevision")); query != "" {
		if parsed, err := strconv.Atoi(query); err == nil {
			return &parsed
		}
	}
	payload, err := creativeReadJSONMap(c)
	if err != nil {
		return nil
	}
	if parsed, ok := creativeRequiredInt(payload, "baseRevision"); ok {
		return &parsed
	}
	return nil
}

func creativeString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func creativeTitle(value any) string {
	title := creativeString(value)
	if len(title) > 255 {
		return title[:255]
	}
	return title
}

func creativeDocumentResponse(document *model.CreativeDocument, includeSnapshot bool) gin.H {
	response := gin.H{
		"id":               document.DocumentId,
		"title":            document.Title,
		"revision":         document.Revision,
		"clientMutationId": document.ClientMutationId,
		"createdTime":      document.CreatedTime,
		"updatedTime":      document.UpdatedTime,
		"metadata":         creativeDecodeJSON(document.MetadataJSON),
	}
	if includeSnapshot {
		response["snapshot"] = creativeDecodeJSON(document.SnapshotJSON)
	}
	return response
}

func creativeDecodeJSON(jsonText string) any {
	if strings.TrimSpace(jsonText) == "" {
		return gin.H{}
	}
	var decoded any
	if err := common.Unmarshal([]byte(jsonText), &decoded); err != nil {
		return gin.H{}
	}
	return decoded
}

func creativeAPISuccess(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}

func creativeAPIError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"success": false, "message": message})
}

func creativeOpenAIError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"error": types.OpenAIError{Message: message, Type: "invalid_request_error"}})
}

func creativeConflictMessage(conflict bool) string {
	if conflict {
		return "revision conflict"
	}
	return ""
}

func creativeAPIRequestOrigin(c *gin.Context) string {
	scheme := strings.TrimSpace(c.GetHeader("X-Forwarded-Proto"))
	if commaIndex := strings.IndexByte(scheme, ','); commaIndex >= 0 {
		scheme = strings.TrimSpace(scheme[:commaIndex])
	}
	if scheme == "" {
		if c.Request.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := strings.TrimSpace(c.GetHeader("X-Forwarded-Host"))
	if commaIndex := strings.IndexByte(host, ','); commaIndex >= 0 {
		host = strings.TrimSpace(host[:commaIndex])
	}
	if host == "" && c.Request != nil {
		host = strings.TrimSpace(c.Request.Host)
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}
