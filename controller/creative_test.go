package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type creativeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f creativeRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestContainsCreativeForbiddenFieldDetectsNestedSecrets(t *testing.T) {
	nestedSecretPayload := func(key string, value any) map[string]any {
		return map[string]any{
			"messages": []any{
				map[string]any{"role": "user", "content": "hello"},
			},
			"metadata": map[string]any{
				"auditTrail": []any{
					map[string]any{
						"details": map[string]any{
							key: value,
						},
					},
				},
			},
		}
	}
	nestedSecretPath := func(key string) string {
		return "metadata.auditTrail[0].details." + key
	}

	tests := []struct {
		name      string
		payload   map[string]any
		wantField string
	}{
		{
			name: "api key in nested object",
			payload: map[string]any{
				"messages": []any{
					map[string]any{"role": "user", "content": "hello"},
				},
				"metadata": map[string]any{
					"nested": map[string]any{
						"apiKey": "secret",
					},
				},
			},
			wantField: "metadata.nested.apiKey",
		},
		{
			name: "authorization in nested array object",
			payload: map[string]any{
				"messages": []any{
					map[string]any{"role": "user", "content": "hello"},
					map[string]any{
						"role": "assistant",
						"tool_calls": []any{
							map[string]any{
								"headers": map[string]any{
									"Authorization": "Bearer secret",
								},
							},
						},
					},
				},
			},
			wantField: "messages[1].tool_calls[0].headers.Authorization",
		},
		{
			name:      "camel case access token in nested array object",
			payload:   nestedSecretPayload("accessToken", "secret"),
			wantField: nestedSecretPath("accessToken"),
		},
		{
			name:      "camel case refresh token in nested array object",
			payload:   nestedSecretPayload("refreshToken", "secret"),
			wantField: nestedSecretPath("refreshToken"),
		},
		{
			name:      "camel case id token in nested array object",
			payload:   nestedSecretPayload("idToken", "secret"),
			wantField: nestedSecretPath("idToken"),
		},
		{
			name:      "camel case internal token in nested array object",
			payload:   nestedSecretPayload("internalToken", "secret"),
			wantField: nestedSecretPath("internalToken"),
		},
		{
			name:      "camel case channel id in nested array object",
			payload:   nestedSecretPayload("channelId", 7),
			wantField: nestedSecretPath("channelId"),
		},
		{
			name:      "snake case channel id in nested array object",
			payload:   nestedSecretPayload("channel_id", 7),
			wantField: nestedSecretPath("channel_id"),
		},
		{
			name:      "channel override in nested array object",
			payload:   nestedSecretPayload("channelOverride", "vip-channel"),
			wantField: nestedSecretPath("channelOverride"),
		},
		{
			name:      "snake case channel override in nested array object",
			payload:   nestedSecretPayload("channel_override", "vip-channel"),
			wantField: nestedSecretPath("channel_override"),
		},
		{
			name:      "snake case model name in nested array object",
			payload:   nestedSecretPayload("model_name", "expensive-upstream-model"),
			wantField: nestedSecretPath("model_name"),
		},
		{
			name:      "camel case model name in nested array object",
			payload:   nestedSecretPayload("modelName", "expensive-upstream-model"),
			wantField: nestedSecretPath("modelName"),
		},
		{
			name:      "provider request key in nested array object",
			payload:   nestedSecretPayload("req_key", "jimeng_expensive_model"),
			wantField: nestedSecretPath("req_key"),
		},
		{
			name:      "provider override in nested array object",
			payload:   nestedSecretPayload("providerOverride", "openai"),
			wantField: nestedSecretPath("providerOverride"),
		},
		{
			name:      "snake case provider override in nested array object",
			payload:   nestedSecretPayload("provider_override", "openai"),
			wantField: nestedSecretPath("provider_override"),
		},
		{
			name:      "provider id in nested array object",
			payload:   nestedSecretPayload("providerId", "openai"),
			wantField: nestedSecretPath("providerId"),
		},
		{
			name:      "snake case provider id in nested array object",
			payload:   nestedSecretPayload("provider_id", "openai"),
			wantField: nestedSecretPath("provider_id"),
		},
		{
			name:      "base url camel case in nested array object",
			payload:   nestedSecretPayload("baseUrl", "https://upstream.example"),
			wantField: nestedSecretPath("baseUrl"),
		},
		{
			name:      "base URL acronym in nested array object",
			payload:   nestedSecretPayload("baseURL", "https://upstream.example"),
			wantField: nestedSecretPath("baseURL"),
		},
		{
			name:      "upstream key camel case in nested array object",
			payload:   nestedSecretPayload("upstreamKey", "secret"),
			wantField: nestedSecretPath("upstreamKey"),
		},
		{
			name:      "upstream key snake case in nested array object",
			payload:   nestedSecretPayload("upstream_key", "secret"),
			wantField: nestedSecretPath("upstream_key"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			field, forbidden := containsCreativeForbiddenField(tt.payload)

			require.True(t, forbidden)
			require.Equal(t, tt.wantField, field)
		})
	}
}

func TestContainsCreativeForbiddenFieldRejectsRelayProviderOverride(t *testing.T) {
	field, forbidden := containsCreativeForbiddenField(map[string]any{
		"model":    "gpt-4o",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
		"provider": "openai",
	})

	require.True(t, forbidden)
	require.Equal(t, "provider", field)
}

func TestContainsCreativeForbiddenFieldAllowsNormalChatPayload(t *testing.T) {
	field, forbidden := containsCreativeForbiddenField(map[string]any{
		"model": "gpt-4o",
		"messages": []any{
			map[string]any{"role": "system", "content": []any{
				map[string]any{"type": "text", "text": "You are helpful."},
			}},
			map[string]any{"role": "user", "content": "hello"},
		},
		"max_tokens": 64,
		"metadata": map[string]any{
			"requestId": "req-123",
			"client": map[string]any{
				"name": "creative-ui",
			},
		},
		"stream": true,
		"stream_options": map[string]any{
			"include_usage": true,
		},
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "lookup",
					"description": "Lookup public information",
					"parameters": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"query": map[string]any{"type": "string"},
						},
					},
				},
			},
		},
	})

	require.False(t, forbidden)
	require.Empty(t, field)
}

func TestContainsCreativeForbiddenFieldRejectsSecretLookingStringValues(t *testing.T) {
	tests := []struct {
		name      string
		payload   map[string]any
		wantField string
	}{
		{
			name: "OpenAI-style key nested in document snapshot text",
			payload: map[string]any{
				"snapshot": map[string]any{
					"nodes": []any{
						map[string]any{"text": "sk-test-abcdefghijklmnopqrstuvwxyz1234567890"},
					},
				},
			},
			wantField: "snapshot.nodes[0].text",
		},
		{
			name: "Bearer token nested in document metadata value",
			payload: map[string]any{
				"metadata": map[string]any{
					"publicHeaders": []any{
						"Authorization: Bearer abcdefghijklmnopqrstuvwxyz1234567890",
					},
				},
			},
			wantField: "metadata.publicHeaders[0]",
		},
		{
			name: "JWT-looking bearer token nested in relay metadata value",
			payload: map[string]any{
				"metadata": map[string]any{
					"trace": map[string]any{
						"note": "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payload.signature",
					},
				},
			},
			wantField: "metadata.trace.note",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			field, forbidden := containsCreativeForbiddenField(tt.payload)

			require.True(t, forbidden)
			require.Equal(t, tt.wantField, field)
		})
	}
}

func TestContainsCreativeForbiddenFieldAllowsPublicAuthorizationText(t *testing.T) {
	field, forbidden := containsCreativeForbiddenField(map[string]any{
		"snapshot": map[string]any{
			"nodes": []any{
				map[string]any{
					"text": "This public note mentions Authorization headers but contains no credential.",
				},
			},
		},
	})

	require.False(t, forbidden)
	require.Empty(t, field)
}

func TestCreativeListModelsReturnsCompleteSessionUserCallablePool(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 33)
	expectedModelIds := seedCreativeControllerModelPool(t)
	router := newCreativeSessionTestRouter(33)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/creative/api/models", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	payload := decodeCreativeResponse(t, recorder)
	require.Equal(t, true, payload["success"])
	require.Equal(t, "list", payload["object"])
	catalogVersion, ok := payload["catalogVersion"].(string)
	require.True(t, ok)
	require.NotEmpty(t, catalogVersion)

	models, ok := payload["data"].([]any)
	require.True(t, ok)
	require.Len(t, models, 30)
	seen := make(map[string]struct{}, len(models))
	for _, rawModel := range models {
		modelObject, ok := rawModel.(map[string]any)
		require.True(t, ok)
		modelId, ok := modelObject["id"].(string)
		require.True(t, ok)
		require.NotEmpty(t, modelId)
		require.NotContains(t, seen, modelId)
		seen[modelId] = struct{}{}
	}
	require.Len(t, seen, 30)
	for _, expectedModelId := range expectedModelIds {
		require.Contains(t, seen, expectedModelId)
	}

	requireCreativeResponseOmitsPolicyFields(t, recorder.Body.String())
	requireCreativeResponseOmitsSecretFields(t, recorder.Body.String())
}

func TestCreativeListModelsIncludesMockPreviewBindingOnlyForEnabledCanary(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 35)
	seedCreativeControllerModelPool(t)
	withCreativeControllerOptions(t, map[string]string{
		service.CreativeAdapterEnabledOptionKey:      "true",
		service.CreativeAdapterCanaryGroupsOptionKey: "default",
	})
	router := newCreativeSessionTestRouter(35)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/creative/api/models", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	payload := decodeCreativeResponse(t, recorder)
	models, ok := payload["data"].([]any)
	require.True(t, ok)
	require.Len(t, models, 31)

	var mockBinding map[string]any
	for _, rawModel := range models {
		modelObject, ok := rawModel.(map[string]any)
		require.True(t, ok)
		if modelObject["id"] == "mock:gpt-image-2:preview" {
			mockBinding = modelObject
			break
		}
	}
	require.NotNil(t, mockBinding)
	require.Equal(t, "gpt-image-2", mockBinding["providerModelId"])
	require.Equal(t, "mock-gpt-image-2-price", mockBinding["priceModelId"])
	require.NotEqual(t, mockBinding["id"], mockBinding["providerModelId"])
	require.NotEqual(t, mockBinding["providerModelId"], mockBinding["priceModelId"])
	schema, ok := mockBinding["parameterSchema"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, schema)

	requireCreativeResponseOmitsSecretFields(t, recorder.Body.String())
}

func TestCreativeListModelsIncludesStoredEnabledBindingsAndDedupesPreview(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 3501)
	seedCreativeControllerModelPool(t)
	withCreativeImageTaskMockBinding(t, true, []string{"default"})
	router := newCreativeSessionTestRouter(3501)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/creative/api/models", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	payload := decodeCreativeResponse(t, recorder)
	models, ok := payload["data"].([]any)
	require.True(t, ok)
	require.Len(t, models, 31)
	count := 0
	for _, rawModel := range models {
		modelObject, ok := rawModel.(map[string]any)
		require.True(t, ok)
		if modelObject["id"] != "mock:gpt-image-2:preview" {
			continue
		}
		count++
		require.Equal(t, "gpt-image-2", modelObject["providerModelId"])
		require.Equal(t, "mock-gpt-image-2-price", modelObject["priceModelId"])
		schema, ok := modelObject["parameterSchema"].([]any)
		require.True(t, ok)
		require.NotEmpty(t, schema)
	}
	require.Equal(t, 1, count)

	withCreativeControllerOptions(t, map[string]string{
		service.CreativeAdapterCanaryGroupsOptionKey: "default",
	})
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/creative/api/models", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	models = decodeCreativeResponse(t, recorder)["data"].([]any)
	require.Len(t, models, 31)
	count = 0
	for _, rawModel := range models {
		modelObject := rawModel.(map[string]any)
		if modelObject["id"] == "mock:gpt-image-2:preview" {
			count++
		}
	}
	require.Equal(t, 1, count)
}

func TestCreativeListModelsDoesNotIncludeMockPreviewBindingWhenCanaryMisses(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 36)
	seedCreativeControllerModelPool(t)
	withCreativeControllerOptions(t, map[string]string{
		service.CreativeAdapterEnabledOptionKey:      "true",
		service.CreativeAdapterCanaryGroupsOptionKey: "vip",
	})
	router := newCreativeSessionTestRouter(36)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/creative/api/models", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	payload := decodeCreativeResponse(t, recorder)
	models, ok := payload["data"].([]any)
	require.True(t, ok)
	require.Len(t, models, 30)
	for _, rawModel := range models {
		modelObject, ok := rawModel.(map[string]any)
		require.True(t, ok)
		require.NotEqual(t, "mock:gpt-image-2:preview", modelObject["id"])
	}
}

func TestCreativeBootstrapAndModelsDoNotReturnUIDisplayPolicyFields(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 34)
	seedCreativeControllerModelPool(t)
	require.NoError(t, model.DB.Create(&model.CreativeModelPreference{
		UserId: 34,
		PreferenceJSON: `{
			"default":{"text":{"modelId":"creative-model-05","providerIdHint":"safe-display-hint"}},
			"pinned":["creative-model-05"],
			"defaultModel":"creative-model-shadow",
			"defaultVisibleModels":["creative-model-shadow"],
			"order":["creative-model-shadow"],
			"group":"shadow",
			"uiPolicy":{"defaultModel":"creative-model-shadow"},
			"displayPolicy":{"group":"shadow"}
		}`,
		Revision: 9,
	}).Error)
	router := newCreativeSessionTestRouter(34)

	bootstrap := httptest.NewRecorder()
	router.ServeHTTP(bootstrap, httptest.NewRequest(http.MethodGet, "/creative/api/bootstrap", nil))
	require.Equal(t, http.StatusOK, bootstrap.Code)
	requireCreativeResponseOmitsPolicyFields(t, bootstrap.Body.String())
	bootstrapPayload := decodeCreativeResponse(t, bootstrap)
	bootstrapData := creativeResponseData(t, bootstrapPayload)
	modelPreference := creativeResponseObject(t, bootstrapData, "modelPreference")
	require.Equal(t, float64(9), modelPreference["revision"])
	preference := creativeResponseObject(t, modelPreference, "preference")
	defaultPreference := creativeResponseObject(t, preference, "default")
	textDefault := creativeResponseObject(t, defaultPreference, "text")
	require.Equal(t, "creative-model-05", textDefault["modelId"])
	require.Equal(t, "safe-display-hint", textDefault["providerIdHint"])

	models := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/creative/api/models", nil)
	for _, sessionCookie := range bootstrap.Result().Cookies() {
		request.AddCookie(sessionCookie)
	}
	router.ServeHTTP(models, request)
	require.Equal(t, http.StatusOK, models.Code)
	requireCreativeResponseOmitsPolicyFields(t, models.Body.String())
}

func TestCreativeBootstrapIssuesSessionAuthMaterial(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 31)
	router := newCreativeSessionTestRouter(31)

	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/creative/api/bootstrap", nil))
	require.Equal(t, http.StatusOK, first.Code)
	firstPayload := decodeCreativeResponse(t, first)
	firstData := creativeResponseData(t, firstPayload)
	firstAuth := creativeResponseObject(t, firstData, "auth")
	require.Equal(t, "session-broker", firstAuth["mode"])
	firstCSRF, ok := firstAuth["csrfToken"].(string)
	require.True(t, ok)
	require.NotEmpty(t, firstCSRF)
	firstNonce, ok := firstAuth["nonce"].(string)
	require.True(t, ok)
	require.NotEmpty(t, firstNonce)
	require.NotEqual(t, firstCSRF, firstNonce)
	require.NotContains(t, firstAuth, "apiKey")
	require.NotContains(t, firstAuth, "token")
	require.NotContains(t, firstAuth, "baseUrl")
	require.NotContains(t, firstAuth, "provider")
	require.Equal(t, creativeBrokerBaseURL, creativeResponseObject(t, firstData, "profile")["brokerBaseUrl"])

	second := httptest.NewRecorder()
	secondRequest := httptest.NewRequest(http.MethodGet, "/creative/api/bootstrap", nil)
	for _, sessionCookie := range first.Result().Cookies() {
		secondRequest.AddCookie(sessionCookie)
	}
	router.ServeHTTP(second, secondRequest)
	require.Equal(t, http.StatusOK, second.Code)
	secondAuth := creativeResponseObject(t, creativeResponseData(t, decodeCreativeResponse(t, second)), "auth")
	require.Equal(t, firstCSRF, secondAuth["csrfToken"])
	require.Equal(t, firstNonce, secondAuth["nonce"])
}

func TestCreativeVideoRelayEnvDefaultIsFailClosed(t *testing.T) {
	t.Setenv("CREATIVE_VIDEO_RELAY_ENABLED", "")

	require.False(t, loadCreativeVideoRelayEnabledFromEnv())

	t.Setenv("CREATIVE_VIDEO_RELAY_ENABLED", "true")
	require.True(t, loadCreativeVideoRelayEnabledFromEnv())
}

func TestCreativeBootstrapReportsVideoRelayCapability(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 36)
	seedCreativeControllerModelPool(t)
	router := newCreativeSessionTestRouter(36)

	restoreDisabled := SetCreativeVideoRelayEnabledForTest(false)
	disabled := httptest.NewRecorder()
	router.ServeHTTP(disabled, httptest.NewRequest(http.MethodGet, "/creative/api/bootstrap", nil))
	restoreDisabled()
	require.Equal(t, http.StatusOK, disabled.Code)
	disabledCapabilities := creativeResponseObject(t, creativeResponseData(t, decodeCreativeResponse(t, disabled)), "capabilities")
	require.Equal(t, false, disabledCapabilities["videoRelayEnabled"])

	restoreEnabled := SetCreativeVideoRelayEnabledForTest(true)
	enabled := httptest.NewRecorder()
	router.ServeHTTP(enabled, httptest.NewRequest(http.MethodGet, "/creative/api/bootstrap", nil))
	restoreEnabled()
	require.Equal(t, http.StatusOK, enabled.Code)
	enabledCapabilities := creativeResponseObject(t, creativeResponseData(t, decodeCreativeResponse(t, enabled)), "capabilities")
	require.Equal(t, true, enabledCapabilities["videoRelayEnabled"])
}

func TestCreativeNonceMiddlewareRequiresSameOriginSignalForUnsafeMethods(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 42)
	seedCreativeControllerModelPool(t)
	router := newCreativeSessionTestRouter(42)
	unsafeMethods := []string{http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete}
	for _, method := range unsafeMethods {
		method := method
		router.Handle(method, "/creative/api/origin-check", middleware.CreativeRequireNonce(), func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"method": method})
		})
	}
	auth := bootstrapCreativeSessionAuth(t, router)
	body := map[string]any{"ok": true}

	for _, method := range unsafeMethods {
		t.Run(method, func(t *testing.T) {
			missingOriginSignal := performCreativeSessionJSON(t, router, method, "/creative/api/origin-check", body, auth.cookies, creativeNonceHeaders(auth))
			require.Equal(t, http.StatusForbidden, missingOriginSignal.Code)
			require.Equal(t, "creative request origin is invalid", decodeCreativeResponse(t, missingOriginSignal)["message"])

			sameOrigin := performCreativeSessionJSON(t, router, method, "/creative/api/origin-check", body, auth.cookies, creativeSameOriginNonceHeaders(auth))
			require.Equal(t, http.StatusOK, sameOrigin.Code)

			sameRefererHeaders := creativeNonceHeaders(auth)
			sameRefererHeaders["Referer"] = "http://example.com/creative/api/origin-check?from=creative"
			sameReferer := performCreativeSessionJSON(t, router, method, "/creative/api/origin-check", body, auth.cookies, sameRefererHeaders)
			require.Equal(t, http.StatusOK, sameReferer.Code)

			crossOriginHeaders := creativeNonceHeaders(auth)
			crossOriginHeaders["Origin"] = "https://evil.example"
			crossOrigin := performCreativeSessionJSON(t, router, method, "/creative/api/origin-check", body, auth.cookies, crossOriginHeaders)
			require.Equal(t, http.StatusForbidden, crossOrigin.Code)
			require.Equal(t, "creative request origin is invalid", decodeCreativeResponse(t, crossOrigin)["message"])

			crossRefererHeaders := creativeNonceHeaders(auth)
			crossRefererHeaders["Referer"] = "https://evil.example/creative/api/origin-check"
			crossReferer := performCreativeSessionJSON(t, router, method, "/creative/api/origin-check", body, auth.cookies, crossRefererHeaders)
			require.Equal(t, http.StatusForbidden, crossReferer.Code)
			require.Equal(t, "creative request origin is invalid", decodeCreativeResponse(t, crossReferer)["message"])
		})
	}
}

