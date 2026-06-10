package controller

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

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

func TestCreativeNonceMiddlewareRequiresSameOriginSignalForUnsafeMethods(t *testing.T) {
	setupCreativeControllerTestDB(t)
	seedCreativeControllerUser(t, 42)
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
	service.InitHttpClient()

	var gotAuthorization string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		require.Equal(t, "/v1/videos/upstream_content/content", r.URL.Path)
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("video-bytes"))
	}))
	t.Cleanup(upstream.Close)
	upstreamURL, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	fetchSetting := system_setting.GetFetchSetting()
	previousAllowPrivateIP := fetchSetting.AllowPrivateIp
	previousAllowedPorts := append([]string(nil), fetchSetting.AllowedPorts...)
	fetchSetting.AllowPrivateIp = true
	fetchSetting.AllowedPorts = append(fetchSetting.AllowedPorts, upstreamURL.Port())
	t.Cleanup(func() {
		fetchSetting.AllowPrivateIp = previousAllowPrivateIP
		fetchSetting.AllowedPorts = previousAllowedPorts
	})

	baseURL := upstream.URL
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
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Ability{}, &model.CreativeModelPreference{}, &model.CreativeDocument{}, &model.CreativeAsset{}, &model.CreativeDocumentAssetRef{}, &model.CreativeVideoIdempotency{}))

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
	relayRouter.Use(middleware.CreativeRequireSameOrigin())
	relayRouter.Use(middleware.CreativeRequireNonce())
	relayRouter.Use(CreativeRejectForbiddenRelayFields())
	generalRelayRouter := relayRouter.Group("")
	generalRelayRouter.Use(middleware.CreativeRelaySessionBroker())
	generalRelayRouter.POST("/chat/completions", relayHandler)
	generalRelayRouter.POST("/images/generations", relayHandler)
	videoRelayRouter := relayRouter.Group("/videos")
	videoRelayRouter.Use(CreativeVideoRelayGate())
	videoRelayRouter.Use(CreativeVideoSubmitIdempotency())
	videoRelayRouter.Use(middleware.CreativeRelaySessionBroker())
	videoRelayRouter.POST("", relayHandler)
	videoRelayRouter.GET("/:task_id", relayHandler)
	videoRelayRouter.GET("/:task_id/content", relayHandler)
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
