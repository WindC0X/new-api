package channel

import (
	"net/http"
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestProcessHeaderOverride_ChannelTestSkipsPassthroughRules(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"*": "",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Empty(t, headers)
}

func TestProcessHeaderOverride_ChannelTestSkipsClientHeaderPlaceholder(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Upstream-Trace": "{client_header:X-Trace-Id}",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	_, ok := headers["x-upstream-trace"]
	require.False(t, ok)
}

func TestProcessHeaderOverride_NonTestKeepsClientHeaderPlaceholder(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Upstream-Trace": "{client_header:X-Trace-Id}",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "trace-123", headers["x-upstream-trace"])
}

func TestProcessHeaderOverride_RuntimeOverrideIsFinalHeaderMap(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		IsChannelTest:             false,
		UseRuntimeHeadersOverride: true,
		RuntimeHeadersOverride: map[string]any{
			"x-static":  "runtime-value",
			"x-runtime": "runtime-only",
		},
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Static": "legacy-value",
				"X-Legacy": "legacy-only",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "runtime-value", headers["x-static"])
	require.Equal(t, "runtime-only", headers["x-runtime"])
	_, exists := headers["x-legacy"]
	require.False(t, exists)
}

func TestProcessHeaderOverride_PassthroughSkipsAcceptEncoding(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")
	ctx.Request.Header.Set("Accept-Encoding", "gzip")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"*": "",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "trace-123", headers["x-trace-id"])

	_, hasAcceptEncoding := headers["accept-encoding"]
	require.False(t, hasAcceptEncoding)
}

func TestProcessHeaderOverride_PassthroughSkipsSensitiveBrowserHeaders(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")
	ctx.Request.Header.Set("Cookie", "session=leak")
	ctx.Request.Header.Set("Authorization", "Bearer leak")
	ctx.Request.Header.Set("X-Creative-CSRF", "csrf-leak")
	ctx.Request.Header.Set("X-Creative-Nonce", "nonce-leak")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"*": "",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "trace-123", headers["x-trace-id"])
	require.NotContains(t, headers, "cookie")
	require.NotContains(t, headers, "authorization")
	require.NotContains(t, headers, "x-creative-csrf")
	require.NotContains(t, headers, "x-creative-nonce")
}

func TestProcessHeaderOverride_PassthroughSkipsProxyConnectionAndConnectionTokens(t *testing.T) {
	t.Parallel()

	for name, headerOverride := range map[string]map[string]any{
		"wildcard": {
			"*": "",
		},
		"regex": {
			"regex:.*": "",
		},
	} {
		name := name
		headerOverride := headerOverride
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			ctx.Request.Header.Set("Connection", "X-Trace-Id, X-Debug-Hop")
			ctx.Request.Header.Set("Proxy-Connection", "keep-alive")
			ctx.Request.Header.Set("X-Trace-Id", "trace-hop")
			ctx.Request.Header.Set("X-Debug-Hop", "debug-hop")
			ctx.Request.Header.Set("X-Safe", "safe-123")

			info := &relaycommon.RelayInfo{
				IsChannelTest: false,
				ChannelMeta: &relaycommon.ChannelMeta{
					HeadersOverride: headerOverride,
				},
			}

			headers, err := processHeaderOverride(info, ctx)
			require.NoError(t, err)
			require.Equal(t, "safe-123", headers["x-safe"])
			require.NotContains(t, headers, "connection")
			require.NotContains(t, headers, "proxy-connection")
			require.NotContains(t, headers, "x-trace-id")
			require.NotContains(t, headers, "x-debug-hop")
		})
	}
}

func TestProcessHeaderOverride_PassthroughAndClientHeaderSkipMultiValueConnectionTokens(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Add("Connection", "X-First-Hop")
	ctx.Request.Header.Add("Connection", "X-Second-Hop")
	ctx.Request.Header.Set("X-First-Hop", "first-hop")
	ctx.Request.Header.Set("X-Second-Hop", "second-hop")
	ctx.Request.Header.Set("X-Safe", "safe-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"*":                 "",
				"X-Upstream-First":  "{client_header:X-First-Hop}",
				"X-Upstream-Second": "{client_header:X-Second-Hop}",
				"X-Upstream-Safe":   "{client_header:X-Safe}",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "safe-123", headers["x-safe"])
	require.Equal(t, "safe-123", headers["x-upstream-safe"])
	require.NotContains(t, headers, "connection")
	require.NotContains(t, headers, "x-first-hop")
	require.NotContains(t, headers, "x-second-hop")
	require.NotContains(t, headers, "x-upstream-first")
	require.NotContains(t, headers, "x-upstream-second")
}