func TestCreativeGetBootstrapModelsAndDocumentsDoNotRequireOriginSignal(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 35)
	seedCreativeControllerModelPool(t)
	_, err := model.CreateCreativeDocument(&model.CreativeDocument{
		UserId:       35,
		DocumentId:   "doc-get-origin-free",
		Title:        "Origin-free GET",
		SnapshotJSON: "{}",
		MetadataJSON: "{}",
	})
	require.NoError(t, err)
	router := newCreativeSessionTestRouter(35)
	router.GET("/creative/api/documents", func(c *gin.Context) {
		c.Set("id", 35)
		CreativeListDocuments(c)
	})

	auth := bootstrapCreativeSessionAuth(t, router)

	models := httptest.NewRecorder()
	modelsRequest := httptest.NewRequest(http.MethodGet, "/creative/api/models", nil)
	for _, sessionCookie := range auth.cookies {
		modelsRequest.AddCookie(sessionCookie)
	}
	router.ServeHTTP(models, modelsRequest)
	require.Equal(t, http.StatusOK, models.Code)

	documents := httptest.NewRecorder()
	documentsRequest := httptest.NewRequest(http.MethodGet, "/creative/api/documents", nil)
	for _, sessionCookie := range auth.cookies {
		documentsRequest.AddCookie(sessionCookie)
	}
	router.ServeHTTP(documents, documentsRequest)
	require.Equal(t, http.StatusOK, documents.Code)
	documentsPayload := decodeCreativeResponse(t, documents)
	require.Equal(t, true, documentsPayload["success"])
	require.Len(t, creativeResponseArray(t, creativeResponseData(t, documentsPayload), "documents"), 1)
}

func TestCreativeNonceMiddlewareProtectsUnsafePreferenceMutation(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 41)
	seedCreativeControllerModelPool(t)
	router := newCreativeSessionTestRouter(41)
	router.PATCH("/creative/api/preferences/model", middleware.CreativeRequireNonce(), CreativePatchModelPreference)
	auth := bootstrapCreativeSessionAuth(t, router)
	body := map[string]any{
		"baseRevision": 0,
		"preference": map[string]any{
			"default": map[string]any{
				"text": map[string]any{"modelId": "gpt-4o", "vendorHint": "openai"},
			},
		},
	}

	missing := performCreativeSessionJSON(t, router, http.MethodPatch, "/creative/api/preferences/model", body, auth.cookies, nil)
	require.Equal(t, http.StatusForbidden, missing.Code)

	wrong := performCreativeSessionJSON(t, router, http.MethodPatch, "/creative/api/preferences/model", body, auth.cookies, map[string]string{
		"X-Creative-CSRF":  auth.csrfToken,
		"X-Creative-Nonce": "wrong-nonce",
	})
	require.Equal(t, http.StatusForbidden, wrong.Code)

	crossOrigin := performCreativeSessionJSON(t, router, http.MethodPatch, "/creative/api/preferences/model", body, auth.cookies, map[string]string{
		"Origin":           "https://evil.example",
		"X-Creative-CSRF":  auth.csrfToken,
		"X-Creative-Nonce": auth.nonce,
	})
	require.Equal(t, http.StatusForbidden, crossOrigin.Code)
	require.Equal(t, "creative request origin is invalid", decodeCreativeResponse(t, crossOrigin)["message"])

	crossReferer := performCreativeSessionJSON(t, router, http.MethodPatch, "/creative/api/preferences/model", body, auth.cookies, map[string]string{
		"Referer":          "https://evil.example/creative/api/preferences/model",
		"X-Creative-CSRF":  auth.csrfToken,
		"X-Creative-Nonce": auth.nonce,
	})
	require.Equal(t, http.StatusForbidden, crossReferer.Code)
	require.Equal(t, "creative request origin is invalid", decodeCreativeResponse(t, crossReferer)["message"])

	correct := performCreativeSessionJSON(t, router, http.MethodPatch, "/creative/api/preferences/model", body, auth.cookies, creativeSameOriginNonceHeaders(auth))
	require.Equal(t, http.StatusOK, correct.Code)
	payload := decodeCreativeResponse(t, correct)
	require.Equal(t, true, payload["success"])
	require.Equal(t, float64(1), creativeResponseData(t, payload)["revision"])

	body["baseRevision"] = 1
	matchingOrigin := performCreativeSessionJSON(t, router, http.MethodPatch, "/creative/api/preferences/model", body, auth.cookies, map[string]string{
		"Origin":           "http://example.com",
		"X-Creative-CSRF":  auth.csrfToken,
		"X-Creative-Nonce": auth.nonce,
	})
	require.Equal(t, http.StatusOK, matchingOrigin.Code)
	matchingOriginPayload := decodeCreativeResponse(t, matchingOrigin)
	require.Equal(t, true, matchingOriginPayload["success"])
	require.Equal(t, float64(2), creativeResponseData(t, matchingOriginPayload)["revision"])
}

func TestCreativeNonceMiddlewareProtectsRelayPost(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 43)
	seedCreativeControllerModelPool(t)
	router := newCreativeSessionTestRouter(43)
	router.POST("/creative/relay/v1/chat/completions", middleware.CreativeRequireNonce(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
	auth := bootstrapCreativeSessionAuth(t, router)
	body := map[string]any{
		"model":    "gpt-4o",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	}

	missing := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/chat/completions", body, auth.cookies, nil)
	require.Equal(t, http.StatusForbidden, missing.Code)

	wrong := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/chat/completions", body, auth.cookies, map[string]string{
		"X-Creative-CSRF":  "wrong-csrf",
		"X-Creative-Nonce": auth.nonce,
	})
	require.Equal(t, http.StatusForbidden, wrong.Code)

	correct := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/chat/completions", body, auth.cookies, creativeSameOriginNonceHeaders(auth))
	require.Equal(t, http.StatusOK, correct.Code)
}

func TestCreativeRelaySessionBrokerSelectsCallableGroupFromFullUserPool(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 51)
	seedCreativeControllerModelPool(t)

	router := newCreativeRelayBrokerTestRouter(t, 51, func(c *gin.Context) {
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		var payload map[string]any
		if err := common.Unmarshal(bodyBytes, &payload); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"bodyModel":  payload["model"],
			"tokenGroup": common.GetContextKeyString(c, constant.ContextKeyTokenGroup),
			"usingGroup": common.GetContextKeyString(c, constant.ContextKeyUsingGroup),
		})
	})
	auth := bootstrapCreativeSessionAuth(t, router)

	defaultGroup := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/chat/completions", map[string]any{
		"model":    "creative-model-05",
		"messages": []any{map[string]any{"role": "user", "content": "hello default"}},
	}, auth.cookies, creativeSameOriginNonceHeaders(auth))
	require.Equal(t, http.StatusOK, defaultGroup.Code)
	defaultPayload := decodeCreativeResponse(t, defaultGroup)
	require.Equal(t, "creative-model-05", defaultPayload["bodyModel"])
	require.Equal(t, "default", defaultPayload["usingGroup"])
	require.Equal(t, "default", defaultPayload["tokenGroup"])

	vipGroup := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/chat/completions", map[string]any{
		"model":    "creative-model-25",
		"messages": []any{map[string]any{"role": "user", "content": "hello vip"}},
	}, auth.cookies, creativeSameOriginNonceHeaders(auth))
	require.Equal(t, http.StatusOK, vipGroup.Code)
	vipPayload := decodeCreativeResponse(t, vipGroup)
	require.Equal(t, "creative-model-25", vipPayload["bodyModel"])
	require.Equal(t, "vip", vipPayload["usingGroup"])
	require.Equal(t, "vip", vipPayload["tokenGroup"])
}

func TestCreativeRelaySessionBrokerRejectsUnavailableModelBeforeRelay(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 52)
	seedCreativeControllerModelPool(t)

	relayReached := false
	router := newCreativeRelayBrokerTestRouter(t, 52, func(c *gin.Context) {
		relayReached = true
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
	auth := bootstrapCreativeSessionAuth(t, router)

	recorder := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/chat/completions", map[string]any{
		"model":    "creative-model-shadow",
		"messages": []any{map[string]any{"role": "user", "content": "shadow"}},
	}, auth.cookies, creativeSameOriginNonceHeaders(auth))

	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.False(t, relayReached)
	errorObject := creativeResponseObject(t, decodeCreativeResponse(t, recorder), "error")
	require.Equal(t, "access_denied", errorObject["type"])
	require.Equal(t, "model", errorObject["param"])
	require.Contains(t, errorObject["message"], "model is not available")
}

func TestCreativeRelaySessionBrokerRejectsWrongModalityBeforeRelay(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 5201)
	seedCreativeControllerSunoModelPool(t)

	relayReached := false
	router := newCreativeRelayBrokerTestRouter(t, 5201, func(c *gin.Context) {
		relayReached = true
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
	auth := bootstrapCreativeSessionAuth(t, router)

	recorder := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/generations", map[string]any{
		"model":  "suno_lyrics",
		"prompt": "draw this with an audio-only model",
	}, auth.cookies, creativeSameOriginNonceHeaders(auth))

	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.False(t, relayReached)
	errorObject := creativeResponseObject(t, decodeCreativeResponse(t, recorder), "error")
	require.Equal(t, "access_denied", errorObject["type"])
	require.Equal(t, "model", errorObject["param"])
	require.Contains(t, errorObject["message"], "creative endpoint")
}

func TestCreativeRelaySessionBrokerAcceptsBrowserSessionWithoutAPIKey(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 54)
	seedCreativeControllerModelPool(t)

	router := newCreativeRelayBrokerTestRouter(t, 54, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"authorization":  c.GetHeader("Authorization"),
			"useAccessToken": c.GetBool("use_access_token"),
			"usingGroup":     common.GetContextKeyString(c, constant.ContextKeyUsingGroup),
			"tokenGroup":     common.GetContextKeyString(c, constant.ContextKeyTokenGroup),
		})
	})
	auth := bootstrapCreativeSessionAuth(t, router)

	recorder := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/chat/completions", map[string]any{
		"model":    "creative-model-05",
		"messages": []any{map[string]any{"role": "user", "content": "browser session only"}},
	}, auth.cookies, creativeSameOriginNonceHeaders(auth))

	require.Equal(t, http.StatusOK, recorder.Code)
	payload := decodeCreativeResponse(t, recorder)
	require.Empty(t, payload["authorization"])
	require.Equal(t, false, payload["useAccessToken"])
	require.Equal(t, "default", payload["usingGroup"])
	require.Equal(t, "default", payload["tokenGroup"])
	require.NotContains(t, recorder.Body.String(), "apiKey")
	require.NotContains(t, recorder.Body.String(), "sk-")
}

func TestCreativeRelayChatCompletionsUsesTemporarySessionTokenContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/creative/relay/v1/chat/completions", strings.NewReader(`{"model":123}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("id", 55)
	ctx.Set("group", "default")
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "vip")
	common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "vip")

	CreativeRelayChatCompletions(ctx)

	require.GreaterOrEqual(t, recorder.Code, http.StatusBadRequest)
	require.Equal(t, 0, ctx.GetInt("token_id"))
	require.Empty(t, ctx.GetString("token_key"))
	require.Equal(t, "creative-session-broker-vip", ctx.GetString("token_name"))
	require.Equal(t, false, ctx.GetBool("token_unlimited_quota"))
	require.Equal(t, 0, ctx.GetInt("token_quota"))
	require.Equal(t, "vip", common.GetContextKeyString(ctx, constant.ContextKeyTokenGroup))
	require.NotContains(t, recorder.Body.String(), "apiKey")
	require.NotContains(t, recorder.Body.String(), "sk-")
}

func TestCreativeRelayImagesGenerationsUsesTemporarySessionTokenContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/creative/relay/v1/images/generations", strings.NewReader(`{"model":123}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("id", 58)
	ctx.Set("group", "default")
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "vip")
	common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "vip")

	CreativeRelayImagesGenerations(ctx)

	require.GreaterOrEqual(t, recorder.Code, http.StatusBadRequest)
	require.Equal(t, 0, ctx.GetInt("token_id"))
	require.Empty(t, ctx.GetString("token_key"))
	require.Equal(t, "creative-session-broker-vip", ctx.GetString("token_name"))
	require.Equal(t, false, ctx.GetBool("token_unlimited_quota"))
	require.Equal(t, 0, ctx.GetInt("token_quota"))
	require.Equal(t, "vip", common.GetContextKeyString(ctx, constant.ContextKeyTokenGroup))
	require.NotContains(t, recorder.Body.String(), "apiKey")
	require.NotContains(t, recorder.Body.String(), "sk-")
}

func TestCreativeRelayValidationErrorDoesNotCreateOrConsumeTokenQuotaOrLeakSecrets(t *testing.T) {
	setupCreativeControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Token{}, &model.Log{}))
	seedCreativeControllerUser(t, 57)
	realTokenKey := "creative-real-validation-token"
	require.NoError(t, model.DB.Create(&model.Token{
		Id:          5701,
		UserId:      57,
		Key:         realTokenKey,
		Name:        "real-browser-hidden-token",
		Status:      common.TokenStatusEnabled,
		RemainQuota: 888,
		UsedQuota:   4,
	}).Error)
	originalLogConsumeEnabled := common.LogConsumeEnabled
	common.LogConsumeEnabled = true
	t.Cleanup(func() {
		common.LogConsumeEnabled = originalLogConsumeEnabled
	})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/creative/relay/v1/chat/completions", strings.NewReader(`{"model":"creative-model-05"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.Header.Set("Authorization", "Bearer sk-"+realTokenKey)
	ctx.Set("id", 57)
	ctx.Set("group", "default")
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "default")

	CreativeRelayChatCompletions(ctx)

	require.GreaterOrEqual(t, recorder.Code, http.StatusBadRequest)
	responseBody := recorder.Body.String()
	require.NotContains(t, responseBody, realTokenKey)
	require.NotContains(t, responseBody, "sk-"+realTokenKey)
	require.NotContains(t, responseBody, "creative-session-broker")

	var token model.Token
	require.NoError(t, model.DB.Where("id = ?", 5701).First(&token).Error)
	require.Equal(t, 888, token.RemainQuota)
	require.Equal(t, 4, token.UsedQuota)

	var tokenCount int64
	require.NoError(t, model.DB.Model(&model.Token{}).Where("user_id = ?", 57).Count(&tokenCount).Error)
	require.Equal(t, int64(1), tokenCount)

	var temporaryTokenCount int64
	require.NoError(t, model.DB.Model(&model.Token{}).Where("user_id = ? AND name LIKE ?", 57, "creative-session-broker%").Count(&temporaryTokenCount).Error)
	require.Equal(t, int64(0), temporaryTokenCount)

	var consumeLogCount int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("user_id = ? AND type = ?", 57, model.LogTypeConsume).Count(&consumeLogCount).Error)
	require.Equal(t, int64(0), consumeLogCount)
}

func TestCreativeRelayRejectsForbiddenFieldsBeforeSessionBroker(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 53)
	seedCreativeControllerModelPool(t)

	relayReachedCount := 0
	router := newCreativeRelayBrokerTestRouter(t, 53, func(c *gin.Context) {
		relayReachedCount++
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
	auth := bootstrapCreativeSessionAuth(t, router)

	tests := []struct {
		name       string
		body       map[string]any
		extra      map[string]string
		wantField  string
		wantStatus int
	}{
		{
			name: "Authorization header",
			body: map[string]any{
				"model":    "creative-model-05",
				"messages": []any{map[string]any{"role": "user", "content": "hello"}},
			},
			extra:      map[string]string{"Authorization": "Bearer sk-test-abcdefghijklmnopqrstuvwxyz"},
			wantField:  "Authorization",
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "apiKey body field",
			body: map[string]any{
				"model":    "creative-model-05",
				"messages": []any{map[string]any{"role": "user", "content": "hello"}},
				"apiKey":   "sk-test-abcdefghijklmnopqrstuvwxyz",
			},
			wantField:  "apiKey",
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "baseUrl body field",
			body: map[string]any{
				"model":    "creative-model-05",
				"messages": []any{map[string]any{"role": "user", "content": "hello"}},
				"baseUrl":  "https://upstream.example",
			},
			wantField:  "baseUrl",
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "provider body field",
			body: map[string]any{
				"model":    "creative-model-05",
				"messages": []any{map[string]any{"role": "user", "content": "hello"}},
				"provider": "openai",
			},
			wantField:  "provider",
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "channel body field",
			body: map[string]any{
				"model":     "creative-model-05",
				"messages":  []any{map[string]any{"role": "user", "content": "hello"}},
				"channelId": 7,
			},
			wantField:  "channelId",
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "nested channel override variant",
			body: map[string]any{
				"model":    "creative-model-05",
				"messages": []any{map[string]any{"role": "user", "content": "hello"}},
				"metadata": map[string]any{
					"routing": []any{map[string]any{"channel_override": "vip-channel"}},
				},
			},
			wantField:  "metadata.routing[0].channel_override",
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "nested model_name billing override variant",
			body: map[string]any{
				"model":    "creative-model-05",
				"messages": []any{map[string]any{"role": "user", "content": "hello"}},
				"metadata": map[string]any{
					"model_name": "expensive-upstream-model",
				},
			},
			wantField:  "metadata.model_name",
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "nested req_key provider model override variant",
			body: map[string]any{
				"model":    "creative-model-05",
				"messages": []any{map[string]any{"role": "user", "content": "hello"}},
				"metadata": map[string]any{
					"req_key": "jimeng_expensive_model",
				},
			},
			wantField:  "metadata.req_key",
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "nested provider id variant",
			body: map[string]any{
				"model":    "creative-model-05",
				"messages": []any{map[string]any{"role": "user", "content": "hello"}},
				"metadata": map[string]any{
					"routing": map[string]any{"provider_id": "openai"},
				},
			},
			wantField:  "metadata.routing.provider_id",
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "nested Authorization-looking bearer value",
			body: map[string]any{
				"model":    "creative-model-05",
				"messages": []any{map[string]any{"role": "user", "content": "hello"}},
				"metadata": map[string]any{
					"publicHeaders": []any{"Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payload.signature"},
				},
			},
			wantField:  "metadata.publicHeaders[0]",
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := creativeSameOriginNonceHeaders(auth)
			for key, value := range tt.extra {
				headers[key] = value
			}

			recorder := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/chat/completions", tt.body, auth.cookies, headers)

			require.Equal(t, tt.wantStatus, recorder.Code)
			require.Equal(t, 0, relayReachedCount)
			errorObject := creativeResponseObject(t, decodeCreativeResponse(t, recorder), "error")
			require.Contains(t, errorObject["message"], "forbidden field "+tt.wantField)
		})
	}
}

