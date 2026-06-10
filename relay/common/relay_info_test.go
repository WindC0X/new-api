package common

import (
	"net/http"
	"net/http/httptest"
	"testing"

	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRelayInfoGetFinalRequestRelayFormatPrefersExplicitFinal(t *testing.T) {
	info := &RelayInfo{
		RelayFormat:             types.RelayFormatOpenAI,
		RequestConversionChain:  []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude},
		FinalRequestRelayFormat: types.RelayFormatOpenAIResponses,
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatOpenAIResponses), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatFallsBackToConversionChain(t *testing.T) {
	info := &RelayInfo{
		RelayFormat:            types.RelayFormatOpenAI,
		RequestConversionChain: []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude},
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatClaude), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatFallsBackToRelayFormat(t *testing.T) {
	info := &RelayInfo{
		RelayFormat: types.RelayFormatGemini,
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatGemini), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatNilReceiver(t *testing.T) {
	var info *RelayInfo
	require.Equal(t, types.RelayFormat(""), info.GetFinalRequestRelayFormat())
}

func TestCreativeRelayModeNormalizesPathForBillingReuse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/creative/relay/v1/chat/completions?include_usage=1", nil)

	info := GenRelayInfoOpenAI(ctx, nil)

	require.Equal(t, relayconstant.RelayModeChatCompletions, info.RelayMode)
	require.Equal(t, "/v1/chat/completions?include_usage=1", info.RequestURLPath)
	require.True(t, info.IsPlayground)
	require.Equal(t, types.RelayFormatOpenAI, info.RelayFormat)
}

func TestCreativeImageRelayModeNormalizesPathForBillingReuse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/creative/relay/v1/images/generations?size=1024x1024", nil)

	info := GenRelayInfoImage(ctx, nil)

	require.Equal(t, relayconstant.RelayModeImagesGenerations, info.RelayMode)
	require.Equal(t, "/v1/images/generations?size=1024x1024", info.RequestURLPath)
	require.True(t, info.IsPlayground)
	require.Equal(t, types.RelayFormat(types.RelayFormatOpenAIImage), info.RelayFormat)
}

func TestCreativeVideoRelayModeNormalizesPathForTaskReuse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/creative/relay/v1/videos?trace=1", nil)

	info, err := GenRelayInfo(ctx, types.RelayFormatTask, nil, nil)

	require.NoError(t, err)
	require.Equal(t, relayconstant.RelayModeVideoSubmit, info.RelayMode)
	require.Equal(t, "/v1/videos?trace=1", info.RequestURLPath)
	require.True(t, info.IsPlayground)
	require.Equal(t, types.RelayFormat(types.RelayFormatTask), info.RelayFormat)
}

func TestCreativeVideoFetchRelayModeNormalizesPathForTaskReuse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/creative/relay/v1/videos/task_abc?trace=1", nil)

	info, err := GenRelayInfo(ctx, types.RelayFormatTask, nil, nil)

	require.NoError(t, err)
	require.Equal(t, relayconstant.RelayModeVideoFetchByID, info.RelayMode)
	require.Equal(t, "/v1/videos/task_abc?trace=1", info.RequestURLPath)
	require.True(t, info.IsPlayground)
}