func TestProcessHeaderOverride_PassthroughSkipsSeparatorVariantsAndWebSocketHeaders(t *testing.T) {
	t.Parallel()

	for name, headerOverride := range map[string]map[string]any{
		"wildcard": {
			"*": "",
		},
		"regex": {
			"regex:.*": "",
		},
	} {
		name := name
		headerOverride := headerOverride
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			ctx.Request.Header.Set("X-Trace-Id", "trace-123")
			ctx.Request.Header.Set("X_API_Key", "api-key-leak")
			ctx.Request.Header.Set("Api.Secret", "api-secret-leak")
			ctx.Request.Header.Set("X_Creative_CSRF", "csrf-leak")
			ctx.Request.Header.Set("X Api Key", "api-key-space-leak")
			ctx.Request.Header.Set("Api Secret", "api-secret-space-leak")
			ctx.Request.Header.Set("X Creative CSRF", "csrf-space-leak")
			ctx.Request.Header.Set("Sec-WebSocket-Key", "ws-key-leak")
			ctx.Request.Header.Set("Sec_WebSocket_Version", "13")
			ctx.Request.Header.Set("Sec.WebSocket.Extensions", "permessage-deflate")
			ctx.Request.Header.Set("Sec-WebSocket-Protocol", "realtime,openai-insecure-api-key.leak")

			info := &relaycommon.RelayInfo{
				IsChannelTest: false,
				ChannelMeta: &relaycommon.ChannelMeta{
					HeadersOverride: headerOverride,
				},
			}

			headers, err := processHeaderOverride(info, ctx)
			require.NoError(t, err)
			require.Equal(t, "trace-123", headers["x-trace-id"])
			require.NotContains(t, headers, "x_api_key")
			require.NotContains(t, headers, "api.secret")
			require.NotContains(t, headers, "x_creative_csrf")
			require.NotContains(t, headers, "x api key")
			require.NotContains(t, headers, "api secret")
			require.NotContains(t, headers, "x creative csrf")
			require.NotContains(t, headers, "sec-websocket-key")
			require.NotContains(t, headers, "sec_websocket_version")
			require.NotContains(t, headers, "sec.websocket.extensions")
			require.NotContains(t, headers, "sec-websocket-protocol")
		})
	}
}

func TestProcessHeaderOverride_RegexPassthroughMatchesLowercaseNormalizedHeaderName(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")
	ctx.Request.Header.Set("Authorization", "Bearer leak")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"regex:^x-trace": "",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "trace-123", headers["x-trace-id"])
	require.NotContains(t, headers, "authorization")
}

func TestProcessHeaderOverride_ClientHeaderPlaceholderSkipsSensitiveBrowserHeaders(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("Authorization", "Bearer leak")
	ctx.Request.Header.Set("X-Creative-CSRF", "csrf-leak")
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Upstream-Auth":  "{client_header:Authorization}",
				"X-Upstream-CSRF":  "{client_header:X-Creative-CSRF}",
				"X-Upstream-Trace": "{client_header:X-Trace-Id}",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.NotContains(t, headers, "x-upstream-auth")
	require.NotContains(t, headers, "x-upstream-csrf")
	require.Equal(t, "trace-123", headers["x-upstream-trace"])
}

func TestProcessHeaderOverride_ClientHeaderPlaceholderSkipsSensitiveTargetHeaders(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"Authorization":    "{client_header:X-Trace-Id}",
				"Host":             "{client_header:X-Trace-Id}",
				"Cookie":           "{client_header:X-Trace-Id}",
				"X-Creative-CSRF":  "{client_header:X-Trace-Id}",
				"Proxy-Connection": "{client_header:X-Trace-Id}",
				"X-Upstream-Trace": "{client_header:X-Trace-Id}",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "trace-123", headers["x-upstream-trace"])
	require.NotContains(t, headers, "authorization")
	require.NotContains(t, headers, "host")
	require.NotContains(t, headers, "cookie")
	require.NotContains(t, headers, "x-creative-csrf")
	require.NotContains(t, headers, "proxy-connection")
}

func TestProcessHeaderOverride_ClientHeaderPlaceholderSkipsConnectionTokens(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("Connection", "X-Trace-Id, X-Debug-Hop")
	ctx.Request.Header.Set("X-Trace-Id", "trace-hop")
	ctx.Request.Header.Set("X-Safe", "safe-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Upstream-Trace": "{client_header:X-Trace-Id}",
				"X-Debug-Hop":      "{client_header:X-Safe}",
				"X-Upstream-Safe":  "{client_header:X-Safe}",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "safe-123", headers["x-upstream-safe"])
	require.NotContains(t, headers, "x-upstream-trace")
	require.NotContains(t, headers, "x-debug-hop")
}