func TestCreativeRelayAppliesUserModelRequestRateLimit(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 5301)
	seedCreativeControllerModelPool(t)

	originalEnabled := setting.ModelRequestRateLimitEnabled
	originalDuration := setting.ModelRequestRateLimitDurationMinutes
	originalTotal := setting.ModelRequestRateLimitCount
	originalSuccess := setting.ModelRequestRateLimitSuccessCount
	originalRedisEnabled := common.RedisEnabled
	setting.ModelRequestRateLimitEnabled = true
	setting.ModelRequestRateLimitDurationMinutes = 1
	setting.ModelRequestRateLimitCount = 0
	setting.ModelRequestRateLimitSuccessCount = 1
	common.RedisEnabled = false
	t.Cleanup(func() {
		setting.ModelRequestRateLimitEnabled = originalEnabled
		setting.ModelRequestRateLimitDurationMinutes = originalDuration
		setting.ModelRequestRateLimitCount = originalTotal
		setting.ModelRequestRateLimitSuccessCount = originalSuccess
		common.RedisEnabled = originalRedisEnabled
	})

	router := newCreativeRelayBrokerTestRouter(t, 5301, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
	auth := bootstrapCreativeSessionAuth(t, router)
	body := map[string]any{
		"model":    "creative-model-05",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	}

	first := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/chat/completions", body, auth.cookies, creativeSameOriginNonceHeaders(auth))
	require.Equal(t, http.StatusOK, first.Code)

	second := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/chat/completions", body, auth.cookies, creativeSameOriginNonceHeaders(auth))
	require.Equal(t, http.StatusTooManyRequests, second.Code)
}

func TestCreativeRelayRejectsProviderOverrideBeforeDistributionAndBilling(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 56)
	seedCreativeControllerModelPool(t)

	distributionReached := false
	billingReached := false
	// Build a minimal route with marker middleware after the forbidden-field guard.
	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("creative-distribution-gate-test-secret"))))
	router.GET("/creative/api/bootstrap", func(c *gin.Context) {
		session := sessions.Default(c)
		session.Set("username", "creative-user-56")
		session.Set("role", common.RoleCommonUser)
		session.Set("id", 56)
		session.Set("status", common.UserStatusEnabled)
		session.Set("group", "default")
		c.Set("id", 56)
		CreativeBootstrap(c)
	})
	relayRouter := router.Group("/creative/relay/v1")
	relayRouter.Use(middleware.BodyStorageCleanup())
	relayRouter.Use(middleware.CreativeSessionHeaderBridge(), middleware.UserAuth())
	relayRouter.Use(middleware.CreativeRequireNonce())
	relayRouter.Use(CreativeRejectForbiddenRelayFields())
	relayRouter.Use(func(c *gin.Context) {
		distributionReached = true
		c.Next()
	})
	relayRouter.POST("/chat/completions", func(c *gin.Context) {
		billingReached = true
		c.JSON(http.StatusOK, gin.H{"success": true})
	})

	auth := bootstrapCreativeSessionAuth(t, router)
	recorder := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/chat/completions", map[string]any{
		"model":    "creative-model-05",
		"messages": []any{map[string]any{"role": "user", "content": "override attempt"}},
		"metadata": map[string]any{"routing": map[string]any{"providerOverride": "openai"}},
	}, auth.cookies, creativeSameOriginNonceHeaders(auth))

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.False(t, distributionReached)
	require.False(t, billingReached)
	errorObject := creativeResponseObject(t, decodeCreativeResponse(t, recorder), "error")
	require.Contains(t, errorObject["message"], "forbidden field metadata.routing.providerOverride")
}

func TestCreativeVideoRelayRejectsForbiddenQueryAndHeadersForGetBeforeSessionBroker(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 60)
	seedCreativeControllerModelPool(t)

	relayReachedCount := 0
	router := newCreativeRelayBrokerTestRouter(t, 60, func(c *gin.Context) {
		relayReachedCount++
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
	auth := bootstrapCreativeSessionAuth(t, router)

	queryOverride := performCreativeSessionJSON(t, router, http.MethodGet, "/creative/relay/v1/videos/task_123?apiKey=sk-test-abcdefghijklmnopqrstuvwxyz", nil, auth.cookies, map[string]string{
		"Origin": "http://example.com",
	})
	require.Equal(t, http.StatusBadRequest, queryOverride.Code)
	require.Equal(t, 0, relayReachedCount)
	queryError := creativeResponseObject(t, decodeCreativeResponse(t, queryOverride), "error")
	require.Contains(t, queryError["message"], "forbidden field apiKey")

	headerOverride := performCreativeSessionJSON(t, router, http.MethodGet, "/creative/relay/v1/videos/task_123", nil, auth.cookies, map[string]string{
		"Origin":    "http://example.com",
		"X-API-Key": "sk-test-abcdefghijklmnopqrstuvwxyz",
	})
	require.Equal(t, http.StatusBadRequest, headerOverride.Code)
	require.Equal(t, 0, relayReachedCount)
	headerError := creativeResponseObject(t, decodeCreativeResponse(t, headerOverride), "error")
	require.Contains(t, headerError["message"], "forbidden field X-Api-Key")
}

func TestCreativeVideoRelayValidatesMultipartAndReplaysBody(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 61)
	seedCreativeControllerModelPool(t)

	relayReachedCount := 0
	router := newCreativeRelayBrokerTestRouter(t, 61, func(c *gin.Context) {
		relayReachedCount++
		form, err := common.ParseMultipartFormReusable(c)
		require.NoError(t, err)
		c.JSON(http.StatusOK, gin.H{
			"model":      form.Value["model"],
			"prompt":     form.Value["prompt"],
			"tokenGroup": common.GetContextKeyString(c, constant.ContextKeyTokenGroup),
			"usingGroup": common.GetContextKeyString(c, constant.ContextKeyUsingGroup),
		})
	})
	auth := bootstrapCreativeSessionAuth(t, router)

	missingNonce := performCreativeSessionMultipart(t, router, http.MethodPost, "/creative/relay/v1/videos", auth.cookies, nil, map[string]string{
		"model":  "creative-model-05",
		"prompt": "safe video prompt",
	})
	require.Equal(t, http.StatusForbidden, missingNonce.Code)
	require.Equal(t, 0, relayReachedCount)

	forbiddenHeaders := creativeSameOriginNonceHeaders(auth)
	forbiddenHeaders["Idempotency-Key"] = "video-multipart-forbidden"
	forbidden := performCreativeSessionMultipart(t, router, http.MethodPost, "/creative/relay/v1/videos", auth.cookies, forbiddenHeaders, map[string]string{
		"model":   "creative-model-05",
		"prompt":  "safe video prompt",
		"baseUrl": "https://upstream.example",
	})
	require.Equal(t, http.StatusBadRequest, forbidden.Code)
	require.Equal(t, 0, relayReachedCount)
	forbiddenError := creativeResponseObject(t, decodeCreativeResponse(t, forbidden), "error")
	require.Contains(t, forbiddenError["message"], "forbidden field baseUrl")

	allowedHeaders := creativeSameOriginNonceHeaders(auth)
	allowedHeaders["Idempotency-Key"] = "video-multipart-allowed"
	allowed := performCreativeSessionMultipart(t, router, http.MethodPost, "/creative/relay/v1/videos", auth.cookies, allowedHeaders, map[string]string{
		"model":  "creative-model-05",
		"prompt": "safe video prompt",
	})
	require.Equal(t, http.StatusOK, allowed.Code)
	require.Equal(t, 1, relayReachedCount)
	payload := decodeCreativeResponse(t, allowed)
	require.Equal(t, []any{"creative-model-05"}, payload["model"])
	require.Equal(t, []any{"safe video prompt"}, payload["prompt"])
	require.Equal(t, "default", payload["usingGroup"])
	require.Equal(t, "default", payload["tokenGroup"])
}

func TestCreativeVideoRelayRejectsForbiddenMultipartFilePartNames(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 62)
	seedCreativeControllerModelPool(t)

	relayReachedCount := 0
	router := newCreativeRelayBrokerTestRouter(t, 62, func(c *gin.Context) {
		relayReachedCount++
		form, err := common.ParseMultipartFormReusable(c)
		require.NoError(t, err)
		c.JSON(http.StatusOK, gin.H{
			"model": form.Value["model"],
			"files": len(form.File),
		})
	})
	auth := bootstrapCreativeSessionAuth(t, router)

	forbiddenHeaders := creativeSameOriginNonceHeaders(auth)
	forbiddenHeaders["Idempotency-Key"] = "video-multipart-file-forbidden"
	forbidden := performCreativeSessionMultipartWithFiles(t, router, http.MethodPost, "/creative/relay/v1/videos", auth.cookies, forbiddenHeaders, map[string]string{
		"model":  "creative-model-05",
		"prompt": "safe video prompt",
	}, map[string]string{
		"headers.Authorization": "Bearer leak",
	})
	require.Equal(t, http.StatusBadRequest, forbidden.Code)
	require.Equal(t, 0, relayReachedCount)
	forbiddenError := creativeResponseObject(t, decodeCreativeResponse(t, forbidden), "error")
	require.Contains(t, forbiddenError["message"], "forbidden field headers.Authorization")

	allowedHeaders := creativeSameOriginNonceHeaders(auth)
	allowedHeaders["Idempotency-Key"] = "video-multipart-file-allowed"
	allowed := performCreativeSessionMultipartWithFiles(t, router, http.MethodPost, "/creative/relay/v1/videos", auth.cookies, allowedHeaders, map[string]string{
		"model":  "creative-model-05",
		"prompt": "safe video prompt",
	}, map[string]string{
		"input_reference": "image-bytes",
	})
	require.Equal(t, http.StatusOK, allowed.Code)
	require.Equal(t, 1, relayReachedCount)
	payload := decodeCreativeResponse(t, allowed)
	require.Equal(t, []any{"creative-model-05"}, payload["model"])
	require.Equal(t, float64(1), payload["files"])
}

func TestCreativeRelayVideosUsesTemporarySessionTokenContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/creative/relay/v1/videos", strings.NewReader(`{"model":"creative-model-05","prompt":"safe"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("id", 62)
	ctx.Set("group", "default")
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "vip")
	common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "vip")

	CreativeRelayVideos(ctx)

	require.GreaterOrEqual(t, recorder.Code, http.StatusBadRequest)
	require.Equal(t, 0, ctx.GetInt("token_id"))
	require.Empty(t, ctx.GetString("token_key"))
	require.Equal(t, "creative-session-broker-vip", ctx.GetString("token_name"))
	require.Equal(t, false, ctx.GetBool("token_unlimited_quota"))
	require.Equal(t, 0, ctx.GetInt("token_quota"))
	require.Equal(t, "vip", common.GetContextKeyString(ctx, constant.ContextKeyTokenGroup))
	require.NotContains(t, recorder.Body.String(), "apiKey")
	require.NotContains(t, recorder.Body.String(), "sk-")
}

