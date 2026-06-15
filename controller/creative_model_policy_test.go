package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCreativeBootstrapReturnsEffectiveFilteredModelPolicy(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 901)
	seedCreativeControllerModelPool(t)
	setCreativeModelPolicyOptionForTest(t, `{
		"version": 1,
		"global": {
			"defaults": {"text": "creative-model-05", "image": "creative-model-shadow"},
			"recommended": {"text": ["creative-model-05", "creative-model-shadow"]}
		},
		"groups": {
			"default": {
				"defaults": {"text": "creative-model-06"},
				"recommended": {"text": ["creative-model-06", "creative-model-shadow"]}
			}
		}
	}`)
	router := newCreativeSessionTestRouter(901)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/creative/api/bootstrap", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	requireCreativeResponseOmitsSecretFields(t, recorder.Body.String())
	payload := decodeCreativeResponse(t, recorder)
	data := creativeResponseData(t, payload)
	modelPolicy := creativeResponseObject(t, data, "modelPolicy")
	require.NotEmpty(t, data["modelPolicyVersion"])
	defaults := creativeResponseObject(t, modelPolicy, "defaults")
	require.Equal(t, "creative-model-06", defaults["text"])
	require.NotContains(t, defaults, "image")
	recommended := creativeResponseObject(t, modelPolicy, "recommended")
	require.Equal(t, []any{"creative-model-06"}, recommended["text"])
	stale := creativeResponseObject(t, modelPolicy, "stale")
	staleDefaults := creativeResponseObject(t, stale, "defaults")
	require.Equal(t, "creative-model-shadow", staleDefaults["image"])
	staleRecommended := creativeResponseObject(t, stale, "recommended")
	require.Equal(t, []any{"creative-model-shadow"}, staleRecommended["text"])
}

func TestCreativeListModelsReturnsSafeMetadataCatalog(t *testing.T) {
	setupCreativeControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Model{}, &model.Vendor{}))
	seedCreativeControllerUser(t, 902)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group:     "default",
		Model:     "custom-image-alias",
		ChannelId: 9101,
		Enabled:   true,
	}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group:     "default",
		Model:     "leaky-metadata-model",
		ChannelId: 9102,
		Enabled:   true,
	}).Error)
	require.NoError(t, model.DB.Create(&model.Vendor{
		Id:     77,
		Name:   "Safe Vendor",
		Status: 1,
	}).Error)
	require.NoError(t, model.DB.Create(&model.Vendor{
		Id:     78,
		Name:   "api key vendor token",
		Status: 1,
	}).Error)
	require.NoError(t, model.DB.Create(&model.Model{
		ModelName:   "custom-image-alias",
		Description: "A safe image model alias",
		Tags:        "image, custom, apiKey",
		VendorID:    77,
		Status:      1,
		Endpoints:   `{"image-generation":"/v1/images/generations"}`,
	}).Error)
	require.NoError(t, model.DB.Create(&model.Model{
		ModelName:   "leaky-metadata-model",
		Description: "Display label mentions sk-hidden-token but must not ship",
		Tags:        "image, safe-tag, api-key, api key, base-url, base url, selected-key, channel-id, owner-id, token, password, sk-inline-secret",
		VendorID:    78,
		Status:      1,
		Endpoints:   `{"image-generation":"/v1/images/generations"}`,
	}).Error)
	model.InvalidatePricingCache()
	t.Cleanup(model.InvalidatePricingCache)

	router := newCreativeSessionTestRouter(902)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/creative/api/models", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	requireCreativeResponseOmitsSecretFields(t, recorder.Body.String())
	raw := recorder.Body.String()
	for _, forbidden := range []string{
		"channel_id",
		"channelId",
		"api_key",
		"apiKey",
		"base_url",
		"baseUrl",
		"selected_key",
		"selectedKey",
		"owner_id",
		"ownerId",
	} {
		require.NotContains(t, raw, `"`+forbidden+`"`)
	}
	payload := decodeCreativeResponse(t, recorder)
	data := creativeResponseArray(t, payload, "data")
	require.Len(t, data, 2)
	itemsByID := map[string]map[string]any{}
	for _, rawItem := range data {
		item, ok := rawItem.(map[string]any)
		require.True(t, ok)
		id, ok := item["id"].(string)
		require.True(t, ok)
		itemsByID[id] = item
	}
	item := itemsByID["custom-image-alias"]
	require.NotNil(t, item)
	require.Equal(t, "custom-image-alias", item["id"])
	require.Equal(t, "custom-image-alias", item["label"])
	require.Equal(t, "cia", item["shortCode"])
	require.Equal(t, "image", item["type"])
	require.Equal(t, "image", item["modality"])
	require.Equal(t, "Safe Vendor", item["vendor"])
	require.Equal(t, []any{"image", "custom"}, item["tags"])
	require.Equal(t, "A safe image model alias", item["description"])
	leakyItem := itemsByID["leaky-metadata-model"]
	require.NotNil(t, leakyItem)
	require.Equal(t, "leaky-metadata-model", leakyItem["id"])
	require.NotContains(t, recorder.Body.String(), "api key vendor token")
	require.NotContains(t, recorder.Body.String(), "sk-hidden-token")
	require.NotContains(t, recorder.Body.String(), "sk-inline-secret")
	require.Equal(t, []any{"image", "safe-tag"}, leakyItem["tags"])
	require.NotContains(t, leakyItem, "description")
	require.NotEqual(t, "api key vendor token", leakyItem["vendor"])
	require.NotEmpty(t, payload["catalogVersion"])
}

