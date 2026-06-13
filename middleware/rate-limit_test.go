package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func configureGlobalWebRateLimitForTest(t *testing.T, limit int) {
	t.Helper()

	originalRedisEnabled := common.RedisEnabled
	originalEnabled := common.GlobalWebRateLimitEnable
	originalLimit := common.GlobalWebRateLimitNum
	originalDuration := common.GlobalWebRateLimitDuration
	common.RedisEnabled = false
	common.GlobalWebRateLimitEnable = true
	common.GlobalWebRateLimitNum = limit
	common.GlobalWebRateLimitDuration = 180
	t.Cleanup(func() {
		common.RedisEnabled = originalRedisEnabled
		common.GlobalWebRateLimitEnable = originalEnabled
		common.GlobalWebRateLimitNum = originalLimit
		common.GlobalWebRateLimitDuration = originalDuration
	})
}

func TestGlobalWebRateLimitBypassesStaticAndCreativeAppShell(t *testing.T) {
	gin.SetMode(gin.TestMode)
	configureGlobalWebRateLimitForTest(t, 1)

	engine := gin.New()
	engine.Use(GlobalWebRateLimit())
	engine.GET("/*path", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	for index, path := range []string{
		"/creative/",
		"/creative/board/demo",
		"/creative/assets/app.js",
		"/creative/sw.js",
		"/assets/admin.js",
		"/favicon.ico",
	} {
		t.Run(path, func(t *testing.T) {
			for i := 0; i < 3; i++ {
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodGet, path, nil)
				request.RemoteAddr = fmt.Sprintf("203.0.113.%d:12345", 21+index)
				engine.ServeHTTP(recorder, request)
				require.Equal(t, http.StatusNoContent, recorder.Code)
			}
		})
	}
}

func TestGlobalWebRateLimitStillAppliesToCreativeAPIAndRelay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	configureGlobalWebRateLimitForTest(t, 1)

	engine := gin.New()
	engine.Use(GlobalWebRateLimit())
	engine.GET("/*path", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	for index, path := range []string{
		"/creative/api/bootstrap",
		"/creative/relay/v1/videos/task_abc",
	} {
		t.Run(path, func(t *testing.T) {
			for i, want := range []int{http.StatusNoContent, http.StatusTooManyRequests} {
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodGet, path, nil)
				request.RemoteAddr = fmt.Sprintf("203.0.113.%d:12345", 40+index)
				engine.ServeHTTP(recorder, request)
				require.Equal(t, want, recorder.Code, "request %d", i+1)
			}
		})
	}
}

func TestGlobalWebRateLimitCreativeAPIRelay429IsNoStore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	configureGlobalWebRateLimitForTest(t, 1)

	engine := gin.New()
	engine.Use(GlobalWebRateLimit())
	engine.GET("/*path", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	for index, path := range []string{
		"/creative/api/bootstrap",
		"/creative/relay/v1/videos/task_abc",
	} {
		t.Run(path, func(t *testing.T) {
			for i, want := range []int{http.StatusNoContent, http.StatusTooManyRequests} {
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodGet, path, nil)
				request.RemoteAddr = fmt.Sprintf("203.0.113.%d:12345", 80+index)
				engine.ServeHTTP(recorder, request)
				require.Equal(t, want, recorder.Code, "request %d", i+1)
				require.Equal(t, "private, no-store", recorder.Header().Get("Cache-Control"))
				require.Equal(t, "no-cache", recorder.Header().Get("Pragma"))
				require.Equal(t, "0", recorder.Header().Get("Expires"))
			}
		})
	}
}

func TestGlobalWebRateLimitDoesNotTreatCreativeAPIDocsAsCreativeAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	configureGlobalWebRateLimitForTest(t, 1)

	engine := gin.New()
	engine.Use(GlobalWebRateLimit())
	engine.GET("/*path", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	for i := 0; i < 3; i++ {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/creative/api-docs", nil)
		request.RemoteAddr = "203.0.113.91:12345"
		engine.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusNoContent, recorder.Code, "request %d", i+1)
		require.False(t, strings.Contains(recorder.Header().Get("Cache-Control"), "no-store"))
	}
}