func TestCreativeRelayVideoHandlersRejectAccessTokenOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, tt := range []struct {
		name    string
		method  string
		target  string
		params  gin.Params
		handler gin.HandlerFunc
	}{
		{name: "submit", method: http.MethodPost, target: "/creative/relay/v1/videos", handler: CreativeRelayVideos},
		{name: "fetch", method: http.MethodGet, target: "/creative/relay/v1/videos/task_abc", params: gin.Params{{Key: "task_id", Value: "task_abc"}}, handler: CreativeRelayVideoFetch},
		{name: "content", method: http.MethodGet, target: "/creative/relay/v1/videos/task_abc/content", params: gin.Params{{Key: "task_id", Value: "task_abc"}}, handler: CreativeRelayVideoContent},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(tt.method, tt.target, strings.NewReader(`{"model":"creative-model-05"}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			ctx.Params = tt.params
			ctx.Set("id", 63)
			ctx.Set("group", "default")
			ctx.Set("use_access_token", true)

			tt.handler(ctx)

			require.Equal(t, http.StatusForbidden, recorder.Code)
			errorObject := creativeResponseObject(t, decodeCreativeResponse(t, recorder), "error")
			require.Contains(t, errorObject["message"], "browser session")
		})
	}
}

func TestCreativeRelayVideoContentIsOwnerScoped(t *testing.T) {
	setupCreativeControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Task{}, &model.Channel{}))
	require.NoError(t, model.DB.Create(&model.Task{
		TaskID:    "task_other_user",
		UserId:    6502,
		Status:    model.TaskStatusSuccess,
		ChannelId: 1,
	}).Error)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/creative/relay/v1/videos/task_other_user/content", nil)
	ctx.Params = gin.Params{{Key: "task_id", Value: "task_other_user"}}
	ctx.Set("id", 6501)
	ctx.Set("group", "default")
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "default")

	CreativeRelayVideoContent(ctx)

	require.Equal(t, http.StatusNotFound, recorder.Code)
	errorObject := creativeResponseObject(t, decodeCreativeResponse(t, recorder), "error")
	require.Contains(t, errorObject["message"], "Task not found")
}

func TestCreativeRelayVideoContentUsesStoredKeyAffinity(t *testing.T) {
	setupCreativeControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Task{}, &model.Channel{}))

	var gotAuthorization string
	previousTransport := http.DefaultTransport
	http.DefaultTransport = creativeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotAuthorization = r.Header.Get("Authorization")
		require.Equal(t, "https://video.example/v1/videos/upstream_content/content", r.URL.String())
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"video/mp4"}},
			Body:       io.NopCloser(strings.NewReader("video-bytes")),
			Request:    r,
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	fetchSetting := system_setting.GetFetchSetting()
	previousSSRFProtection := fetchSetting.EnableSSRFProtection
	fetchSetting.EnableSSRFProtection = false
	t.Cleanup(func() { fetchSetting.EnableSSRFProtection = previousSSRFProtection })

	baseURL := "https://video.example"
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:      2,
		Type:    constant.ChannelTypeOpenAI,
		Key:     "sk-fresh-random-key",
		BaseURL: &baseURL,
		Status:  common.ChannelStatusEnabled,
		Name:    "content-affinity",
	}).Error)
	require.NoError(t, model.DB.Create(&model.Task{
		TaskID:    "task_content_affinity",
		UserId:    6503,
		Status:    model.TaskStatusSuccess,
		ChannelId: 2,
		Platform:  constant.TaskPlatform("openai"),
		PrivateData: model.TaskPrivateData{
			Key:            "sk-original-selected-key",
			UpstreamTaskID: "upstream_content",
		},
	}).Error)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/creative/relay/v1/videos/task_content_affinity/content", nil)
	ctx.Params = gin.Params{{Key: "task_id", Value: "task_content_affinity"}}
	ctx.Set("id", 6503)
	ctx.Set("group", "default")
	ctx.Set(creativeVideoContentContextKey, true)
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "default")

	VideoProxy(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "Bearer sk-original-selected-key", gotAuthorization)
	require.Equal(t, "video-bytes", recorder.Body.String())
	require.Equal(t, "private, no-store", recorder.Header().Get("Cache-Control"))
	require.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))
}

func TestCreativeRelayVideoFetchRewritesRawResultURLToContentProxy(t *testing.T) {
	setupCreativeControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Task{}))

	rawURL := "https://private.example/video.mp4?X-Amz-Signature=secret"
	task := &model.Task{
		TaskID:    "task_video_private_url",
		UserId:    6504,
		Status:    model.TaskStatusSuccess,
		Platform:  constant.TaskPlatform(fmt.Sprintf("%d", constant.ChannelTypeSora)),
		Progress:  "100%",
		ChannelId: 1,
		PrivateData: model.TaskPrivateData{
			ResultURL: rawURL,
		},
	}
	task.SetData(map[string]any{
		"id":     "upstream_private_url",
		"object": "video",
		"model":  "sora-test",
		"status": "completed",
		"url":    rawURL,
		"metadata": map[string]any{
			"url":          rawURL,
			"provider_url": rawURL,
			"duration":     4,
		},
	})
	require.NoError(t, model.DB.Create(task).Error)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/creative/relay/v1/videos/task_video_private_url", nil)
	ctx.Params = gin.Params{{Key: "task_id", Value: "task_video_private_url"}}
	ctx.Set("id", 6504)
	ctx.Set("group", "default")
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "default")

	CreativeRelayVideoFetch(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	payload := decodeCreativeResponse(t, recorder)
	metadata := creativeResponseObject(t, payload, "metadata")
	require.Equal(t, "/creative/relay/v1/videos/task_video_private_url/content", metadata["url"])
	require.Equal(t, float64(4), metadata["duration"])
	require.NotContains(t, recorder.Body.String(), rawURL)
	require.NotContains(t, recorder.Body.String(), "provider_url")
	require.NotContains(t, recorder.Body.String(), "X-Amz-Signature")
}

func TestCreativeVideoRelayRequiresSameOriginForGet(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 64)
	seedCreativeControllerModelPool(t)

	relayReachedCount := 0
	router := newCreativeRelayBrokerTestRouter(t, 64, func(c *gin.Context) {
		relayReachedCount++
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
	auth := bootstrapCreativeSessionAuth(t, router)

	missingOrigin := performCreativeSessionJSON(t, router, http.MethodGet, "/creative/relay/v1/videos/task_123", nil, auth.cookies, nil)
	require.Equal(t, http.StatusForbidden, missingOrigin.Code)
	require.Equal(t, 0, relayReachedCount)

	crossHeaders := map[string]string{"Origin": "https://evil.example"}
	crossOrigin := performCreativeSessionJSON(t, router, http.MethodGet, "/creative/relay/v1/videos/task_123", nil, auth.cookies, crossHeaders)
	require.Equal(t, http.StatusForbidden, crossOrigin.Code)
	require.Equal(t, 0, relayReachedCount)

	sameHeaders := map[string]string{"Origin": "http://example.com"}
	sameOrigin := performCreativeSessionJSON(t, router, http.MethodGet, "/creative/relay/v1/videos/task_123", nil, auth.cookies, sameHeaders)
	require.Equal(t, http.StatusOK, sameOrigin.Code)
	require.Equal(t, 1, relayReachedCount)
}

func TestCreativeVideoRelayGateDisablesBeforeSessionBroker(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 65)
	seedCreativeControllerModelPool(t)

	relayReachedCount := 0
	router := newCreativeRelayBrokerTestRouter(t, 65, func(c *gin.Context) {
		relayReachedCount++
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
	auth := bootstrapCreativeSessionAuth(t, router)
	defer SetCreativeVideoRelayEnabledForTest(false)()
	headers := creativeSameOriginNonceHeaders(auth)
	headers["Idempotency-Key"] = "disabled-video-submit"

	recorder := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/videos", map[string]any{"model": "creative-model-05", "prompt": "safe"}, auth.cookies, headers)
	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.Equal(t, 0, relayReachedCount)
}

func TestCreativeVideoRelayRejectsBracketAndXRoutingOverrides(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 66)
	seedCreativeControllerModelPool(t)

	relayReachedCount := 0
	router := newCreativeRelayBrokerTestRouter(t, 66, func(c *gin.Context) {
		relayReachedCount++
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
	auth := bootstrapCreativeSessionAuth(t, router)

	headerOverride := performCreativeSessionJSON(t, router, http.MethodGet, "/creative/relay/v1/videos/task_123", nil, auth.cookies, map[string]string{
		"Origin":        "http://example.com",
		"X-Provider-Id": "provider-leak",
	})
	require.Equal(t, http.StatusBadRequest, headerOverride.Code)
	require.Equal(t, 0, relayReachedCount)

	upstreamHeaderOverride := performCreativeSessionJSON(t, router, http.MethodGet, "/creative/relay/v1/videos/task_123", nil, auth.cookies, map[string]string{
		"Origin":         "http://example.com",
		"X-Upstream-Key": "upstream-key-leak",
	})
	require.Equal(t, http.StatusBadRequest, upstreamHeaderOverride.Code)
	require.Equal(t, 0, relayReachedCount)

	queryOverride := performCreativeSessionJSON(t, router, http.MethodGet, "/creative/relay/v1/videos/task_123?headers[Authorization]=Bearer+leak", nil, auth.cookies, map[string]string{"Origin": "http://example.com"})
	require.Equal(t, http.StatusBadRequest, queryOverride.Code)
	require.Equal(t, 0, relayReachedCount)

	upstreamQueryOverride := performCreativeSessionJSON(t, router, http.MethodGet, "/creative/relay/v1/videos/task_123?x_upstream_base_url=https://upstream.example", nil, auth.cookies, map[string]string{"Origin": "http://example.com"})
	require.Equal(t, http.StatusBadRequest, upstreamQueryOverride.Code)
	require.Equal(t, 0, relayReachedCount)

	headers := creativeSameOriginNonceHeaders(auth)
	headers["Idempotency-Key"] = "nested-body-reject"
	bodyOverride := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/videos", map[string]any{
		"model":    "creative-model-05",
		"prompt":   "safe",
		"metadata": map[string]any{"request_headers.Authorization": "Bearer leak"},
	}, auth.cookies, headers)
	require.Equal(t, http.StatusBadRequest, bodyOverride.Code)
	require.Equal(t, 0, relayReachedCount)
}

func TestCreativeVideoSubmitIdempotencyReplaysExistingTaskAndConflictsOnPayload(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 67)
	require.NoError(t, model.DB.AutoMigrate(&model.Task{}, &model.CreativeVideoIdempotency{}))
	record, existed, err := model.PrepareCreativeVideoIdempotency(67, "idem-1", "hash-1")
	require.NoError(t, err)
	require.False(t, existed)
	require.NoError(t, model.DB.Create(&model.Task{TaskID: record.TaskID, UserId: 67, Status: model.TaskStatusSubmitted, ChannelId: 1, Platform: constant.TaskPlatform("sora")}).Error)

	replay, existed, err := model.PrepareCreativeVideoIdempotency(67, "idem-1", "hash-1")
	require.NoError(t, err)
	require.True(t, existed)
	require.Equal(t, record.TaskID, replay.TaskID)
	require.Equal(t, "hash-1", replay.PayloadHash)

	conflict, existed, err := model.PrepareCreativeVideoIdempotency(67, "idem-1", "hash-2")
	require.NoError(t, err)
	require.True(t, existed)
	require.Equal(t, "hash-1", conflict.PayloadHash)
}

func TestCreativeVideoSubmitIdempotencyIsScopedByAction(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 69)
	require.NoError(t, model.DB.AutoMigrate(&model.Task{}, &model.CreativeVideoIdempotency{}))

	videoSubmit, existed, err := model.PrepareCreativeVideoIdempotencyScoped(69, "video.submit", "idem-shared", "hash-video")
	require.NoError(t, err)
	require.False(t, existed)
	require.Equal(t, "video.submit", videoSubmit.Scope)

	statusRetry, existed, err := model.PrepareCreativeVideoIdempotencyScoped(69, "video.status", "idem-shared", "hash-status")
	require.NoError(t, err)
	require.False(t, existed)
	require.NotEqual(t, videoSubmit.TaskID, statusRetry.TaskID)
	require.Equal(t, "video.status", statusRetry.Scope)

	replay, existed, err := model.PrepareCreativeVideoIdempotencyScoped(69, "video.submit", "idem-shared", "hash-video")
	require.NoError(t, err)
	require.True(t, existed)
	require.Equal(t, videoSubmit.TaskID, replay.TaskID)
	require.Equal(t, "hash-video", replay.PayloadHash)
}

func TestCreativeVideoSubmitIdempotencyReplayRewritesRawResultURL(t *testing.T) {
	setupCreativeControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Task{}, &model.CreativeVideoIdempotency{}))

	body := `{"model":"creative-model-05","prompt":"safe video"}`
	sum := sha256.Sum256([]byte(body))
	payloadHash := hex.EncodeToString(sum[:])
	record, existed, err := model.PrepareCreativeVideoIdempotency(169, "replay-private-url", payloadHash)
	require.NoError(t, err)
	require.False(t, existed)

	rawURL := "https://private.example/replay.mp4?token=secret"
	task := &model.Task{
		TaskID:   record.TaskID,
		UserId:   169,
		Status:   model.TaskStatusSuccess,
		Platform: constant.TaskPlatform(fmt.Sprintf("%d", constant.ChannelTypeSora)),
		Progress: "100%",
		PrivateData: model.TaskPrivateData{
			ResultURL: rawURL,
		},
	}
	task.SetData(map[string]any{
		"object": "video",
		"status": "completed",
		"metadata": map[string]any{
			"url": rawURL,
		},
	})
	require.NoError(t, model.DB.Create(task).Error)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.BodyStorageCleanup())
	router.Use(func(c *gin.Context) {
		c.Set("id", 169)
		c.Set("group", "default")
		c.Next()
	})
	router.POST("/creative/relay/v1/videos", CreativeVideoSubmitIdempotency(), func(c *gin.Context) {
		t.Fatal("idempotency replay must short-circuit before relay handler")
	})

	request := httptest.NewRequest(http.MethodPost, "/creative/relay/v1/videos", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "replay-private-url")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	payload := decodeCreativeResponse(t, recorder)
	metadata := creativeResponseObject(t, payload, "metadata")
	require.Equal(t, "/creative/relay/v1/videos/"+record.TaskID+"/content", metadata["url"])
	require.NotContains(t, recorder.Body.String(), rawURL)
	require.NotContains(t, recorder.Body.String(), "token=secret")
}

func TestCreativeVideoSubmitIdempotencyCleansRecordWhenSessionBrokerRejects(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 68)
	seedCreativeControllerModelPool(t)

	router := newCreativeRelayBrokerTestRouter(t, 68, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
	auth := bootstrapCreativeSessionAuth(t, router)
	headers := creativeSameOriginNonceHeaders(auth)
	headers["Idempotency-Key"] = "session-broker-reject"

	rejected := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/videos", map[string]any{
		"model":  "creative-model-not-available",
		"prompt": "safe video prompt",
	}, auth.cookies, headers)

	require.Equal(t, http.StatusForbidden, rejected.Code)
	var count int64
	require.NoError(t, model.DB.Model(&model.CreativeVideoIdempotency{}).
		Where("user_id = ? AND request_id = ?", 68, "session-broker-reject").
		Count(&count).Error)
	require.Equal(t, int64(0), count)
}

func TestCreativeImageTaskSubmitFetchAndReplayAreMockOnlyAndPrivate(t *testing.T) {
	setupCreativeControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Task{}, &model.CreativeVideoIdempotency{}))
	seedCreativeControllerUser(t, 801)
	seedCreativeControllerModelPool(t)
	withCreativeImageTaskMockBinding(t, true, []string{"default"})
	router := newCreativeRelayBrokerTestRouter(t, 801, func(c *gin.Context) {
		t.Fatal("image task route must not use distributed provider relay")
	})
	auth := bootstrapCreativeSessionAuth(t, router)
	headers := creativeSameOriginNonceHeaders(auth)
	headers["Idempotency-Key"] = "image-task-submit-1"

	submit := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/tasks", map[string]any{
		"model":  "mock:gpt-image-2:preview",
		"prompt": "safe mock image",
		"userParams": map[string]any{
			"size":    "1024x1024",
			"quality": "auto",
		},
	}, auth.cookies, headers)
	require.Equal(t, http.StatusAccepted, submit.Code)
	require.NotContains(t, submit.Body.String(), "user_id")
	require.NotContains(t, submit.Body.String(), "channel_id")
	require.NotContains(t, submit.Body.String(), "channelId")
	require.NotContains(t, submit.Body.String(), "quota")
	require.NotContains(t, submit.Body.String(), "private_data")
	require.NotContains(t, submit.Body.String(), "mock://")
	require.NotContains(t, submit.Body.String(), "token=secret")
	payload := decodeCreativeResponse(t, submit)
	taskID, ok := payload["task_id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, taskID)
	require.Equal(t, "completed", payload["status"])
	require.Equal(t, "mock:gpt-image-2:preview", payload["model"])
	result := creativeResponseObject(t, payload, "result")
	require.Equal(t, "/creative/relay/v1/images/tasks/"+taskID+"/content", result["url"])
	metadata := creativeResponseObject(t, payload, "metadata")
	require.Equal(t, "mock:gpt-image-2:preview", metadata["bindingId"])
	require.Equal(t, "gpt-image-2", metadata["providerModelId"])
	require.Equal(t, "mock-gpt-image-2-price", metadata["priceModelId"])
	require.NotContains(t, metadata, "channelId")
	var storedTask model.Task
	require.NoError(t, model.DB.Where("user_id = ? AND task_id = ?", 801, taskID).First(&storedTask).Error)
	var storedMetadata creativeImageTaskMetadata
	require.NoError(t, storedTask.GetData(&storedMetadata))
	require.True(t, storedMetadata.CreativeManaged)
	require.Equal(t, "mock:gpt-image-2:preview", storedMetadata.BindingId)
	require.Equal(t, "gpt-image-2", storedMetadata.ProviderModelId)
	require.Equal(t, "mock-gpt-image-2-price", storedMetadata.PriceModelId)
	require.Equal(t, "mock_image_task", storedMetadata.AdapterPreset)
	require.Equal(t, "mock_gpt_image", storedMetadata.ParameterTemplate)
	require.Equal(t, 0, storedMetadata.ChannelId)
	require.Equal(t, map[string]any{"quality": "auto", "size": "1024x1024"}, storedMetadata.UserParams)
	require.NotNil(t, storedTask.PrivateData.BillingContext)
	require.Equal(t, "mock-gpt-image-2-price", storedTask.PrivateData.BillingContext.OriginModelName)
	require.True(t, storedTask.PrivateData.BillingContext.PerCallBilling)
	require.Equal(t, 0, storedTask.PrivateData.BillingContext.PreConsumedQuota)

	replay := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/tasks", map[string]any{
		"model":  "mock:gpt-image-2:preview",
		"prompt": "safe mock image",
		"userParams": map[string]any{
			"size":    "1024x1024",
			"quality": "auto",
		},
	}, auth.cookies, headers)
	require.Equal(t, http.StatusOK, replay.Code)
	require.Equal(t, taskID, decodeCreativeResponse(t, replay)["task_id"])

	fetch := performCreativeSessionJSON(t, router, http.MethodGet, "/creative/relay/v1/images/tasks/"+taskID, nil, auth.cookies, map[string]string{"Origin": "http://example.com"})
	require.Equal(t, http.StatusOK, fetch.Code)
	require.Equal(t, taskID, decodeCreativeResponse(t, fetch)["task_id"])
	require.NotContains(t, fetch.Body.String(), "mock://")
	require.NotContains(t, fetch.Body.String(), "token=secret")

	content := performCreativeSessionJSON(t, router, http.MethodGet, "/creative/relay/v1/images/tasks/"+taskID+"/content", nil, auth.cookies, map[string]string{"Origin": "http://example.com"})
	require.Equal(t, http.StatusOK, content.Code)
	require.Equal(t, "image/png", content.Header().Get("Content-Type"))
	require.Equal(t, "private, no-store", content.Header().Get("Cache-Control"))
}

func TestCreativeImageTaskRouteBoundariesAndResolverFailClosed(t *testing.T) {
	setupCreativeControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Task{}, &model.CreativeVideoIdempotency{}))
	seedCreativeControllerUser(t, 802)
	seedCreativeControllerModelPool(t)
	withCreativeImageTaskMockBinding(t, true, []string{"default"})
	router := newCreativeRelayBrokerTestRouter(t, 802, func(c *gin.Context) {
		t.Fatal("image task route must not use distributed provider relay")
	})
	auth := bootstrapCreativeSessionAuth(t, router)

	body := map[string]any{"model": "mock:gpt-image-2:preview", "prompt": "safe mock image", "userParams": map[string]any{"size": "1024x1024"}}
	missingNonce := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/tasks", body, auth.cookies, map[string]string{"Idempotency-Key": "missing-nonce"})
	require.Equal(t, http.StatusForbidden, missingNonce.Code)

	crossHeaders := creativeNonceHeaders(auth)
	crossHeaders["Origin"] = "https://evil.example"
	crossHeaders["Idempotency-Key"] = "cross-origin"
	crossOrigin := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/tasks", body, auth.cookies, crossHeaders)
	require.Equal(t, http.StatusForbidden, crossOrigin.Code)

	noIdempotency := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/tasks", body, auth.cookies, creativeSameOriginNonceHeaders(auth))
	require.Equal(t, http.StatusBadRequest, noIdempotency.Code)
	require.Contains(t, creativeResponseObject(t, decodeCreativeResponse(t, noIdempotency), "error")["message"], "Idempotency-Key")

	referenceHeaders := creativeSameOriginNonceHeaders(auth)
	referenceHeaders["Idempotency-Key"] = "image-reference-rejected"
	reference := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/tasks", map[string]any{
		"model":  "mock:gpt-image-2:preview",
		"prompt": "safe",
		"images": []any{"/creative/api/assets/asset-1/content"},
	}, auth.cookies, referenceHeaders)
	require.Equal(t, http.StatusBadRequest, reference.Code)
	require.Contains(t, creativeResponseObject(t, decodeCreativeResponse(t, reference), "error")["message"], "reference images are not supported")

	forbiddenHeaders := creativeSameOriginNonceHeaders(auth)
	forbiddenHeaders["Idempotency-Key"] = "forbidden-body"
	forbidden := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/tasks", map[string]any{
		"model":  "mock:gpt-image-2:preview",
		"prompt": "safe",
		"userParams": map[string]any{
			"callback": "https://evil.example/cb",
		},
	}, auth.cookies, forbiddenHeaders)
	require.Equal(t, http.StatusBadRequest, forbidden.Code)

	disabledHeaders := creativeSameOriginNonceHeaders(auth)
	disabledHeaders["Idempotency-Key"] = "adapter-disabled"
	withCreativeImageTaskMockBinding(t, false, []string{"default"})
	disabled := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/tasks", body, auth.cookies, disabledHeaders)
	require.Equal(t, http.StatusBadRequest, disabled.Code)
	require.Contains(t, creativeResponseObject(t, decodeCreativeResponse(t, disabled), "error")["message"], "disabled")
}

func TestCreativeImageTaskRejectsBoundaryAliasesBeforeMockInsert(t *testing.T) {
	setupCreativeControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Task{}, &model.CreativeVideoIdempotency{}))
	seedCreativeControllerUser(t, 808)
	seedCreativeControllerModelPool(t)
	withCreativeImageTaskMockBinding(t, true, []string{"default"})
	router := newCreativeRelayBrokerTestRouter(t, 808, func(c *gin.Context) {
		t.Fatal("image task route must not use distributed provider relay")
	})
	auth := bootstrapCreativeSessionAuth(t, router)
	body := map[string]any{"model": "mock:gpt-image-2:preview", "prompt": "safe mock image"}
	previousInsert := creativeImageTaskInsert
	insertCount := 0
	creativeImageTaskInsert = func(task *model.Task) error {
		insertCount++
		return previousInsert(task)
	}
	t.Cleanup(func() { creativeImageTaskInsert = previousInsert })

	noSessionHeaders := map[string]string{
		"Origin":          "http://example.com",
		"Idempotency-Key": "image-no-session",
	}
	noSession := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/tasks", body, nil, noSessionHeaders)
	require.NotEqual(t, http.StatusAccepted, noSession.Code)

	badNonceHeaders := map[string]string{
		"Origin":           "http://example.com",
		"X-Creative-CSRF":  auth.csrfToken,
		"X-Creative-Nonce": "wrong-nonce",
		"Idempotency-Key":  "image-bad-nonce",
	}
	badNonce := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/tasks", body, auth.cookies, badNonceHeaders)
	require.Equal(t, http.StatusForbidden, badNonce.Code)

	headerHeaders := creativeSameOriginNonceHeaders(auth)
	headerHeaders["Idempotency-Key"] = "image-forbidden-header"
	headerHeaders["X-Notify-Hook"] = "https://evil.example/callback"
	headerCase := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/tasks", body, auth.cookies, headerHeaders)
	require.Equal(t, http.StatusBadRequest, headerCase.Code)
	require.Contains(t, creativeResponseObject(t, decodeCreativeResponse(t, headerCase), "error")["message"], "forbidden field X-Notify-Hook")

	queryHeaders := creativeSameOriginNonceHeaders(auth)
	queryHeaders["Idempotency-Key"] = "image-forbidden-query"
	queryCase := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/tasks?ownerId=999", body, auth.cookies, queryHeaders)
	require.Equal(t, http.StatusBadRequest, queryCase.Code)
	require.Contains(t, creativeResponseObject(t, decodeCreativeResponse(t, queryCase), "error")["message"], "forbidden field ownerId")

	bodyAliasHeaders := creativeSameOriginNonceHeaders(auth)
	bodyAliasHeaders["Idempotency-Key"] = "image-forbidden-body-alias"
	bodyAliasCase := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/tasks", map[string]any{
		"model":  "mock:gpt-image-2:preview",
		"prompt": "safe mock image",
		"metadata": map[string]any{
			"sourceProfileId": "standalone-provider",
			"internalOptions": map[string]any{
				"onProgress": "callback",
			},
		},
	}, auth.cookies, bodyAliasHeaders)
	require.Equal(t, http.StatusBadRequest, bodyAliasCase.Code)
	require.Contains(t, creativeResponseObject(t, decodeCreativeResponse(t, bodyAliasCase), "error")["message"], "forbidden field metadata.")

	routingHeaders := creativeSameOriginNonceHeaders(auth)
	routingHeaders["Idempotency-Key"] = "image-forbidden-routing"
	routingCase := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/tasks", map[string]any{
		"model":   "mock:gpt-image-2:preview",
		"prompt":  "safe mock image",
		"routing": map[string]any{"channel": 7},
	}, auth.cookies, routingHeaders)
	require.Equal(t, http.StatusBadRequest, routingCase.Code)
	require.Contains(t, creativeResponseObject(t, decodeCreativeResponse(t, routingCase), "error")["message"], "forbidden field routing.channel")

	formHeaders := creativeSameOriginNonceHeaders(auth)
	formHeaders["Idempotency-Key"] = "image-forbidden-form"
	formCase := performCreativeSessionMultipart(t, router, http.MethodPost, "/creative/relay/v1/images/tasks", auth.cookies, formHeaders, map[string]string{
		"model":         "mock:gpt-image-2:preview",
		"prompt":        "safe mock image",
		"mj-api-secret": "leaked-secret",
	})
	require.Equal(t, http.StatusBadRequest, formCase.Code)
	require.Contains(t, creativeResponseObject(t, decodeCreativeResponse(t, formCase), "error")["message"], "forbidden field mj-api-secret")

	fileHeaders := creativeSameOriginNonceHeaders(auth)
	fileHeaders["Idempotency-Key"] = "image-forbidden-file"
	fileCase := performCreativeSessionMultipartWithFiles(t, router, http.MethodPost, "/creative/relay/v1/images/tasks", auth.cookies, fileHeaders, map[string]string{
		"model":  "mock:gpt-image-2:preview",
		"prompt": "safe mock image",
	}, map[string]string{
		"callback": "file contents",
	})
	require.Equal(t, http.StatusBadRequest, fileCase.Code)
	require.Contains(t, creativeResponseObject(t, decodeCreativeResponse(t, fileCase), "error")["message"], "forbidden field callback")

	require.Equal(t, 0, insertCount)
}

func TestCreativeImageSyncRouteRejectsManagedBindingBeforeProviderRelay(t *testing.T) {
	setupCreativeControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Task{}, &model.CreativeVideoIdempotency{}))
	seedCreativeControllerUser(t, 807)
	seedCreativeControllerModelPool(t)
	withCreativeImageTaskMockBinding(t, true, []string{"default"})
	router := newCreativeRelayBrokerTestRouter(t, 807, func(c *gin.Context) {
		t.Fatal("managed image binding sync route must fail before provider relay")
	})
	auth := bootstrapCreativeSessionAuth(t, router)
	headers := creativeSameOriginNonceHeaders(auth)
	recorder := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/generations", map[string]any{
		"model":  "mock:gpt-image-2:preview",
		"prompt": "safe mock image",
		"userParams": map[string]any{
			"size": "1024x1024",
		},
	}, auth.cookies, headers)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, creativeResponseObject(t, decodeCreativeResponse(t, recorder), "error")["message"], "/creative/relay/v1/images/tasks")
	require.NotContains(t, recorder.Body.String(), "mock://")
	require.NotContains(t, recorder.Body.String(), "token=secret")
}

func TestCreativeImageTaskFetchIsOwnerScopedAndPlatformScoped(t *testing.T) {
	setupCreativeControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Task{}, &model.CreativeVideoIdempotency{}))
	seedCreativeControllerUser(t, 803)
	seedCreativeControllerUser(t, 804)
	withCreativeImageTaskMockBinding(t, true, []string{"default"})
	task := &model.Task{
		TaskID:      "task_image_owner_scope",
		UserId:      804,
		Group:       "default",
		Platform:    constant.TaskPlatformCreativeImage,
		Action:      creativeImageTaskActionGenerate,
		Status:      model.TaskStatusSuccess,
		Progress:    "100%",
		PrivateData: model.TaskPrivateData{ResultURL: "mock://creative-image/task_image_owner_scope?token=secret"},
	}
	task.SetData(creativeImageTaskMetadata{
		Version:           1,
		CreativeManaged:   true,
		BindingId:         "mock:gpt-image-2:preview",
		ProviderModelId:   "gpt-image-2",
		PriceModelId:      "mock-gpt-image-2-price",
		AdapterPreset:     "mock_image_task",
		ParameterTemplate: "mock_gpt_image",
	})
	require.NoError(t, model.DB.Create(task).Error)
	sameUserWrongPlatform := &model.Task{
		TaskID:   "task_image_wrong_platform_same_user",
		UserId:   803,
		Group:    "default",
		Platform: constant.TaskPlatformSuno,
		Action:   creativeImageTaskActionGenerate,
		Status:   model.TaskStatusSuccess,
	}
	sameUserWrongPlatform.SetData(creativeImageTaskMetadata{
		Version:           1,
		CreativeManaged:   true,
		BindingId:         "mock:gpt-image-2:preview",
		ProviderModelId:   "gpt-image-2",
		PriceModelId:      "mock-gpt-image-2-price",
		AdapterPreset:     "mock_image_task",
		ParameterTemplate: "mock_gpt_image",
	})
	require.NoError(t, model.DB.Create(sameUserWrongPlatform).Error)
	sameUserUnmanaged := &model.Task{
		TaskID:   "task_image_unmanaged_same_user",
		UserId:   803,
		Group:    "default",
		Platform: constant.TaskPlatformCreativeImage,
		Action:   creativeImageTaskActionGenerate,
		Status:   model.TaskStatusSuccess,
	}
	require.NoError(t, model.DB.Create(sameUserUnmanaged).Error)
	router := newCreativeRelayBrokerTestRouter(t, 803, func(c *gin.Context) {})
	auth := bootstrapCreativeSessionAuth(t, router)

	fetch := performCreativeSessionJSON(t, router, http.MethodGet, "/creative/relay/v1/images/tasks/task_image_owner_scope", nil, auth.cookies, map[string]string{"Origin": "http://example.com"})
	require.Equal(t, http.StatusNotFound, fetch.Code)
	require.NotContains(t, fetch.Body.String(), "token=secret")
	wrongPlatform := performCreativeSessionJSON(t, router, http.MethodGet, "/creative/relay/v1/images/tasks/task_image_wrong_platform_same_user", nil, auth.cookies, map[string]string{"Origin": "http://example.com"})
	require.Equal(t, http.StatusNotFound, wrongPlatform.Code)
	unmanaged := performCreativeSessionJSON(t, router, http.MethodGet, "/creative/relay/v1/images/tasks/task_image_unmanaged_same_user", nil, auth.cookies, map[string]string{"Origin": "http://example.com"})
	require.Equal(t, http.StatusNotFound, unmanaged.Code)
}

func TestCreativeImageTaskPublicSurfacesDoNotLeakFakeSecretCorpus(t *testing.T) {
	setupCreativeControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Task{}, &model.CreativeVideoIdempotency{}))
	seedCreativeControllerUser(t, 810)
	corpus := []string{
		"Bearer sk-test-secret",
		"sk-test-secret",
		"https://provider.example/v1/images?X-Amz-Signature=abc&X-Amz-Credential=credential",
		"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB",
		"token=secret",
	}
	task := &model.Task{
		TaskID:    "task_image_secret_private",
		UserId:    810,
		Group:     "default",
		Platform:  constant.TaskPlatformCreativeImage,
		Action:    creativeImageTaskActionGenerate,
		Status:    model.TaskStatusSuccess,
		Progress:  "100%",
		ChannelId: 99,
		Quota:     123,
		PrivateData: model.TaskPrivateData{
			UpstreamTaskID: corpus[0],
			Key:            corpus[1],
			ResultURL:      corpus[2],
			BillingContext: &model.TaskBillingContext{OriginModelName: "mock-gpt-image-2-price", PreConsumedQuota: 456},
		},
		FailReason: strings.Join(corpus, " "),
	}
	task.SetData(creativeImageTaskMetadata{
		Version:           1,
		CreativeManaged:   true,
		BindingId:         "mock:gpt-image-2:preview",
		ProviderModelId:   "gpt-image-2",
		PriceModelId:      "mock-gpt-image-2-price",
		AdapterPreset:     "mock_image_task",
		ParameterTemplate: "mock_gpt_image",
		ChannelId:         99,
	})
	require.NoError(t, model.DB.Create(task).Error)
	router := newCreativeRelayBrokerTestRouter(t, 810, func(c *gin.Context) {})
	auth := bootstrapCreativeSessionAuth(t, router)

	fetch := performCreativeSessionJSON(t, router, http.MethodGet, "/creative/relay/v1/images/tasks/task_image_secret_private", nil, auth.cookies, map[string]string{"Origin": "http://example.com"})
	require.Equal(t, http.StatusOK, fetch.Code)
	content := performCreativeSessionJSON(t, router, http.MethodGet, "/creative/relay/v1/images/tasks/task_image_secret_private/content", nil, auth.cookies, map[string]string{"Origin": "http://example.com"})
	require.Equal(t, http.StatusOK, content.Code)

	publicBodies := []string{fetch.Body.String(), content.Body.String()}
	for _, body := range publicBodies {
		require.NotContains(t, body, "user_id")
		require.NotContains(t, body, "channel_id")
		require.NotContains(t, body, "channelId")
		require.NotContains(t, body, "quota")
		require.NotContains(t, body, "private_data")
		for _, secret := range corpus {
			require.NotContains(t, body, secret)
		}
	}
}

func TestCreativeImageTaskHandlersRejectAccessTokenOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name    string
		method  string
		target  string
		params  gin.Params
		handler gin.HandlerFunc
	}{
		{name: "submit", method: http.MethodPost, target: "/creative/relay/v1/images/tasks", handler: CreativeRelayImageTaskSubmit},
		{name: "fetch", method: http.MethodGet, target: "/creative/relay/v1/images/tasks/task_abc", params: gin.Params{{Key: "task_id", Value: "task_abc"}}, handler: CreativeRelayImageTaskFetch},
		{name: "content", method: http.MethodGet, target: "/creative/relay/v1/images/tasks/task_abc/content", params: gin.Params{{Key: "task_id", Value: "task_abc"}}, handler: CreativeRelayImageTaskContent},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(tt.method, tt.target, strings.NewReader(`{"model":"mock:gpt-image-2:preview"}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			ctx.Params = tt.params
			ctx.Set("id", 806)
			ctx.Set("group", "default")
			ctx.Set("use_access_token", true)

			tt.handler(ctx)

			require.Equal(t, http.StatusForbidden, recorder.Code)
			errorObject := creativeResponseObject(t, decodeCreativeResponse(t, recorder), "error")
			require.Contains(t, errorObject["message"], "browser session")
		})
	}
}

