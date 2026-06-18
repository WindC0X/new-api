package controller

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func GetCreativeModelBindings(c *gin.Context) {
	creativeModelBindingsNoStore(c)
	if !creativeModelBindingsRequireDashboardSession(c) {
		return
	}
	state, err := service.GetCreativeModelBindingsAdminState()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	common.ApiSuccess(c, state)
}

func GetCreativeChannelSummaries(c *gin.Context) {
	creativeModelBindingsNoStore(c)
	if !creativeModelBindingsRequireDashboardSession(c) {
		return
	}

	pageInfo := common.GetPageQuery(c)
	channelID, ok := creativeOptionalPositiveIntQuery(c, "channel_id")
	if !ok {
		return
	}
	if channelID == 0 {
		var idOK bool
		channelID, idOK = creativeOptionalPositiveIntQuery(c, "id")
		if !idOK {
			return
		}
	}
	keyword := strings.TrimSpace(c.Query("keyword"))
	if keyword == "" {
		keyword = strings.TrimSpace(c.Query("q"))
	}
	result, err := service.ListCreativeChannelSummaries(
		pageInfo.GetPage(),
		pageInfo.GetPageSize(),
		keyword,
		channelID,
		c.Query("group"),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "failed to load creative channel summaries"})
		return
	}
	common.ApiSuccess(c, result)
}

func GetCreativeAdapterManifests(c *gin.Context) {
	creativeModelBindingsNoStore(c)
	if !creativeModelBindingsRequireDashboardSession(c) {
		return
	}
	state, err := service.GetCreativeAdapterManifestAdminState()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	common.ApiSuccess(c, state)
}

func ValidateCreativeModelBindings(c *gin.Context) {
	creativeModelBindingsNoStore(c)
	if !creativeModelBindingsRequireDashboardSession(c) {
		return
	}
	config, ok := creativeModelBindingsConfigFromRequest(c)
	if !ok {
		return
	}
	state, err := service.BuildCreativeModelBindingsAdminState(config)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	common.ApiSuccess(c, gin.H{"valid": true, "state": state})
}

func DryRunCreativeModelBindings(c *gin.Context) {
	creativeModelBindingsNoStore(c)
	if !creativeModelBindingsRequireDashboardSession(c) {
		return
	}
	config, ok := creativeModelBindingsConfigFromRequest(c)
	if !ok {
		return
	}
	result, err := service.BuildCreativeModelBindingsDryRun(config)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	common.ApiSuccess(c, result)
}

func UpdateCreativeModelBindings(c *gin.Context) {
	creativeModelBindingsNoStore(c)
	if !creativeModelBindingsRequireDashboardSession(c) {
		return
	}
	config, ok := creativeModelBindingsConfigFromRequest(c)
	if !ok {
		return
	}
	dryRun, err := service.BuildCreativeModelBindingsDryRun(config)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	if !dryRun.NoProviderCall {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "creative model bindings dry-run did not prove noProviderCall=true"})
		return
	}
	config, _, err = service.UpdateStoredCreativeModelBindingsConfig(config)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	common.SysLog("creative model bindings updated by user_id=" + common.Interface2String(c.GetInt("id")) + " bindings=" + common.Interface2String(len(config.Bindings)))
	state, err := service.BuildCreativeModelBindingsAdminState(config)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	common.ApiSuccess(c, state)
}

func creativeModelBindingsNoStore(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	c.Header("Pragma", "no-cache")
}

func creativeModelBindingsRequireDashboardSession(c *gin.Context) bool {
	if c.GetBool("use_access_token") {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": "This operation requires dashboard session authentication. API access token is not allowed.",
		})
		return false
	}
	return true
}

func creativeModelBindingsConfigFromRequest(c *gin.Context) (service.CreativeModelBindingsConfig, bool) {
	var payload any
	if err := common.DecodeJson(c.Request.Body, &payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid JSON"})
		return service.CreativeModelBindingsConfig{}, false
	}
	config, err := creativeModelBindingsConfigFromValue(creativeModelBindingsPayloadValue(payload))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return service.CreativeModelBindingsConfig{}, false
	}
	return config, true
}

func creativeModelBindingsPayloadValue(payload any) any {
	object, ok := payload.(map[string]any)
	if !ok {
		return payload
	}
	if config, ok := object["config"]; ok {
		return config
	}
	if bindings, ok := object["bindings"]; ok {
		return map[string]any{"version": object["version"], "bindings": bindings}
	}
	if value, ok := object["value"]; ok {
		return value
	}
	return payload
}

func creativeModelBindingsConfigFromValue(value any) (service.CreativeModelBindingsConfig, error) {
	if raw, ok := value.(string); ok {
		return service.ParseCreativeModelBindingsConfig(raw)
	}
	encoded, err := common.Marshal(value)
	if err != nil {
		return service.CreativeModelBindingsConfig{}, err
	}
	return service.ParseCreativeModelBindingsConfig(string(encoded))
}

func creativeOptionalPositiveIntQuery(c *gin.Context, key string) (int, bool) {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return 0, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": key + " must be a positive integer"})
		return 0, false
	}
	return value, true
}