func TestProcessHeaderOverride_ClientHeaderPlaceholderSkipsSeparatorVariantsAndWebSocket(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X_API_Key", "api-key-leak")
	ctx.Request.Header.Set("Api.Secret", "api-secret-leak")
	ctx.Request.Header.Set("X_Creative_CSRF", "csrf-leak")
	ctx.Request.Header.Set("X Api Key", "api-key-space-leak")
	ctx.Request.Header.Set("Api Secret", "api-secret-space-leak")
	ctx.Request.Header.Set("X Creative CSRF", "csrf-space-leak")
	ctx.Request.Header.Set("Sec-WebSocket-Key", "ws-key-leak")
	ctx.Request.Header.Set("Sec_WebSocket_Version", "13")
	ctx.Request.Header.Set("Sec.WebSocket.Extensions", "permessage-deflate")
	ctx.Request.Header.Set("Sec-WebSocket-Protocol", "realtime,openai-insecure-api-key.leak")
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Upstream-Api-Key":              "{client_header:X_API_Key}",
				"X-Upstream-Api-Secret":           "{client_header:Api.Secret}",
				"X-Upstream-CSRF":                 "{client_header:X_Creative_CSRF}",
				"X-Upstream-Api-Key-Space":        "{client_header:X Api Key}",
				"X-Upstream-Api-Secret-Space":     "{client_header:Api Secret}",
				"X-Upstream-CSRF-Space":           "{client_header:X Creative CSRF}",
				"X-Upstream-WebSocket-Key":        "{client_header:Sec-WebSocket-Key}",
				"X-Upstream-WebSocket-Version":    "{client_header:Sec_WebSocket_Version}",
				"X-Upstream-WebSocket-Extensions": "{client_header:Sec.WebSocket.Extensions}",
				"X-Upstream-WebSocket-Protocol":   "{client_header:Sec-WebSocket-Protocol}",
				"X-Upstream-Trace":                "{client_header:X-Trace-Id}",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.NotContains(t, headers, "x-upstream-api-key")
	require.NotContains(t, headers, "x-upstream-api-secret")
	require.NotContains(t, headers, "x-upstream-csrf")
	require.NotContains(t, headers, "x-upstream-api-key-space")
	require.NotContains(t, headers, "x-upstream-api-secret-space")
	require.NotContains(t, headers, "x-upstream-csrf-space")
	require.NotContains(t, headers, "x-upstream-websocket-key")
	require.NotContains(t, headers, "x-upstream-websocket-version")
	require.NotContains(t, headers, "x-upstream-websocket-extensions")
	require.NotContains(t, headers, "x-upstream-websocket-protocol")
	require.Equal(t, "trace-123", headers["x-upstream-trace"])
}

func TestProcessHeaderOverride_PassHeadersTemplateSetsRuntimeHeaders(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx.Request.Header.Set("Originator", "Codex CLI")
	ctx.Request.Header.Set("Session_id", "sess-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		RequestHeaders: map[string]string{
			"Originator": "Codex CLI",
			"Session_id": "sess-123",
		},
		ChannelMeta: &relaycommon.ChannelMeta{
			ParamOverride: map[string]any{
				"operations": []any{
					map[string]any{
						"mode":  "pass_headers",
						"value": []any{"Originator", "Session_id", "X-Codex-Beta-Features"},
					},
				},
			},
			HeadersOverride: map[string]any{
				"X-Static": "legacy-value",
			},
		},
	}

	_, err := relaycommon.ApplyParamOverrideWithRelayInfo([]byte(`{"model":"gpt-4.1"}`), info)
	require.NoError(t, err)
	require.True(t, info.UseRuntimeHeadersOverride)
	require.Equal(t, "Codex CLI", info.RuntimeHeadersOverride["originator"])
	require.Equal(t, "sess-123", info.RuntimeHeadersOverride["session_id"])
	_, exists := info.RuntimeHeadersOverride["x-codex-beta-features"]
	require.False(t, exists)
	require.Equal(t, "legacy-value", info.RuntimeHeadersOverride["x-static"])

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "Codex CLI", headers["originator"])
	require.Equal(t, "sess-123", headers["session_id"])
	_, exists = headers["x-codex-beta-features"]
	require.False(t, exists)

	upstreamReq := httptest.NewRequest(http.MethodPost, "https://example.com/v1/responses", nil)
	applyHeaderOverrideToRequest(upstreamReq, headers)
	require.Equal(t, "Codex CLI", upstreamReq.Header.Get("Originator"))
	require.Equal(t, "sess-123", upstreamReq.Header.Get("Session_id"))
	require.Empty(t, upstreamReq.Header.Get("X-Codex-Beta-Features"))
}