func TestCreativeImageTaskAcceptedInsertFailureKeepsIdempotencyGuard(t *testing.T) {
	setupCreativeControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Task{}, &model.CreativeVideoIdempotency{}))
	seedCreativeControllerUser(t, 805)
	withCreativeImageTaskMockBinding(t, true, []string{"default"})
	previousInsert := creativeImageTaskInsert
	insertCount := 0
	creativeImageTaskInsert = func(task *model.Task) error {
		insertCount++
		return fmt.Errorf("forced insert failure")
	}
	t.Cleanup(func() { creativeImageTaskInsert = previousInsert })

	router := newCreativeRelayBrokerTestRouter(t, 805, func(c *gin.Context) {})
	auth := bootstrapCreativeSessionAuth(t, router)
	headers := creativeSameOriginNonceHeaders(auth)
	headers["Idempotency-Key"] = "image-insert-fail"
	recorder := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/tasks", map[string]any{
		"model":  "mock:gpt-image-2:preview",
		"prompt": "safe mock image",
	}, auth.cookies, headers)
	require.Equal(t, http.StatusInternalServerError, recorder.Code)

	var record model.CreativeVideoIdempotency
	require.NoError(t, model.DB.Where("user_id = ? AND scope = ? AND request_id = ?", 805, creativeImageTaskIdempotencyScope, "image-insert-fail").First(&record).Error)
	require.NotEmpty(t, record.TaskID)

	replay := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/tasks", map[string]any{
		"model":  "mock:gpt-image-2:preview",
		"prompt": "safe mock image",
	}, auth.cookies, headers)
	require.Equal(t, http.StatusConflict, replay.Code)
	require.Equal(t, 1, insertCount)
}

func TestCreativeImageTaskAcceptedFinalizeFailuresReplayExistingTask(t *testing.T) {
	setupCreativeControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Task{}, &model.CreativeVideoIdempotency{}))
	seedCreativeControllerUser(t, 809)
	withCreativeImageTaskMockBinding(t, true, []string{"default"})

	for _, tt := range []struct {
		name        string
		requestID   string
		installFail func()
		resetHook   func()
		wantMessage string
	}{
		{
			name:      "idempotency complete failure",
			requestID: "image-complete-fail",
			installFail: func() {
				creativeImageTaskCompleteIdempotency = func(userID int, scope string, requestID string, taskID string) error {
					return fmt.Errorf("forced idempotency complete failure")
				}
			},
			resetHook: func() {
				creativeImageTaskCompleteIdempotency = model.CompleteCreativeVideoIdempotencyScoped
			},
			wantMessage: "idempotency",
		},
		{
			name:      "accepted finalize failure",
			requestID: "image-finalize-fail",
			installFail: func() {
				creativeImageTaskFinalizeAccepted = func(c *gin.Context, task *model.Task) error {
					return fmt.Errorf("forced finalize failure")
				}
			},
			resetHook: func() {
				creativeImageTaskFinalizeAccepted = func(c *gin.Context, task *model.Task) error { return nil }
			},
			wantMessage: "finalize",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.installFail()
			t.Cleanup(tt.resetHook)
			router := newCreativeRelayBrokerTestRouter(t, 809, func(c *gin.Context) {
				t.Fatal("image task route must not use distributed provider relay")
			})
			auth := bootstrapCreativeSessionAuth(t, router)
			headers := creativeSameOriginNonceHeaders(auth)
			headers["Idempotency-Key"] = tt.requestID
			body := map[string]any{
				"model":  "mock:gpt-image-2:preview",
				"prompt": "safe mock image " + tt.requestID,
			}

			failed := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/tasks", body, auth.cookies, headers)
			require.Equal(t, http.StatusInternalServerError, failed.Code)
			require.Contains(t, creativeResponseObject(t, decodeCreativeResponse(t, failed), "error")["message"], tt.wantMessage)

			var record model.CreativeVideoIdempotency
			require.NoError(t, model.DB.Where("user_id = ? AND scope = ? AND request_id = ?", 809, creativeImageTaskIdempotencyScope, tt.requestID).First(&record).Error)
			require.NotEmpty(t, record.TaskID)
			var taskCount int64
			require.NoError(t, model.DB.Model(&model.Task{}).Where("user_id = ? AND task_id = ?", 809, record.TaskID).Count(&taskCount).Error)
			require.Equal(t, int64(1), taskCount)

			tt.resetHook()
			replay := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/tasks", body, auth.cookies, headers)
			require.Equal(t, http.StatusOK, replay.Code)
			require.Equal(t, record.TaskID, decodeCreativeResponse(t, replay)["task_id"])
			require.NoError(t, model.DB.Model(&model.Task{}).Where("user_id = ? AND task_id = ?", 809, record.TaskID).Count(&taskCount).Error)
			require.Equal(t, int64(1), taskCount)
		})
	}
}

func TestCreativeImageTaskSourceHasNoProviderTransportReferences(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	sourceFile := filepath.Join(filepath.Dir(thisFile), "creative_image_tasks.go")
	source, err := os.ReadFile(sourceFile)
	require.NoError(t, err)
	text := string(source)
	for _, forbidden := range []string{
		"CreativeRelaySessionBroker",
		"Distribute(",
		"http.NewRequest",
		"DoRequest(",
		"ChannelBaseUrl",
		"ApiKey",
		"Authorization",
		"baseURL",
		"Duomi",
		"GrsAI",
		"duomi",
		"grsai",
	} {
		require.NotContains(t, text, forbidden)
	}
}

func TestCreativeSubmitIdempotencyKeepsRecordAfterProviderAcceptedLocalFailure(t *testing.T) {
	setupCreativeControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Task{}, &model.CreativeVideoIdempotency{}))

	tests := []struct {
		name      string
		path      string
		body      string
		requestID string
		scope     string
		install   func(*gin.Engine, gin.HandlerFunc)
	}{
		{
			name:      "video",
			path:      "/creative/relay/v1/videos",
			body:      `{"model":"creative-model-05","prompt":"safe video"}`,
			requestID: "video-provider-accepted-local-failure",
			scope:     model.CreativeVideoIdempotencyScopeVideoSubmit,
			install: func(router *gin.Engine, handler gin.HandlerFunc) {
				router.POST("/creative/relay/v1/videos", CreativeVideoSubmitIdempotency(), handler)
			},
		},
		{
			name:      "suno",
			path:      "/creative/relay/v1/suno/submit/music",
			body:      `{"prompt":"safe song"}`,
			requestID: "suno-provider-accepted-local-failure",
			scope:     "suno.submit.music",
			install: func(router *gin.Engine, handler gin.HandlerFunc) {
				router.POST("/creative/relay/v1/suno/submit/:action", CreativeSunoSubmitGuard(), handler)
			},
		},
		{
			name:      "mj",
			path:      "/creative/relay/v1/mj/submit/imagine",
			body:      `{"prompt":"safe image"}`,
			requestID: "mj-provider-accepted-local-failure",
			scope:     creativeMJSubmitScopeImagine,
			install: func(router *gin.Engine, handler gin.HandlerFunc) {
				router.POST("/creative/relay/v1/mj/submit/imagine", CreativeMJSubmitImagineGuard(), handler)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			router := gin.New()
			router.Use(middleware.BodyStorageCleanup())
			router.Use(func(c *gin.Context) {
				c.Set("id", 91)
				c.Next()
			})
			tt.install(router, func(c *gin.Context) {
				c.Set("creative_task_provider_accepted", true)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "local persistence failure"})
			})

			request := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", tt.requestID)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusInternalServerError, recorder.Code)
			var count int64
			require.NoError(t, model.DB.Model(&model.CreativeVideoIdempotency{}).
				Where("user_id = ? AND scope = ? AND request_id = ?", 91, tt.scope, tt.requestID).
				Count(&count).Error)
			require.Equal(t, int64(1), count)
		})
	}
}

func TestCreativeSunoSubmitRequiresIdempotencyAndInfersGroupBeforeRelay(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 70)
	seedCreativeControllerModelPool(t)
	seedCreativeControllerSunoModelPool(t)

	relayReachedCount := 0
	router := newCreativeRelayBrokerTestRouter(t, 70, func(c *gin.Context) {
		relayReachedCount++
		bodyBytes, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		var payload map[string]any
		require.NoError(t, common.Unmarshal(bodyBytes, &payload))
		c.JSON(http.StatusOK, gin.H{
			"bodyModel":  payload["model"],
			"action":     c.Param("action"),
			"usingGroup": common.GetContextKeyString(c, constant.ContextKeyUsingGroup),
			"tokenGroup": common.GetContextKeyString(c, constant.ContextKeyTokenGroup),
		})
	})
	auth := bootstrapCreativeSessionAuth(t, router)

	missingKey := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/suno/submit/music", map[string]any{
		"prompt": "safe song prompt",
	}, auth.cookies, creativeSameOriginNonceHeaders(auth))
	require.Equal(t, http.StatusBadRequest, missingKey.Code)
	require.Equal(t, 0, relayReachedCount)
	missingKeyError := creativeResponseObject(t, decodeCreativeResponse(t, missingKey), "error")
	require.Contains(t, missingKeyError["message"], "Idempotency-Key")

	headers := creativeSameOriginNonceHeaders(auth)
	headers["Idempotency-Key"] = "suno-music-submit-1"
	accepted := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/suno/submit/music", map[string]any{
		"prompt": "safe song prompt",
	}, auth.cookies, headers)
	require.Equal(t, http.StatusOK, accepted.Code)
	require.Equal(t, 1, relayReachedCount)
	payload := decodeCreativeResponse(t, accepted)
	require.Nil(t, payload["bodyModel"])
	require.Equal(t, "music", payload["action"])
	require.Equal(t, "vip", payload["usingGroup"])
	require.Equal(t, "vip", payload["tokenGroup"])

	var record model.CreativeVideoIdempotency
	require.NoError(t, model.DB.Where("user_id = ? AND scope = ? AND request_id = ?", 70, "suno.submit.music", "suno-music-submit-1").First(&record).Error)
	require.Equal(t, "suno.submit.music", record.Scope)
	require.NotEmpty(t, record.TaskID)
}

func TestCreativeSunoSubmitRejectsBrowserSuppliedModelBeforeRelay(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 71)
	seedCreativeControllerModelPool(t)
	seedCreativeControllerSunoModelPool(t)

	relayReachedCount := 0
	router := newCreativeRelayBrokerTestRouter(t, 71, func(c *gin.Context) {
		relayReachedCount++
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
	auth := bootstrapCreativeSessionAuth(t, router)
	headers := creativeSameOriginNonceHeaders(auth)
	headers["Idempotency-Key"] = "suno-model-override"

	recorder := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/suno/submit/music", map[string]any{
		"model":  "suno_lyrics",
		"prompt": "safe song prompt",
	}, auth.cookies, headers)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, 0, relayReachedCount)
	errorObject := creativeResponseObject(t, decodeCreativeResponse(t, recorder), "error")
	require.Contains(t, errorObject["message"], "forbidden field model")
}

