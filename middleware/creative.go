package middleware

import (
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
)

const (
	creativeSessionCSRFKey   = "creative_csrf_token"
	creativeSessionNonceKey  = "creative_nonce"
	creativeCSRFHeaderName   = "X-Creative-CSRF"
	creativeNonceHeaderName  = "X-Creative-Nonce"
	creativeSessionTokenSize = 48
	creativePublicOriginEnv  = "CREATIVE_PUBLIC_ORIGIN"
)

const ContextKeyCreativeRelayModelOverride = "creative_relay_model_override"

// EnsureCreativeSessionAuthMaterial returns the per-browser-session material
// used by embedded /creative calls. It is generated lazily during bootstrap and
// saved in the server-side session cookie payload; opentu receives only opaque
// values and must echo them on unsafe session-broker calls.
func EnsureCreativeSessionAuthMaterial(c *gin.Context) (string, string, error) {
	session := sessions.Default(c)
	csrfToken := creativeSessionString(session.Get(creativeSessionCSRFKey))
	nonce := creativeSessionString(session.Get(creativeSessionNonceKey))

	changed := false
	if csrfToken == "" {
		generated, err := common.GenerateRandomCharsKey(creativeSessionTokenSize)
		if err != nil {
			return "", "", err
		}
		csrfToken = generated
		session.Set(creativeSessionCSRFKey, csrfToken)
		changed = true
	}
	if nonce == "" {
		generated, err := common.GenerateRandomCharsKey(creativeSessionTokenSize)
		if err != nil {
			return "", "", err
		}
		nonce = generated
		session.Set(creativeSessionNonceKey, nonce)
		changed = true
	}
	if changed {
		if err := session.Save(); err != nil {
			return "", "", err
		}
	}
	return csrfToken, nonce, nil
}

// CreativeSessionHeaderBridge allows the embedded /creative SPA to authenticate
// with the existing session cookie without knowing the dashboard's New-Api-User
// header value before bootstrap. UserAuth still performs the actual checks.
func CreativeSessionHeaderBridge() gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.TrimSpace(c.GetHeader("New-Api-User")) == "" {
			if id := sessions.Default(c).Get("id"); id != nil {
				c.Request.Header.Set("New-Api-User", fmt.Sprint(id))
			}
		}
		c.Next()
	}
}

// CreativeRequireNonce validates bootstrap-issued CSRF/nonce material for
// unsafe /creative requests. GET/HEAD/OPTIONS remain session-auth only.
func CreativeRequireSameOrigin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !creativeUnsafeRequestOriginIsValid(c) {
			c.JSON(http.StatusForbidden, gin.H{
				"success": false,
				"message": "creative request origin is invalid",
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

// CreativeRejectCrossOriginWhenPresent allows originless safe browser GETs
// (for navigation/bootstrap compatibility) while rejecting explicit cross-site
// Origin/Referer signals on session-bound Creative API reads.
func CreativeRejectCrossOriginWhenPresent() gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.TrimSpace(c.GetHeader("Origin")) == "" &&
			strings.TrimSpace(c.GetHeader("Referer")) == "" {
			c.Next()
			return
		}
		if !creativeUnsafeRequestOriginIsValid(c) {
			c.JSON(http.StatusForbidden, gin.H{
				"success": false,
				"message": "creative request origin is invalid",
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

func CreativeRequireNonce() gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}

		if !creativeUnsafeRequestOriginIsValid(c) {
			c.JSON(http.StatusForbidden, gin.H{
				"success": false,
				"message": "creative request origin is invalid",
			})
			c.Abort()
			return
		}

		session := sessions.Default(c)
		expectedCSRF := creativeSessionString(session.Get(creativeSessionCSRFKey))
		expectedNonce := creativeSessionString(session.Get(creativeSessionNonceKey))
		actualCSRF := strings.TrimSpace(c.GetHeader(creativeCSRFHeaderName))
		actualNonce := strings.TrimSpace(c.GetHeader(creativeNonceHeaderName))

		if expectedCSRF == "" || expectedNonce == "" ||
			!creativeConstantTimeEqual(expectedCSRF, actualCSRF) ||
			!creativeConstantTimeEqual(expectedNonce, actualNonce) {
			c.JSON(http.StatusForbidden, gin.H{
				"success": false,
				"message": "creative session auth is invalid",
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

// CreativeRelaySessionBroker prepares a session-backed playground-style relay
// context without exposing or accepting API keys. It selects the callable group
// server-side from the user's full model pool.
func CreativeRelaySessionBroker() gin.HandlerFunc {
	return func(c *gin.Context) {
		userId := c.GetInt("id")
		userCache, err := model.GetUserCache(userId)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": "failed to load user", "type": "server_error"}})
			c.Abort()
			return
		}
		userCache.WriteContext(c)

		modelName, err := readCreativeRelayModel(c)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error(), "type": "invalid_request_error"}})
			c.Abort()
			return
		}
		if modelName != "" {
			requiredModality := creativeRelayRequiredPolicyModality(c)
			selectedGroup, ok := service.SelectCreativeModelGroupForModality(userCache.Group, modelName, requiredModality)
			if !ok {
				message := "model is not available for this user"
				if requiredModality != "" {
					message = "model is not available for this creative endpoint"
				}
				c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"message": message, "type": "access_denied", "param": "model"}})
				c.Abort()
				return
			}
			common.SetContextKey(c, constant.ContextKeyUsingGroup, selectedGroup)
			common.SetContextKey(c, constant.ContextKeyTokenGroup, selectedGroup)
		} else {
			common.SetContextKey(c, constant.ContextKeyUsingGroup, userCache.Group)
		}
		c.Next()
	}
}