func TestCreativeModelPolicyAdminEndpointsNormalizeAndDiagnose(t *testing.T) {
	setupCreativeControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Option{}))
	seedCreativeControllerModelPool(t)
	setCreativeModelPolicyOptionForTest(t, "")
	router := newCreativeModelPolicyAdminTestRouter()

	putBody := map[string]any{
		"policy": map[string]any{
			"version": float64(1),
			"global": map[string]any{
				"defaults":    map[string]any{"text": " creative-model-02 "},
				"recommended": map[string]any{"text": []any{"creative-model-02", "creative-model-02", "creative-model-shadow"}},
			},
			"groups": map[string]any{
				"default": map[string]any{
					"defaults": map[string]any{"image": "creative-model-shadow"},
				},
			},
		},
	}
	put := performJSONRequest(t, router, http.MethodPut, "/api/creative/model-policy", putBody)
	require.Equal(t, http.StatusOK, put.Code)
	putPayload := decodeCreativeResponse(t, put)
	require.Equal(t, true, putPayload["success"])
	putData := creativeResponseData(t, putPayload)
	policy := creativeResponseObject(t, putData, "policy")
	global := creativeResponseObject(t, policy, "global")
	defaults := creativeResponseObject(t, global, "defaults")
	require.Equal(t, "creative-model-02", defaults["text"])
	recommended := creativeResponseObject(t, global, "recommended")
	require.Equal(t, []any{"creative-model-02", "creative-model-shadow"}, recommended["text"])
	modelPools := creativeResponseArray(t, putData, "modelPools")
	require.NotEmpty(t, modelPools)
	require.NotContains(t, put.Body.String(), "channelId")
	require.NotContains(t, put.Body.String(), "apiKey")
	require.NotContains(t, put.Body.String(), "baseUrl")
	require.NotContains(t, put.Body.String(), "provider")

	get := performJSONRequest(t, router, http.MethodGet, "/api/creative/model-policy", nil)
	require.Equal(t, http.StatusOK, get.Code)
	getData := creativeResponseData(t, decodeCreativeResponse(t, get))
	require.NotEmpty(t, getData["policyJSON"])
	require.NotEmpty(t, getData["cleanedPolicyJSON"])
	require.NotContains(t, get.Body.String(), "channelId")
	require.NotContains(t, get.Body.String(), "apiKey")
	require.NotContains(t, get.Body.String(), "baseUrl")
	require.NotContains(t, get.Body.String(), "provider")
}

func TestCreativeModelPolicyAdminPutRejectsUnsafeFields(t *testing.T) {
	setupCreativeControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Option{}))
	setCreativeModelPolicyOptionForTest(t, "")
	router := newCreativeModelPolicyAdminTestRouter()

	recorder := performJSONRequest(t, router, http.MethodPut, "/api/creative/model-policy", map[string]any{
		"policy": map[string]any{
			"global": map[string]any{
				"defaults": map[string]any{"text": "gpt-4o"},
				"apiKey":   "sk-test-secret",
			},
		},
	})

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	payload := decodeCreativeResponse(t, recorder)
	require.Equal(t, false, payload["success"])
	require.Contains(t, payload["message"], "forbidden field")
}

func newCreativeModelPolicyAdminTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/creative/model-policy", GetCreativeModelPolicy)
	router.PUT("/api/creative/model-policy", UpdateCreativeModelPolicy)
	return router
}

func setCreativeModelPolicyOptionForTest(t *testing.T, value string) {
	t.Helper()

	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = map[string]string{}
	}
	previous, hadPrevious := common.OptionMap[service.CreativeModelPolicyOptionKey]
	common.OptionMap[service.CreativeModelPolicyOptionKey] = value
	common.OptionMapRWMutex.Unlock()

	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		defer common.OptionMapRWMutex.Unlock()
		if hadPrevious {
			common.OptionMap[service.CreativeModelPolicyOptionKey] = previous
		} else {
			delete(common.OptionMap, service.CreativeModelPolicyOptionKey)
		}
	})
}

func performJSONRequest(t *testing.T, router *gin.Engine, method string, target string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		encoded, err := common.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, target, reader)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}