func TestCreativeSunoSubmitIdempotencyIsScopedByActionAndReplaysPublicTask(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 72)
	seedCreativeControllerSunoModelPool(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Task{}, &model.CreativeVideoIdempotency{}))
	emptyBodyHash := creativeTestPayloadHash([]byte("{}"))

	music, existed, err := model.PrepareCreativeVideoIdempotencyScoped(72, "suno.submit.music", "same-request", emptyBodyHash)
	require.NoError(t, err)
	require.False(t, existed)
	require.NoError(t, model.DB.Create(&model.Task{TaskID: music.TaskID, UserId: 72, Status: model.TaskStatusSubmitted, ChannelId: 1, Platform: constant.TaskPlatformSuno}).Error)

	lyrics, existed, err := model.PrepareCreativeVideoIdempotencyScoped(72, "suno.submit.lyrics", "same-request", "hash-lyrics")
	require.NoError(t, err)
	require.False(t, existed)
	require.NotEqual(t, music.TaskID, lyrics.TaskID)

	router := newCreativeRelayBrokerTestRouter(t, 72, func(c *gin.Context) {
		c.JSON(http.StatusInternalServerError, gin.H{"unexpected": "relay should not be reached on replay"})
	})
	auth := bootstrapCreativeSessionAuth(t, router)
	headers := creativeSameOriginNonceHeaders(auth)
	headers["Idempotency-Key"] = "same-request"
	replay := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/suno/submit/music", map[string]any{}, auth.cookies, headers)

	require.Equal(t, http.StatusOK, replay.Code)
	var replayPayload dto.TaskResponse[string]
	require.NoError(t, common.Unmarshal(replay.Body.Bytes(), &replayPayload))
	require.Equal(t, dto.TaskSuccessCode, replayPayload.Code)
	require.Equal(t, music.TaskID, replayPayload.Data)
}

func TestCreativeMJSubmitRequiresIdempotencyAndInfersGroupBeforeRelay(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 74)
	seedCreativeControllerMJModelPool(t)

	relayReachedCount := 0
	router := newCreativeRelayBrokerTestRouter(t, 74, func(c *gin.Context) {
		relayReachedCount++
		bodyBytes, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		var payload map[string]any
		require.NoError(t, common.Unmarshal(bodyBytes, &payload))
		c.JSON(http.StatusOK, gin.H{
			"bodyModel":  payload["model"],
			"usingGroup": common.GetContextKeyString(c, constant.ContextKeyUsingGroup),
			"tokenGroup": common.GetContextKeyString(c, constant.ContextKeyTokenGroup),
			"platform":   c.GetString("platform"),
		})
	})
	auth := bootstrapCreativeSessionAuth(t, router)

	missingKey := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/mj/submit/imagine", map[string]any{
		"botType": "MID_JOURNEY",
		"prompt":  "safe image prompt",
	}, auth.cookies, creativeSameOriginNonceHeaders(auth))
	require.Equal(t, http.StatusBadRequest, missingKey.Code)
	require.Equal(t, 0, relayReachedCount)
	missingKeyError := creativeResponseObject(t, decodeCreativeResponse(t, missingKey), "error")
	require.Contains(t, missingKeyError["message"], "Idempotency-Key")

	headers := creativeSameOriginNonceHeaders(auth)
	headers["Idempotency-Key"] = "mj-imagine-submit-1"
	accepted := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/mj/submit/imagine", map[string]any{
		"botType": "MID_JOURNEY",
		"prompt":  "safe image prompt",
	}, auth.cookies, headers)
	require.Equal(t, http.StatusOK, accepted.Code)
	require.Equal(t, 1, relayReachedCount)
	payload := decodeCreativeResponse(t, accepted)
	require.Nil(t, payload["bodyModel"])
	require.Equal(t, "vip", payload["usingGroup"])
	require.Equal(t, "vip", payload["tokenGroup"])
	require.Equal(t, string(constant.TaskPlatformMidjourney), payload["platform"])

	var record model.CreativeVideoIdempotency
	require.NoError(t, model.DB.Where("user_id = ? AND scope = ? AND request_id = ?", 74, creativeMJSubmitScopeImagine, "mj-imagine-submit-1").First(&record).Error)
	require.Equal(t, creativeMJSubmitScopeImagine, record.Scope)
	require.NotEmpty(t, record.TaskID)
}

func TestCreativeMJSubmitRejectsBrowserModelAndNotifyHookBeforeRelay(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 75)
	seedCreativeControllerMJModelPool(t)

	relayReachedCount := 0
	router := newCreativeRelayBrokerTestRouter(t, 75, func(c *gin.Context) {
		relayReachedCount++
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
	auth := bootstrapCreativeSessionAuth(t, router)
	headers := creativeSameOriginNonceHeaders(auth)
	headers["Idempotency-Key"] = "mj-forbidden"

	modelOverride := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/mj/submit/imagine", map[string]any{
		"model":  "mj_describe",
		"prompt": "safe image prompt",
	}, auth.cookies, headers)
	require.Equal(t, http.StatusBadRequest, modelOverride.Code)
	require.Equal(t, 0, relayReachedCount)
	modelError := creativeResponseObject(t, decodeCreativeResponse(t, modelOverride), "error")
	require.Contains(t, modelError["message"], "forbidden field model")

	headers["Idempotency-Key"] = "mj-notify-hook"
	notifyHook := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/mj/submit/imagine", map[string]any{
		"prompt":     "safe image prompt",
		"notifyHook": "https://evil.example/callback",
	}, auth.cookies, headers)
	require.Equal(t, http.StatusBadRequest, notifyHook.Code)
	require.Equal(t, 0, relayReachedCount)
	notifyError := creativeResponseObject(t, decodeCreativeResponse(t, notifyHook), "error")
	require.Contains(t, notifyError["message"], "forbidden field notifyHook")
}

func TestCreativeSunoSubmitRejectsNotifyCallbackOwnerAliasesBeforeRelay(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 7501)
	seedCreativeControllerSunoModelPool(t)

	relayReachedCount := 0
	router := newCreativeRelayBrokerTestRouter(t, 7501, func(c *gin.Context) {
		relayReachedCount++
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
	auth := bootstrapCreativeSessionAuth(t, router)

	tests := []struct {
		name      string
		body      map[string]any
		wantField string
	}{
		{
			name: "notifyHook top level",
			body: map[string]any{
				"prompt":     "safe song prompt",
				"notifyHook": "https://evil.example/callback",
			},
			wantField: "notifyHook",
		},
		{
			name: "notify hook snake nested",
			body: map[string]any{
				"prompt": "safe song prompt",
				"params": map[string]any{"notify_hook": "https://evil.example/callback"},
			},
			wantField: "params.notify_hook",
		},
		{
			name: "bare notify top level",
			body: map[string]any{
				"prompt": "safe song prompt",
				"notify": "https://evil.example/callback",
			},
			wantField: "notify",
		},
		{
			name: "callback top level",
			body: map[string]any{
				"prompt":   "safe song prompt",
				"callback": "https://evil.example/callback",
			},
			wantField: "callback",
		},
		{
			name: "webhook nested",
			body: map[string]any{
				"prompt": "safe song prompt",
				"params": map[string]any{"webhook": "https://evil.example/callback"},
			},
			wantField: "params.webhook",
		},
		{
			name: "owner id alias",
			body: map[string]any{
				"prompt":  "safe song prompt",
				"ownerId": 999,
			},
			wantField: "ownerId",
		},
		{
			name: "user id snake alias",
			body: map[string]any{
				"prompt":  "safe song prompt",
				"user_id": 999,
			},
			wantField: "user_id",
		},
		{
			name: "api secret alias",
			body: map[string]any{
				"prompt":    "safe song prompt",
				"apiSecret": "secret-value",
			},
			wantField: "apiSecret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := creativeSameOriginNonceHeaders(auth)
			headers["Idempotency-Key"] = "suno-forbidden-" + strings.NewReplacer(" ", "-", "_", "-", ".", "-").Replace(tt.name)
			recorder := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/suno/submit/music", tt.body, auth.cookies, headers)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			errorObject := creativeResponseObject(t, decodeCreativeResponse(t, recorder), "error")
			require.Contains(t, errorObject["message"], "forbidden field "+tt.wantField)
		})
	}
	require.Equal(t, 0, relayReachedCount)
}

func TestCreativeRelayRejectsForbiddenAliasesInHeaderQueryFormAndFileNames(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 7502)
	seedCreativeControllerModelPool(t)

	relayReachedCount := 0
	router := newCreativeRelayBrokerTestRouter(t, 7502, func(c *gin.Context) {
		relayReachedCount++
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
	auth := bootstrapCreativeSessionAuth(t, router)

	headerCaseHeaders := creativeSameOriginNonceHeaders(auth)
	headerCaseHeaders["X-Notify-Hook"] = "https://evil.example/callback"
	headerCase := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/generations", map[string]any{
		"model":  "creative-model-05",
		"prompt": "safe image prompt",
	}, auth.cookies, headerCaseHeaders)
	require.Equal(t, http.StatusBadRequest, headerCase.Code)
	require.Contains(t, creativeResponseObject(t, decodeCreativeResponse(t, headerCase), "error")["message"], "forbidden field X-Notify-Hook")

	bareNotifyHeaderCaseHeaders := creativeSameOriginNonceHeaders(auth)
	bareNotifyHeaderCaseHeaders["X-Notify"] = "https://evil.example/bare-notify"
	bareNotifyHeaderCase := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/generations", map[string]any{
		"model":  "creative-model-05",
		"prompt": "safe image prompt",
	}, auth.cookies, bareNotifyHeaderCaseHeaders)
	require.Equal(t, http.StatusBadRequest, bareNotifyHeaderCase.Code)
	require.Contains(t, creativeResponseObject(t, decodeCreativeResponse(t, bareNotifyHeaderCase), "error")["message"], "forbidden field X-Notify")

	queryCase := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/generations?ownerId=999", map[string]any{
		"model":  "creative-model-05",
		"prompt": "safe image prompt",
	}, auth.cookies, creativeSameOriginNonceHeaders(auth))
	require.Equal(t, http.StatusBadRequest, queryCase.Code)
	require.Contains(t, creativeResponseObject(t, decodeCreativeResponse(t, queryCase), "error")["message"], "forbidden field ownerId")

	textJSONRequest := httptest.NewRequest(http.MethodPost, "/creative/relay/v1/images/generations", bytes.NewBufferString(`{"model":"creative-model-05","prompt":"safe image prompt","callback":"https://evil.example/cb"}`))
	textJSONRequest.Header.Set("Content-Type", "text/plain")
	for _, cookie := range auth.cookies {
		textJSONRequest.AddCookie(cookie)
	}
	for key, value := range creativeSameOriginNonceHeaders(auth) {
		textJSONRequest.Header.Set(key, value)
	}
	textJSONCase := httptest.NewRecorder()
	router.ServeHTTP(textJSONCase, textJSONRequest)
	require.Equal(t, http.StatusBadRequest, textJSONCase.Code)
	require.Contains(t, creativeResponseObject(t, decodeCreativeResponse(t, textJSONCase), "error")["message"], "forbidden field callback")

	formCase := performCreativeSessionMultipart(t, router, http.MethodPost, "/creative/relay/v1/images/generations", auth.cookies, creativeSameOriginNonceHeaders(auth), map[string]string{
		"model":         "creative-model-05",
		"prompt":        "safe image prompt",
		"mj-api-secret": "leaked-secret",
	})
	require.Equal(t, http.StatusBadRequest, formCase.Code)
	require.Contains(t, creativeResponseObject(t, decodeCreativeResponse(t, formCase), "error")["message"], "forbidden field mj-api-secret")

	fileCase := performCreativeSessionMultipartWithFiles(t, router, http.MethodPost, "/creative/relay/v1/images/generations", auth.cookies, creativeSameOriginNonceHeaders(auth), map[string]string{
		"model":  "creative-model-05",
		"prompt": "safe image prompt",
	}, map[string]string{
		"callback": "file contents",
	})
	require.Equal(t, http.StatusBadRequest, fileCase.Code)
	require.Contains(t, creativeResponseObject(t, decodeCreativeResponse(t, fileCase), "error")["message"], "forbidden field callback")

	require.Equal(t, 0, relayReachedCount)
}

func TestCreativeForbiddenNormalizerMatrixCoversAdminSchemaDryRunAndRelay(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 7503)
	seedCreativeControllerModelPool(t)

	dangerousKeys := []string{
		"notifyHook",
		"ownerId",
		"callback_url",
		"x_upstream_base_url",
		"providerOverride",
		"channelId",
		"modelName",
	}
	for _, key := range dangerousKeys {
		t.Run("service "+key, func(t *testing.T) {
			require.True(t, service.CreativeForbiddenKey(key), key)
			require.Error(t, service.ValidateCreativeParameterSchema([]dto.CreativeParameterSchemaItem{{
				Id:    key,
				Label: "Unsafe",
				Type:  "string",
			}}))
			raw, err := common.Marshal(map[string]any{
				"version": 1,
				"bindings": []any{map[string]any{
					"id":                "mock:image:matrix",
					"providerModelId":   "p",
					"priceModelId":      "price",
					"modality":          "image",
					"adapterPreset":     "mock_image_task",
					"parameterTemplate": "mock_gpt_image",
					key:                 "unsafe",
				}},
			})
			require.NoError(t, err)
			_, err = service.ParseCreativeModelBindingsConfig(string(raw))
			require.Error(t, err)
			_, err = service.ValidateCreativeUserParamsForSchema([]dto.CreativeParameterSchemaItem{{
				Id:    "size",
				Label: "Size",
				Type:  "string",
			}}, map[string]any{key: "unsafe"})
			require.Error(t, err)
			redacted := service.RedactCreativeDryRunValue(map[string]any{key: "unsafe"}).(map[string]any)
			require.Equal(t, "[REDACTED]", redacted[key])
		})
	}
	_, err := service.ValidateCreativeUserParamsForSchema([]dto.CreativeParameterSchemaItem{{
		Id:     "serverOnly",
		Label:  "Server Only",
		Type:   "string",
		Hidden: true,
	}}, map[string]any{"serverOnly": "unsafe"})
	require.Error(t, err)

	relayReachedCount := 0
	router := newCreativeRelayBrokerTestRouter(t, 7503, func(c *gin.Context) {
		relayReachedCount++
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
	auth := bootstrapCreativeSessionAuth(t, router)
	for _, key := range dangerousKeys {
		t.Run("relay json "+key, func(t *testing.T) {
			recorder := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/generations", map[string]any{
				"model":  "creative-model-05",
				"prompt": "safe image prompt",
				"params": map[string]any{key: "unsafe"},
			}, auth.cookies, creativeSameOriginNonceHeaders(auth))
			require.Equal(t, http.StatusBadRequest, recorder.Code)
		})
		t.Run("relay query "+key, func(t *testing.T) {
			target := "/creative/relay/v1/images/generations?" + url.Values{key: []string{"unsafe"}}.Encode()
			recorder := performCreativeSessionJSON(t, router, http.MethodPost, target, map[string]any{
				"model":  "creative-model-05",
				"prompt": "safe image prompt",
			}, auth.cookies, creativeSameOriginNonceHeaders(auth))
			require.Equal(t, http.StatusBadRequest, recorder.Code)
		})
		t.Run("relay form "+key, func(t *testing.T) {
			recorder := performCreativeSessionForm(t, router, http.MethodPost, "/creative/relay/v1/images/generations", auth.cookies, creativeSameOriginNonceHeaders(auth), map[string]string{
				"model":  "creative-model-05",
				"prompt": "safe image prompt",
				key:      "unsafe",
			})
			require.Equal(t, http.StatusBadRequest, recorder.Code)
		})
		t.Run("relay multipart field "+key, func(t *testing.T) {
			recorder := performCreativeSessionMultipart(t, router, http.MethodPost, "/creative/relay/v1/images/generations", auth.cookies, creativeSameOriginNonceHeaders(auth), map[string]string{
				"model":  "creative-model-05",
				"prompt": "safe image prompt",
				key:      "unsafe",
			})
			require.Equal(t, http.StatusBadRequest, recorder.Code)
		})
		t.Run("relay multipart file "+key, func(t *testing.T) {
			recorder := performCreativeSessionMultipartWithFiles(t, router, http.MethodPost, "/creative/relay/v1/images/generations", auth.cookies, creativeSameOriginNonceHeaders(auth), map[string]string{
				"model":  "creative-model-05",
				"prompt": "safe image prompt",
			}, map[string]string{key: "file contents"})
			require.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}
	require.Equal(t, 0, relayReachedCount)
}

func TestCreativeMJSubmitIdempotencyIsScopedAndReplaysPublicTask(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 76)
	seedCreativeControllerModelPool(t)
	seedCreativeControllerMJModelPool(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Task{}, &model.CreativeVideoIdempotency{}))
	emptyBodyHash := creativeTestPayloadHash([]byte("{}"))

	mjRecord, existed, err := model.PrepareCreativeVideoIdempotencyScoped(76, creativeMJSubmitScopeImagine, "same-mj-request", emptyBodyHash)
	require.NoError(t, err)
	require.False(t, existed)
	require.NoError(t, model.DB.Create(&model.Task{TaskID: mjRecord.TaskID, UserId: 76, Status: model.TaskStatusSubmitted, ChannelId: 1, Platform: constant.TaskPlatformMidjourney}).Error)

	videoRecord, existed, err := model.PrepareCreativeVideoIdempotencyScoped(76, "video.submit", "same-mj-request", "hash-video")
	require.NoError(t, err)
	require.False(t, existed)
	require.NotEqual(t, mjRecord.TaskID, videoRecord.TaskID)

	router := newCreativeRelayBrokerTestRouter(t, 76, func(c *gin.Context) {
		c.JSON(http.StatusInternalServerError, gin.H{"unexpected": "relay should not be reached on replay"})
	})
	auth := bootstrapCreativeSessionAuth(t, router)
	headers := creativeSameOriginNonceHeaders(auth)
	headers["Idempotency-Key"] = "same-mj-request"
	replay := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/mj/submit/imagine", map[string]any{}, auth.cookies, headers)

	require.Equal(t, http.StatusOK, replay.Code)
	var replayPayload dto.MidjourneyResponse
	require.NoError(t, common.Unmarshal(replay.Body.Bytes(), &replayPayload))
	require.Equal(t, 1, replayPayload.Code)
	require.Equal(t, mjRecord.TaskID, replayPayload.Result)

	conflict := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/mj/submit/imagine", map[string]any{
		"prompt": "different payload",
	}, auth.cookies, headers)
	require.Equal(t, http.StatusConflict, conflict.Code)
	conflictError := creativeResponseObject(t, decodeCreativeResponse(t, conflict), "error")
	require.Contains(t, conflictError["message"], "conflicts with a different payload")
}