func creativeRelayRequiredPolicyModality(c *gin.Context) string {
	path := strings.ToLower(c.Request.URL.Path)
	switch {
	case strings.Contains(path, "/images/"):
		return "image"
	case strings.Contains(path, "/videos"):
		return "video"
	case strings.Contains(path, "/suno/"):
		return "audio"
	case strings.Contains(path, "/mj/"):
		return "image"
	case strings.Contains(path, "/chat/"), strings.Contains(path, "/responses"):
		return "text"
	default:
		return ""
	}
}

func readCreativeRelayModel(c *gin.Context) (string, error) {
	if override := strings.TrimSpace(c.GetString(ContextKeyCreativeRelayModelOverride)); override != "" {
		return override, nil
	}
	if creativeRelayPathIsUnsupportedMJAction(c.Request.URL.Path) {
		return "", nil
	}

	switch c.Request.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return "", nil
	}

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
	if strings.Contains(contentType, "multipart/form-data") {
		form, err := common.ParseMultipartFormReusable(c)
		if err != nil {
			return "", err
		}
		defer form.RemoveAll()
		values := form.Value["model"]
		if len(values) == 0 {
			return "", nil
		}
		return strings.TrimSpace(values[0]), nil
	}

	var payload map[string]any
	if err := common.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	modelName, _ := payload["model"].(string)
	return strings.TrimSpace(modelName), nil
}

func creativeRelayPathIsUnsupportedMJAction(path string) bool {
	normalized := "/" + strings.Trim(strings.TrimSpace(path), "/")
	switch normalized {
	case "/creative/relay/v1/mj/submit/action",
		"/creative/relay/v1/mj/submit/change",
		"/creative/relay/v1/mj/submit/simple-change",
		"/creative/relay/v1/mj/submit/modal",
		"/creative/relay/v1/mj/submit/shorten",
		"/creative/relay/v1/mj/submit/blend",
		"/creative/relay/v1/mj/submit/describe",
		"/creative/relay/v1/mj/submit/edits",
		"/creative/relay/v1/mj/submit/video",
		"/creative/relay/v1/mj/submit/upload-discord-images",
		"/creative/relay/v1/mj/insight-face/swap",
		"/creative/relay/v1/mj/task/image-seed":
		return true
	default:
		return false
	}
}

func creativeSessionString(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func creativeConstantTimeEqual(expected string, actual string) bool {
	if expected == "" || actual == "" || len(expected) != len(actual) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}

func creativeUnsafeRequestOriginIsValid(c *gin.Context) bool {
	expectedOrigin := creativeRequestOrigin(c)
	origin := strings.TrimSpace(c.GetHeader("Origin"))
	if origin != "" {
		return creativeOriginHeaderMatches(origin, expectedOrigin)
	}

	referer := strings.TrimSpace(c.GetHeader("Referer"))
	if referer != "" {
		return creativeRefererOriginMatches(referer, expectedOrigin)
	}

	return false
}

func CreativeRequestOrigin(c *gin.Context) string {
	return creativeRequestOrigin(c)
}

func creativeRequestOrigin(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}

	if publicOrigin := creativeConfiguredPublicOrigin(); publicOrigin != "" {
		return publicOrigin
	}

	scheme := creativeRequestScheme(c.Request)

	host := strings.TrimSpace(c.Request.Host)
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

func creativeRequestScheme(request *http.Request) string {
	if request.TLS != nil {
		return "https"
	}
	if request.URL != nil {
		if scheme := creativeSafeScheme(request.URL.Scheme); scheme != "" {
			return scheme
		}
	}
	return "http"
}

func creativeConfiguredPublicOrigin() string {
	return creativeNormalizeOrigin(os.Getenv(creativePublicOriginEnv))
}

func creativeNormalizeOrigin(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	if creativeSafeScheme(parsed.Scheme) == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ""
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func creativeForwardedProto(header string) string {
	if strings.TrimSpace(header) == "" {
		return ""
	}
	firstForwarded := strings.Split(header, ",")[0]
	for _, part := range strings.Split(firstForwarded, ";") {
		key, value, ok := strings.Cut(part, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "proto") {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"`)
		return creativeSafeScheme(value)
	}
	return ""
}

func creativeFirstHeaderScheme(header string) string {
	if strings.TrimSpace(header) == "" {
		return ""
	}
	return creativeSafeScheme(strings.Split(header, ",")[0])
}

func creativeSafeScheme(scheme string) string {
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	switch scheme {
	case "http", "https":
		return scheme
	default:
		return ""
	}
}

func creativeOriginHeaderMatches(origin string, expectedOrigin string) bool {
	if expectedOrigin == "" {
		return false
	}
	parsedOrigin, ok := creativeParsedOrigin(origin)
	if !ok || parsedOrigin != origin {
		return false
	}
	return parsedOrigin == expectedOrigin
}

func creativeRefererOriginMatches(referer string, expectedOrigin string) bool {
	if expectedOrigin == "" {
		return false
	}
	parsedOrigin, ok := creativeParsedOrigin(referer)
	return ok && parsedOrigin == expectedOrigin
}

func creativeParsedOrigin(rawURL string) (string, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", false
	}
	return parsed.Scheme + "://" + parsed.Host, true
}
