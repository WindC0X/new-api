package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func GetCreativeModelPolicy(c *gin.Context) {
	state, err := service.GetCreativeModelPolicyAdminState()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	common.ApiSuccess(c, state)
}

func UpdateCreativeModelPolicy(c *gin.Context) {
	var payload any
	if err := common.DecodeJson(c.Request.Body, &payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid JSON"})
		return
	}
	policyValue := creativeModelPolicyPayloadValue(payload)
	policy, err := service.NormalizeCreativeModelPolicyValue(policyValue)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	policy, _, err = service.UpdateStoredCreativeModelPolicy(policy)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	state, err := service.BuildCreativeModelPolicyAdminState(policy)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	common.ApiSuccess(c, state)
}

func creativeModelPolicyPayloadValue(payload any) any {
	object, ok := payload.(map[string]any)
	if !ok {
		return payload
	}
	if policy, ok := object["policy"]; ok {
		return policy
	}
	if policy, ok := object["value"]; ok {
		return policy
	}
	return payload
}

func creativeEffectiveModelPolicyForRequest(c *gin.Context, models []dto.CreativeModelCatalogItem) (service.CreativeEffectiveModelPolicy, string, error) {
	userCache, err := model.GetUserCache(c.GetInt("id"))
	if err != nil {
		return service.CreativeEffectiveModelPolicy{}, "", err
	}
	policy, err := service.GetStoredCreativeModelPolicy()
	if err != nil {
		return service.CreativeEffectiveModelPolicy{}, "", err
	}
	availableModels := make([]service.CreativeModelPolicyAvailableModel, 0, len(models))
	for _, modelItem := range models {
		availableModels = append(availableModels, service.CreativeModelPolicyAvailableModel{
			ID:                     modelItem.Id,
			SupportedEndpointTypes: modelItem.SupportedEndpointTypes,
		})
	}
	effective, version := service.BuildEffectiveCreativeModelPolicyForModels(policy, userCache.Group, availableModels)
	return effective, version, nil
}