func TestCreativeRelayMJFetchIsOwnerScopedAndSanitized(t *testing.T) {
	setupCreativeControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Task{}))
	require.NoError(t, model.DB.Create(&model.Task{
		TaskID:    "task_mj_owner",
		UserId:    77,
		Status:    model.TaskStatusSuccess,
		ChannelId: 1,
		Platform:  constant.TaskPlatformMidjourney,
		Action:    constant.MjActionImagine,
		Progress:  "100%",
		PrivateData: model.TaskPrivateData{
			Key:            "sk-selected-secret",
			UpstreamTaskID: "upstream-mj-secret",
			ResultURL:      "https://upstream.example/private-image.png",
		},
		Data: []byte(`{"id":"upstream-mj-secret","prompt":"safe","imageUrl":"https://upstream.example/private-image.png","videoUrl":"https://upstream.example/private-video.mp4?X-Amz-Signature=secret"}`),
	}).Error)
	require.NoError(t, model.DB.Create(&model.Task{
		TaskID:    "task_mj_other_user",
		UserId:    7702,
		Status:    model.TaskStatusSuccess,
		ChannelId: 1,
		Platform:  constant.TaskPlatformMidjourney,
		Progress:  "100%",
	}).Error)

	performFetch := func(userID int, taskID string) *httptest.ResponseRecorder {
		gin.SetMode(gin.TestMode)
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/creative/relay/v1/mj/task/"+taskID+"/fetch", nil)
		ctx.Params = gin.Params{{Key: "task_id", Value: taskID}}
		ctx.Set("id", userID)
		ctx.Set("group", "default")
		ctx.Set("relay_mode", relayconstant.RelayModeMidjourneyTaskFetch)
		common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "default")
		common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "default")
		CreativeRelayMJFetch(ctx)
		return recorder
	}

	sameUser := performFetch(77, "task_mj_owner")
	require.Equal(t, http.StatusOK, sameUser.Code)
	var samePayload dto.MidjourneyDto
	require.NoError(t, common.Unmarshal(sameUser.Body.Bytes(), &samePayload))
	require.Equal(t, "task_mj_owner", samePayload.MjId)
	require.Equal(t, "/creative/relay/v1/mj/image/task_mj_owner", samePayload.ImageUrl)
	require.Empty(t, samePayload.VideoUrl)
	require.NotContains(t, sameUser.Body.String(), "sk-selected-secret")
	require.NotContains(t, sameUser.Body.String(), "upstream-mj-secret")
	require.NotContains(t, sameUser.Body.String(), "upstream.example")
	require.NotContains(t, sameUser.Body.String(), "private-video")

	crossUser := performFetch(77, "task_mj_other_user")
	require.Equal(t, http.StatusNotFound, crossUser.Code)
	require.Contains(t, crossUser.Body.String(), "task_not_found")
	require.NotContains(t, crossUser.Body.String(), "7702")

	missing := performFetch(77, "task_mj_missing")
	require.Equal(t, http.StatusNotFound, missing.Code)
	require.Contains(t, missing.Body.String(), "task_not_found")
}

func TestCreativeRelayMJImageIsOwnerScopedAndPrivate(t *testing.T) {
	setupCreativeControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Task{}, &model.Channel{}))

	hits := 0
	imageURL := "https://cdn.example/image.png"
	previousHTTPClient := creativeMJImageHTTPClient
	creativeMJImageHTTPClient = func() *http.Client {
		return &http.Client{Transport: creativeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			hits++
			require.Equal(t, imageURL, r.URL.String())
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"image/png"}},
				Body:       io.NopCloser(strings.NewReader("image-bytes")),
				Request:    r,
			}, nil
		})}
	}
	t.Cleanup(func() { creativeMJImageHTTPClient = previousHTTPClient })
	fetchSetting := system_setting.GetFetchSetting()
	previousSSRFProtection := fetchSetting.EnableSSRFProtection
	fetchSetting.EnableSSRFProtection = false
	t.Cleanup(func() { fetchSetting.EnableSSRFProtection = previousSSRFProtection })

	require.NoError(t, model.DB.Create(&model.Channel{
		Id:     3,
		Type:   constant.ChannelTypeMidjourney,
		Key:    "sk-fresh-mj-key",
		Status: common.ChannelStatusEnabled,
		Name:   "mj-image-proxy",
	}).Error)
	require.NoError(t, model.DB.Create(&model.Task{
		TaskID:    "task_mj_image_owner",
		UserId:    78,
		Status:    model.TaskStatusSuccess,
		ChannelId: 3,
		Platform:  constant.TaskPlatformMidjourney,
		Progress:  "100%",
		PrivateData: model.TaskPrivateData{
			ResultURL: imageURL,
		},
	}).Error)
	require.NoError(t, model.DB.Create(&model.Task{
		TaskID:    "task_mj_image_other",
		UserId:    7802,
		Status:    model.TaskStatusSuccess,
		ChannelId: 3,
		Platform:  constant.TaskPlatformMidjourney,
		Progress:  "100%",
		PrivateData: model.TaskPrivateData{
			ResultURL: imageURL,
		},
	}).Error)
	require.NoError(t, model.DB.Create(&model.Task{
		TaskID:    "task_mj_no_image",
		UserId:    78,
		Status:    model.TaskStatusSuccess,
		ChannelId: 3,
		Platform:  constant.TaskPlatformMidjourney,
		Progress:  "100%",
	}).Error)

	performImage := func(userID int, taskID string) *httptest.ResponseRecorder {
		gin.SetMode(gin.TestMode)
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/creative/relay/v1/mj/image/"+taskID, nil)
		ctx.Params = gin.Params{{Key: "task_id", Value: taskID}}
		ctx.Set("id", userID)
		ctx.Set("group", "default")
		ctx.Set("relay_mode", relayconstant.RelayModeMidjourneyImage)
		common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "default")
		common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "default")
		CreativeRelayMJImage(ctx)
		return recorder
	}

	sameUser := performImage(78, "task_mj_image_owner")
	require.Equal(t, http.StatusOK, sameUser.Code)
	require.Equal(t, "image-bytes", sameUser.Body.String())
	require.Equal(t, "private, no-store", sameUser.Header().Get("Cache-Control"))
	require.Equal(t, "no-cache", sameUser.Header().Get("Pragma"))
	require.Equal(t, "nosniff", sameUser.Header().Get("X-Content-Type-Options"))
	require.Equal(t, "image/png", sameUser.Header().Get("Content-Type"))
	require.Equal(t, 1, hits)

	crossUser := performImage(78, "task_mj_image_other")
	require.Equal(t, http.StatusNotFound, crossUser.Code)
	require.Contains(t, crossUser.Body.String(), "task_not_found")
	require.Equal(t, 1, hits)

	missingImage := performImage(78, "task_mj_no_image")
	require.Equal(t, http.StatusNotFound, missingImage.Code)
	require.Contains(t, missingImage.Body.String(), "image_not_found")
	require.Equal(t, 1, hits)
}

func TestCreativeRelayMJImageFallbackClientBlocksUnsafeRedirect(t *testing.T) {
	setupCreativeControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Task{}, &model.Channel{}))

	imageURL := "http://93.184.216.34/image.png"
	privateHits := 0
	previousTransport := http.DefaultTransport
	http.DefaultTransport = creativeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case imageURL:
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"http://127.0.0.1/private.png"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    r,
			}, nil
		case "http://127.0.0.1/private.png":
			privateHits++
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"image/png"}},
				Body:       io.NopCloser(strings.NewReader("private-image-bytes")),
				Request:    r,
			}, nil
		default:
			require.Failf(t, "unexpected request", "url=%s", r.URL.String())
			return nil, fmt.Errorf("unexpected request %s", r.URL.String())
		}
	})
	t.Cleanup(func() { http.DefaultTransport = previousTransport })

	previousHTTPClient := creativeMJImageHTTPClient
	creativeMJImageHTTPClient = func() *http.Client { return nil }
	t.Cleanup(func() { creativeMJImageHTTPClient = previousHTTPClient })

	fetchSetting := system_setting.GetFetchSetting()
	previousSSRFProtection := fetchSetting.EnableSSRFProtection
	previousAllowPrivate := fetchSetting.AllowPrivateIp
	fetchSetting.EnableSSRFProtection = true
	fetchSetting.AllowPrivateIp = false
	t.Cleanup(func() {
		fetchSetting.EnableSSRFProtection = previousSSRFProtection
		fetchSetting.AllowPrivateIp = previousAllowPrivate
	})

	require.NoError(t, model.DB.Create(&model.Channel{
		Id:     33,
		Type:   constant.ChannelTypeMidjourney,
		Key:    "sk-fresh-mj-key",
		Status: common.ChannelStatusEnabled,
		Name:   "mj-image-proxy-redirect",
	}).Error)
	require.NoError(t, model.DB.Create(&model.Task{
		TaskID:    "task_mj_image_redirect",
		UserId:    7803,
		Status:    model.TaskStatusSuccess,
		ChannelId: 33,
		Platform:  constant.TaskPlatformMidjourney,
		Progress:  "100%",
		PrivateData: model.TaskPrivateData{
			ResultURL: imageURL,
		},
	}).Error)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/creative/relay/v1/mj/image/task_mj_image_redirect", nil)
	ctx.Params = gin.Params{{Key: "task_id", Value: "task_mj_image_redirect"}}
	ctx.Set("id", 7803)
	ctx.Set("group", "default")
	ctx.Set("relay_mode", relayconstant.RelayModeMidjourneyImage)
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "default")

	CreativeRelayMJImage(ctx)

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.Equal(t, 0, privateHits)
	require.NotContains(t, recorder.Body.String(), "private-image-bytes")
}

func TestCreativeAPIRequestOriginIgnoresUntrustedForwardedHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "http://internal.example/creative/api/documents", nil)
	ctx.Request.Host = "internal.example"
	ctx.Request.Header.Set("X-Forwarded-Proto", "https")
	ctx.Request.Header.Set("X-Forwarded-Host", "evil.example")

	require.Equal(t, "http://internal.example", creativeAPIRequestOrigin(ctx))
}

func TestCreativeRelayMJUnsupportedIsExplicit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/creative/relay/v1/mj/submit/action", nil)

	CreativeRelayMJUnsupported(ctx)

	require.Equal(t, http.StatusNotImplemented, recorder.Code)
	var payload dto.MidjourneyResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, constant.MjErrorUnknown, payload.Code)
	require.Contains(t, payload.Description, "not supported")
}

func TestCreativeRelayMJUnsupportedRejectsAPITokenOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/creative/relay/v1/mj/submit/action", nil)
	ctx.Set("use_access_token", true)

	CreativeRelayMJUnsupported(ctx)

	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "browser session")
}

func TestCreativeRelaySunoFetchIsOwnerScoped(t *testing.T) {
	setupCreativeControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Task{}))
	require.NoError(t, model.DB.Create(&model.Task{
		TaskID:    "task_suno_owner",
		UserId:    73,
		Status:    model.TaskStatusSuccess,
		ChannelId: 111,
		Quota:     22222,
		Platform:  constant.TaskPlatformSuno,
		Progress:  "100%",
		Data:      []byte(`[{"id":"clip-1","audio_url":"https://cdn.example/clip-1.mp3","status":"complete"}]`),
		PrivateData: model.TaskPrivateData{
			ResultURL: "https://private.example/suno.mp3?X-Amz-Signature=secret",
		},
	}).Error)
	require.NoError(t, model.DB.Create(&model.Task{
		TaskID:    "task_suno_other_user",
		UserId:    7302,
		Status:    model.TaskStatusSuccess,
		ChannelId: 1,
		Platform:  constant.TaskPlatformSuno,
		Progress:  "100%",
	}).Error)
	require.NoError(t, model.DB.Create(&model.Task{
		TaskID:    "task_mj_same_user",
		UserId:    73,
		Status:    model.TaskStatusSuccess,
		ChannelId: 999,
		Quota:     12345,
		Platform:  constant.TaskPlatformMidjourney,
		Progress:  "100%",
		Data:      []byte(`{"imageUrl":"https://private.example/mj.png?X-Amz-Signature=secret"}`),
		PrivateData: model.TaskPrivateData{
			ResultURL: "https://private.example/mj.png?X-Amz-Signature=secret",
		},
	}).Error)

	performFetch := func(userID int, taskID string) *httptest.ResponseRecorder {
		gin.SetMode(gin.TestMode)
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/creative/relay/v1/suno/fetch/"+taskID, nil)
		ctx.Params = gin.Params{{Key: "id", Value: taskID}}
		ctx.Set("id", userID)
		ctx.Set("group", "default")
		ctx.Set("relay_mode", relayconstant.RelayModeSunoFetchByID)
		common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "default")
		common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "default")
		CreativeRelaySunoFetch(ctx)
		return recorder
	}

	sameUser := performFetch(73, "task_suno_owner")
	require.Equal(t, http.StatusOK, sameUser.Code)
	require.Contains(t, sameUser.Body.String(), "task_suno_owner")
	require.Contains(t, sameUser.Body.String(), "clip-1")
	require.NotContains(t, sameUser.Body.String(), "channel_id")
	require.NotContains(t, sameUser.Body.String(), "quota")
	require.NotContains(t, sameUser.Body.String(), "user_id")
	require.NotContains(t, sameUser.Body.String(), "result_url")
	require.NotContains(t, sameUser.Body.String(), "private.example")
	require.NotContains(t, sameUser.Body.String(), "22222")

	crossUser := performFetch(73, "task_suno_other_user")
	require.Equal(t, http.StatusBadRequest, crossUser.Code)
	require.Contains(t, crossUser.Body.String(), "task_not_exist")
	require.NotContains(t, crossUser.Body.String(), "7302")

	missing := performFetch(73, "task_suno_missing")
	require.Equal(t, http.StatusBadRequest, missing.Code)
	require.Contains(t, missing.Body.String(), "task_not_exist")

	nonSuno := performFetch(73, "task_mj_same_user")
	require.Equal(t, http.StatusBadRequest, nonSuno.Code)
	require.Contains(t, nonSuno.Body.String(), "task_not_exist")
	require.NotContains(t, nonSuno.Body.String(), "Midjourney")
	require.NotContains(t, nonSuno.Body.String(), "private.example")
	require.NotContains(t, nonSuno.Body.String(), "channel_id")
	require.NotContains(t, nonSuno.Body.String(), "12345")
}

func creativeTestPayloadHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func TestCreativeImageRelayRejectsNonceAndForbiddenFieldsBeforeSessionBroker(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 59)
	seedCreativeControllerModelPool(t)

	relayReachedCount := 0
	router := newCreativeRelayBrokerTestRouter(t, 59, func(c *gin.Context) {
		relayReachedCount++
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
	auth := bootstrapCreativeSessionAuth(t, router)
	body := map[string]any{
		"model":  "creative-model-05",
		"prompt": "draw a safe image",
	}

	missingNonce := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/generations", body, auth.cookies, nil)
	require.Equal(t, http.StatusForbidden, missingNonce.Code)
	require.Equal(t, 0, relayReachedCount)

	forbidden := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/generations", map[string]any{
		"model":   "creative-model-05",
		"prompt":  "draw a safe image",
		"baseUrl": "https://upstream.example",
	}, auth.cookies, creativeSameOriginNonceHeaders(auth))
	require.Equal(t, http.StatusBadRequest, forbidden.Code)
	require.Equal(t, 0, relayReachedCount)
	errorObject := creativeResponseObject(t, decodeCreativeResponse(t, forbidden), "error")
	require.Contains(t, errorObject["message"], "forbidden field baseUrl")

	allowed := performCreativeSessionJSON(t, router, http.MethodPost, "/creative/relay/v1/images/generations", body, auth.cookies, creativeSameOriginNonceHeaders(auth))
	require.Equal(t, http.StatusOK, allowed.Code)
	require.Equal(t, 1, relayReachedCount)
}

func TestCreativePreferenceAPIContractWrapsRevisionAndPreference(t *testing.T) {
	setupCreativeControllerTestDB(t)

	get := runCreativeHandler(t, CreativeGetModelPreference, http.MethodGet, "/creative/api/preferences/model", nil, 17, nil)
	require.Equal(t, http.StatusOK, get.Code)
	getPayload := decodeCreativeResponse(t, get)
	require.Equal(t, true, getPayload["success"])
	require.NotContains(t, getPayload, "revision")
	getData := creativeResponseData(t, getPayload)
	require.Equal(t, float64(0), getData["revision"])
	require.IsType(t, map[string]any{}, getData["preference"])

	patchStringRevision := runCreativeHandler(t, CreativePatchModelPreference, http.MethodPatch, "/creative/api/preferences/model", map[string]any{
		"baseRevision": "0",
		"preference": map[string]any{
			"default": map[string]any{
				"text": map[string]any{
					"modelId":        "gpt-4o",
					"profileId":      "profile-text",
					"providerIdHint": "provider-hint",
					"vendorHint":     "openai",
					"updatedAt":      float64(1700000000000),
					"extra":          "must-not-persist",
				},
			},
			"pinned":               []any{map[string]any{"modelId": "gpt-4o", "profileId": "profile-text", "extra": "must-not-persist"}},
			"recent":               []any{map[string]any{"modelId": "claude-3-5-sonnet", "vendorHint": "anthropic"}},
			"displayMode":          "compact",
			"customOrder":          []any{"claude-3-5-sonnet", "gpt-4o"},
			"defaultModel":         "must-not-persist",
			"order":                []any{"must-not-persist"},
			"group":                "must-not-persist",
			"defaultVisibleModels": []any{"must-not-persist"},
		},
	}, 17, nil)
	require.Equal(t, http.StatusOK, patchStringRevision.Code)
	patchStringPayload := decodeCreativeResponse(t, patchStringRevision)
	require.Equal(t, true, patchStringPayload["success"])
	require.NotContains(t, patchStringPayload, "revision")
	patchStringData := creativeResponseData(t, patchStringPayload)
	require.Equal(t, float64(1), patchStringData["revision"])
	preference := creativeResponseObject(t, patchStringData, "preference")
	defaultPreference := creativeResponseObject(t, preference, "default")
	textDefault := creativeResponseObject(t, defaultPreference, "text")
	require.Equal(t, "gpt-4o", textDefault["modelId"])
	require.Equal(t, "profile-text", textDefault["profileId"])
	require.Equal(t, "provider-hint", textDefault["providerIdHint"])
	require.Equal(t, "openai", textDefault["vendorHint"])
	require.Equal(t, float64(1700000000000), textDefault["updatedAt"])
	require.NotContains(t, textDefault, "extra")
	pinnedPreference := creativeResponseArray(t, preference, "pinned")
	require.Len(t, pinnedPreference, 1)
	firstPinned, ok := pinnedPreference[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "gpt-4o", firstPinned["modelId"])
	require.Equal(t, "profile-text", firstPinned["profileId"])
	require.NotContains(t, firstPinned, "extra")
	recentPreference := creativeResponseArray(t, preference, "recent")
	require.Len(t, recentPreference, 1)
	firstRecent, ok := recentPreference[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "claude-3-5-sonnet", firstRecent["modelId"])
	require.Equal(t, "anthropic", firstRecent["vendorHint"])
	require.Equal(t, "compact", preference["displayMode"])
	require.Equal(t, []any{"claude-3-5-sonnet", "gpt-4o"}, creativeResponseArray(t, preference, "customOrder"))
	require.NotContains(t, preference, "defaultModel")
	require.NotContains(t, preference, "order")
	require.NotContains(t, preference, "group")
	require.NotContains(t, preference, "defaultVisibleModels")

	patchNumericRevision := runCreativeHandler(t, CreativePatchModelPreference, http.MethodPatch, "/creative/api/preferences/model", map[string]any{
		"baseRevision": 1,
		"preference": map[string]any{
			"default": map[string]any{
				"text": map[string]any{"modelId": "claude-3-5-sonnet", "vendorHint": "anthropic"},
			},
		},
	}, 17, nil)
	require.Equal(t, http.StatusOK, patchNumericRevision.Code)
	patchNumericPayload := decodeCreativeResponse(t, patchNumericRevision)
	patchNumericData := creativeResponseData(t, patchNumericPayload)
	require.Equal(t, float64(2), patchNumericData["revision"])
	updatedPreference := creativeResponseObject(t, patchNumericData, "preference")
	updatedDefault := creativeResponseObject(t, updatedPreference, "default")
	updatedTextDefault := creativeResponseObject(t, updatedDefault, "text")
	require.Equal(t, "claude-3-5-sonnet", updatedTextDefault["modelId"])
	require.Equal(t, "anthropic", updatedTextDefault["vendorHint"])

	getAfterPatch := runCreativeHandler(t, CreativeGetModelPreference, http.MethodGet, "/creative/api/preferences/model", nil, 17, nil)
	require.Equal(t, http.StatusOK, getAfterPatch.Code)
	getAfterPatchData := creativeResponseData(t, decodeCreativeResponse(t, getAfterPatch))
	require.Equal(t, float64(2), getAfterPatchData["revision"])
	getAfterPatchPreference := creativeResponseObject(t, getAfterPatchData, "preference")
	getAfterPatchDefault := creativeResponseObject(t, getAfterPatchPreference, "default")
	getAfterPatchTextDefault := creativeResponseObject(t, getAfterPatchDefault, "text")
	require.Equal(t, "claude-3-5-sonnet", getAfterPatchTextDefault["modelId"])
	require.Equal(t, "anthropic", getAfterPatchTextDefault["vendorHint"])
}

