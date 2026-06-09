package controller

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

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
		if strings.TrimSpace(c.GetHeader("Authorization")) != "" {
			creativeOpenAIError(c, http.StatusBadRequest, "forbidden field Authorization")
			c.Abort()
			return
		}
		payload, err := creativeReadJSONMap(c)
		if err != nil {
			creativeOpenAIError(c, http.StatusBadRequest, err.Error())
			c.Abort()
			return
		}
		if field, forbidden := containsCreativeForbiddenField(payload); forbidden {
			creativeOpenAIError(c, http.StatusBadRequest, "forbidden field "+field)
			c.Abort()
			return
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

func creativeRelayWithSessionBroker(c *gin.Context, relayFormat types.RelayFormat) {
	if c.GetBool("use_access_token") {
		creativeOpenAIError(c, http.StatusForbidden, "creative relay requires a browser session")
		return
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
		return
	}
	Relay(c, relayFormat)
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