func TestCreativeDocumentAPIContractWrapsDocumentsAndConflictDocument(t *testing.T) {
	setupCreativeControllerTestDB(t)

	create := runCreativeHandler(t, CreativeCreateDocument, http.MethodPost, "/creative/api/documents", map[string]any{
		"id":               "doc-1",
		"title":            "First",
		"snapshot":         map[string]any{"nodes": []any{}},
		"metadata":         map[string]any{"color": "blue"},
		"clientMutationId": "create-1",
	}, 23, nil)
	require.Equal(t, http.StatusCreated, create.Code)
	createPayload := decodeCreativeResponse(t, create)
	createData := creativeResponseData(t, createPayload)
	createdDocument := creativeResponseObject(t, createData, "document")
	require.Equal(t, "doc-1", createdDocument["id"])
	require.Equal(t, float64(1), createdDocument["revision"])
	require.NotContains(t, createPayload, "document")

	duplicate := runCreativeHandler(t, CreativeCreateDocument, http.MethodPost, "/creative/api/documents", map[string]any{
		"id":               "doc-1",
		"title":            "Duplicate",
		"snapshot":         map[string]any{"nodes": []any{99}},
		"clientMutationId": "create-2",
	}, 23, nil)
	require.Equal(t, http.StatusConflict, duplicate.Code)
	duplicatePayload := decodeCreativeResponse(t, duplicate)
	require.Equal(t, false, duplicatePayload["success"])
	duplicateDocument := creativeResponseObject(t, creativeResponseData(t, duplicatePayload), "document")
	require.Equal(t, float64(1), duplicateDocument["revision"])
	require.Equal(t, "First", duplicateDocument["title"])

	list := runCreativeHandler(t, CreativeListDocuments, http.MethodGet, "/creative/api/documents", nil, 23, nil)
	require.Equal(t, http.StatusOK, list.Code)
	listPayload := decodeCreativeResponse(t, list)
	listData := creativeResponseData(t, listPayload)
	require.Contains(t, listData, "documents")
	require.NotContains(t, listPayload, "documents")
	documents, ok := listData["documents"].([]any)
	require.True(t, ok)
	require.Len(t, documents, 1)
	listDocument, ok := documents[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(1), listDocument["revision"])

	get := runCreativeHandler(t, CreativeGetDocument, http.MethodGet, "/creative/api/documents/doc-1", nil, 23, gin.Params{{Key: "id", Value: "doc-1"}})
	require.Equal(t, http.StatusOK, get.Code)
	getData := creativeResponseData(t, decodeCreativeResponse(t, get))
	getDocument := creativeResponseObject(t, getData, "document")
	require.Equal(t, "doc-1", getDocument["id"])

	update := runCreativeHandler(t, CreativeUpdateDocument, http.MethodPut, "/creative/api/documents/doc-1", map[string]any{
		"baseRevision":     "1",
		"title":            "Second",
		"snapshot":         map[string]any{"nodes": []any{1}},
		"clientMutationId": "update-1",
	}, 23, gin.Params{{Key: "id", Value: "doc-1"}})
	require.Equal(t, http.StatusOK, update.Code)
	updateDocument := creativeResponseObject(t, creativeResponseData(t, decodeCreativeResponse(t, update)), "document")
	require.Equal(t, float64(2), updateDocument["revision"])
	require.Equal(t, "Second", updateDocument["title"])

	conflict := runCreativeHandler(t, CreativeUpdateDocument, http.MethodPut, "/creative/api/documents/doc-1", map[string]any{
		"baseRevision":     1,
		"title":            "Stale",
		"snapshot":         map[string]any{"nodes": []any{2}},
		"clientMutationId": "update-2",
	}, 23, gin.Params{{Key: "id", Value: "doc-1"}})
	require.Equal(t, http.StatusConflict, conflict.Code)
	conflictPayload := decodeCreativeResponse(t, conflict)
	require.Equal(t, false, conflictPayload["success"])
	conflictData := creativeResponseData(t, conflictPayload)
	conflictDocument := creativeResponseObject(t, conflictData, "document")
	require.Equal(t, float64(2), conflictDocument["revision"])
	require.Equal(t, "Second", conflictDocument["title"])
}

func setupCreativeControllerTestDB(t *testing.T) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	originalDB := model.DB
	originalLogDB := model.LOG_DB
	originalUsingSQLite := common.UsingSQLite
	originalUsingMySQL := common.UsingMySQL
	originalUsingPostgreSQL := common.UsingPostgreSQL
	originalRedisEnabled := common.RedisEnabled

	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Ability{}, &model.CreativeModelPreference{}, &model.CreativeDocument{}, &model.CreativeAsset{}, &model.CreativeAssetQuota{}, &model.CreativeDocumentAssetRef{}, &model.CreativeVideoIdempotency{}))

	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
		model.DB = originalDB
		model.LOG_DB = originalLogDB
		common.UsingSQLite = originalUsingSQLite
		common.UsingMySQL = originalUsingMySQL
		common.UsingPostgreSQL = originalUsingPostgreSQL
		common.RedisEnabled = originalRedisEnabled
	})
}

func seedCreativeControllerUser(t *testing.T, userId int) {
	t.Helper()

	require.NoError(t, model.DB.Create(&model.User{
		Id:       userId,
		Username: fmt.Sprintf("creative-user-%d", userId),
		Password: "password123",
		Role:     common.RoleCommonUser,
		Status:   common.UserStatusEnabled,
		Group:    "default",
		Quota:    100000,
		AffCode:  fmt.Sprintf("creative-aff-%d", userId),
	}).Error)
}

func withCreativeControllerOptions(t *testing.T, values map[string]string) {
	t.Helper()

	common.OptionMapRWMutex.Lock()
	originalMap := common.OptionMap
	nextMap := make(map[string]string, len(originalMap)+len(values))
	for key, value := range originalMap {
		nextMap[key] = value
	}
	for key, value := range values {
		if value == "" {
			delete(nextMap, key)
			continue
		}
		nextMap[key] = value
	}
	common.OptionMap = nextMap
	common.OptionMapRWMutex.Unlock()

	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = originalMap
		common.OptionMapRWMutex.Unlock()
	})
}

func withCreativeImageTaskMockBinding(t *testing.T, adapterEnabled bool, canaryGroups []string) {
	t.Helper()
	config := service.CreativeModelBindingsConfig{
		Version: 1,
		Bindings: []service.CreativeModelBindingConfig{{
			Id:                "mock:gpt-image-2:preview",
			ProviderModelId:   "gpt-image-2",
			PriceModelId:      "mock-gpt-image-2-price",
			DisplayName:       "Mock GPT Image 2",
			Modality:          "image",
			Enabled:           true,
			CanaryGroups:      canaryGroups,
			AdapterPreset:     "mock_image_task",
			ParameterTemplate: "mock_gpt_image",
			ParameterSchema: []dto.CreativeParameterSchemaItem{
				{
					Id:           "size",
					Label:        "Size",
					Type:         "enum",
					DefaultValue: "1024x1024",
					Options: []dto.CreativeParamOption{
						{Value: "1024x1024", Label: "1024×1024"},
					},
				},
				{
					Id:           "quality",
					Label:        "Quality",
					Type:         "enum",
					DefaultValue: "auto",
					Options: []dto.CreativeParamOption{
						{Value: "auto", Label: "Auto"},
					},
				},
			},
		}},
	}
	configJSON, err := service.NormalizeCreativeModelBindingsConfigJSON(config)
	require.NoError(t, err)
	enabledValue := ""
	if adapterEnabled {
		enabledValue = "true"
	}
	withCreativeControllerOptions(t, map[string]string{
		service.CreativeAdapterEnabledOptionKey: enabledValue,
		service.CreativeModelBindingsOptionKey:  configJSON,
	})
}

func seedCreativeControllerModelPool(t *testing.T) []string {
	t.Helper()

	expected := make([]string, 0, 30)
	for i := 1; i <= 30; i++ {
		expected = append(expected, fmt.Sprintf("creative-model-%02d", i))
	}

	channelId := 1
	insertAbility := func(group string, modelId string) {
		t.Helper()
		require.NoError(t, model.DB.Create(&model.Ability{
			Group:     group,
			Model:     modelId,
			ChannelId: channelId,
			Enabled:   true,
		}).Error)
		channelId++
	}
	for i := 1; i <= 20; i++ {
		insertAbility("default", fmt.Sprintf("creative-model-%02d", i))
	}
	for i := 11; i <= 30; i++ {
		insertAbility("vip", fmt.Sprintf("creative-model-%02d", i))
	}
	insertAbility("shadow", "creative-model-shadow")

	return expected
}

func seedCreativeControllerSunoModelPool(t *testing.T) {
	t.Helper()

	require.NoError(t, model.DB.Create(&model.Ability{
		Group:     "vip",
		Model:     "suno_music",
		ChannelId: 7001,
		Enabled:   true,
	}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group:     "default",
		Model:     "suno_lyrics",
		ChannelId: 7002,
		Enabled:   true,
	}).Error)
}

func seedCreativeControllerMJModelPool(t *testing.T) {
	t.Helper()

	require.NoError(t, model.DB.Create(&model.Ability{
		Group:     "vip",
		Model:     "mj_imagine",
		ChannelId: 7401,
		Enabled:   true,
	}).Error)
}

type creativeSessionAuthFixture struct {
	csrfToken string
	nonce     string
	cookies   []*http.Cookie
}

func newCreativeSessionTestRouter(userId int) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("creative-session-test-secret"))))
	router.GET("/creative/api/bootstrap", func(c *gin.Context) {
		c.Set("id", userId)
		CreativeBootstrap(c)
	})
	router.GET("/creative/api/models", func(c *gin.Context) {
		c.Set("id", userId)
		CreativeListModels(c)
	})
	return router
}

func newCreativeRelayBrokerTestRouter(t *testing.T, userId int, relayHandler gin.HandlerFunc) *gin.Engine {
	t.Helper()

	gin.SetMode(gin.TestMode)
	restoreCreativeVideoGate := SetCreativeVideoRelayEnabledForTest(true)
	t.Cleanup(restoreCreativeVideoGate)

	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("creative-relay-test-secret"))))
	router.GET("/creative/api/bootstrap", func(c *gin.Context) {
		session := sessions.Default(c)
		session.Set("username", fmt.Sprintf("creative-user-%d", userId))
		session.Set("role", common.RoleCommonUser)
		session.Set("id", userId)
		session.Set("status", common.UserStatusEnabled)
		session.Set("group", "default")
		c.Set("id", userId)
		CreativeBootstrap(c)
	})

	relayRouter := router.Group("/creative/relay/v1")
	relayRouter.Use(middleware.BodyStorageCleanup())
	relayRouter.Use(middleware.CreativeSessionHeaderBridge(), middleware.UserAuth())
	relayRouter.Use(middleware.ModelRequestRateLimit())
	relayRouter.Use(middleware.CreativeRequireSameOrigin())
	relayRouter.Use(middleware.CreativeRequireNonce())
	relayRouter.Use(CreativeRejectForbiddenRelayFields())
	generalRelayRouter := relayRouter.Group("")
	generalRelayRouter.Use(middleware.CreativeRelaySessionBroker())
	generalRelayRouter.POST("/chat/completions", relayHandler)
	relayRouter.POST("/images/generations", CreativeRejectManagedImageBindingSyncRoute(), middleware.CreativeRelaySessionBroker(), relayHandler)
	imageTaskRouter := relayRouter.Group("/images/tasks")
	imageTaskRouter.Use(CreativeImageTaskSubmitIdempotency())
	imageTaskRouter.POST("", CreativeRelayImageTaskSubmit)
	imageTaskRouter.GET("/:task_id", CreativeRelayImageTaskFetch)
	imageTaskRouter.GET("/:task_id/content", CreativeRelayImageTaskContent)
	videoRelayRouter := relayRouter.Group("/videos")
	videoRelayRouter.Use(CreativeVideoRelayGate())
	videoRelayRouter.Use(CreativeVideoSubmitIdempotency())
	videoRelayRouter.Use(middleware.CreativeRelaySessionBroker())
	videoRelayRouter.POST("", relayHandler)
	videoRelayRouter.GET("/:task_id", relayHandler)
	videoRelayRouter.GET("/:task_id/content", relayHandler)
	sunoRelayRouter := relayRouter.Group("/suno")
	sunoRelayRouter.POST("/submit/:action", CreativeSunoSubmitGuard(), middleware.CreativeRelaySessionBroker(), relayHandler)
	sunoRelayRouter.GET("/fetch/:id", middleware.CreativeRelaySessionBroker(), relayHandler)
	sunoRelayRouter.POST("/fetch", middleware.CreativeRelaySessionBroker(), relayHandler)
	mjRelayRouter := relayRouter.Group("/mj")
	mjRelayRouter.POST("/submit/imagine", CreativeMJSubmitImagineGuard(), middleware.CreativeRelaySessionBroker(), relayHandler)
	mjRelayRouter.GET("/task/:task_id/fetch", middleware.CreativeRelaySessionBroker(), relayHandler)
	mjRelayRouter.POST("/task/list-by-condition", middleware.CreativeRelaySessionBroker(), relayHandler)
	mjRelayRouter.GET("/image/:task_id", middleware.CreativeRelaySessionBroker(), relayHandler)
	mjRelayRouter.POST("/submit/action", middleware.CreativeRelaySessionBroker(), CreativeRelayMJUnsupported)
	mjRelayRouter.POST("/submit/change", middleware.CreativeRelaySessionBroker(), CreativeRelayMJUnsupported)
	mjRelayRouter.POST("/submit/simple-change", middleware.CreativeRelaySessionBroker(), CreativeRelayMJUnsupported)
	mjRelayRouter.POST("/submit/modal", middleware.CreativeRelaySessionBroker(), CreativeRelayMJUnsupported)
	mjRelayRouter.POST("/submit/shorten", middleware.CreativeRelaySessionBroker(), CreativeRelayMJUnsupported)
	mjRelayRouter.POST("/submit/blend", middleware.CreativeRelaySessionBroker(), CreativeRelayMJUnsupported)
	mjRelayRouter.POST("/submit/describe", middleware.CreativeRelaySessionBroker(), CreativeRelayMJUnsupported)
	mjRelayRouter.POST("/submit/edits", middleware.CreativeRelaySessionBroker(), CreativeRelayMJUnsupported)
	mjRelayRouter.POST("/submit/video", middleware.CreativeRelaySessionBroker(), CreativeRelayMJUnsupported)
	mjRelayRouter.POST("/submit/upload-discord-images", middleware.CreativeRelaySessionBroker(), CreativeRelayMJUnsupported)
	mjRelayRouter.POST("/insight-face/swap", middleware.CreativeRelaySessionBroker(), CreativeRelayMJUnsupported)
	mjRelayRouter.GET("/task/:task_id/image-seed", middleware.CreativeRelaySessionBroker(), CreativeRelayMJUnsupported)
	return router
}

func creativeNonceHeaders(auth creativeSessionAuthFixture) map[string]string {
	return map[string]string{
		"X-Creative-CSRF":  auth.csrfToken,
		"X-Creative-Nonce": auth.nonce,
	}
}

func creativeSameOriginNonceHeaders(auth creativeSessionAuthFixture) map[string]string {
	headers := creativeNonceHeaders(auth)
	headers["Origin"] = "http://example.com"
	return headers
}

func bootstrapCreativeSessionAuth(t *testing.T, router *gin.Engine) creativeSessionAuthFixture {
	t.Helper()

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/creative/api/bootstrap", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	payload := decodeCreativeResponse(t, recorder)
	auth := creativeResponseObject(t, creativeResponseData(t, payload), "auth")
	csrfToken, ok := auth["csrfToken"].(string)
	require.True(t, ok)
	require.NotEmpty(t, csrfToken)
	nonce, ok := auth["nonce"].(string)
	require.True(t, ok)
	require.NotEmpty(t, nonce)
	return creativeSessionAuthFixture{
		csrfToken: csrfToken,
		nonce:     nonce,
		cookies:   recorder.Result().Cookies(),
	}
}

func performCreativeSessionJSON(t *testing.T, router *gin.Engine, method string, target string, body any, cookies []*http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	encoded, err := common.Marshal(body)
	require.NoError(t, err)
	request := httptest.NewRequest(method, target, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	for _, sessionCookie := range cookies {
		request.AddCookie(sessionCookie)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func performCreativeSessionMultipart(t *testing.T, router *gin.Engine, method string, target string, cookies []*http.Cookie, headers map[string]string, fields map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	return performCreativeSessionMultipartWithFiles(t, router, method, target, cookies, headers, fields, nil)
}

func performCreativeSessionForm(t *testing.T, router *gin.Engine, method string, target string, cookies []*http.Cookie, headers map[string]string, fields map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	values := url.Values{}
	for key, value := range fields {
		values.Set(key, value)
	}
	request := httptest.NewRequest(method, target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, sessionCookie := range cookies {
		request.AddCookie(sessionCookie)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func performCreativeSessionMultipartWithFiles(t *testing.T, router *gin.Engine, method string, target string, cookies []*http.Cookie, headers map[string]string, fields map[string]string, files map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		require.NoError(t, writer.WriteField(key, value))
	}
	for key, value := range files {
		part, err := writer.CreateFormFile(key, "test.txt")
		require.NoError(t, err)
		_, err = part.Write([]byte(value))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	request := httptest.NewRequest(method, target, bytes.NewReader(body.Bytes()))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	for _, sessionCookie := range cookies {
		request.AddCookie(sessionCookie)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func runCreativeHandler(t *testing.T, handler gin.HandlerFunc, method string, target string, body any, userId int, params gin.Params) *httptest.ResponseRecorder {
	t.Helper()

	var requestBody *bytes.Reader
	if body == nil {
		requestBody = bytes.NewReader(nil)
	} else {
		encoded, err := common.Marshal(body)
		require.NoError(t, err)
		requestBody = bytes.NewReader(encoded)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, target, requestBody)
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Params = params
	ctx.Set("id", userId)
	handler(ctx)
	return recorder
}

func decodeCreativeResponse(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var payload map[string]any
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	return payload
}

func creativeResponseData(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()

	data, ok := payload["data"].(map[string]any)
	require.True(t, ok)
	return data
}

func creativeResponseObject(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()

	value, ok := parent[key].(map[string]any)
	require.True(t, ok)
	return value
}

func creativeResponseArray(t *testing.T, parent map[string]any, key string) []any {
	t.Helper()

	value, ok := parent[key].([]any)
	require.True(t, ok)
	return value
}

func requireCreativeResponseOmitsPolicyFields(t *testing.T, rawResponse string) {
	t.Helper()

	for _, forbiddenField := range []string{
		"defaultModel",
		"defaultVisibleModels",
		"order",
		"group",
		"uiPolicy",
		"displayPolicy",
	} {
		require.NotContains(t, rawResponse, `"`+forbiddenField+`"`)
	}
}

func requireCreativeResponseOmitsSecretFields(t *testing.T, rawResponse string) {
	t.Helper()

	for _, forbiddenField := range []string{
		"featuredModels",
		"apiKey",
		"token",
		"baseUrl",
		"provider",
	} {
		require.NotContains(t, rawResponse, `"`+forbiddenField+`"`)
	}
}
