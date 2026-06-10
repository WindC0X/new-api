package constant

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreativeRelayModeChatCompletions(t *testing.T) {
	require.Equal(t, RelayModeChatCompletions, Path2RelayMode("/creative/relay/v1/chat/completions"))
	require.Equal(t, RelayModeChatCompletions, Path2RelayMode("/creative/relay/v1/chat/completions/stream"))
}

func TestCreativeRelayModeImagesGenerations(t *testing.T) {
	require.Equal(t, RelayModeImagesGenerations, Path2RelayMode("/creative/relay/v1/images/generations"))
	require.Equal(t, RelayModeImagesGenerations, Path2RelayMode("/creative/relay/v1/images/generations/stream"))
	require.Equal(t, RelayModeImagesGenerations, Path2RelayMode("/v1/images/generations"))
}

func TestRelayModeVideosMapsCanonicalAndCreativePaths(t *testing.T) {
	require.Equal(t, RelayModeVideoSubmit, Path2RelayMode("/v1/videos"))
	require.Equal(t, RelayModeVideoFetchByID, Path2RelayMode("/v1/videos/task_abc"))
	require.Equal(t, RelayModeVideoSubmit, Path2RelayMode("/creative/relay/v1/videos"))
	require.Equal(t, RelayModeVideoFetchByID, Path2RelayMode("/creative/relay/v1/videos/task_abc"))
	require.Equal(t, RelayModeUnknown, Path2RelayMode("/creative/relay/v1/v1/videos"))
}

func TestRelayModeMidjourneyMapsCreativePaths(t *testing.T) {
	require.Equal(t, RelayModeMidjourneyImagine, Path2RelayMode("/creative/relay/v1/mj/submit/imagine"))
	require.Equal(t, RelayModeMidjourneyTaskFetch, Path2RelayMode("/creative/relay/v1/mj/task/task_abc/fetch"))
	require.Equal(t, RelayModeMidjourneyTaskFetchByCondition, Path2RelayMode("/creative/relay/v1/mj/task/list-by-condition"))
	require.Equal(t, RelayModeMidjourneyImage, Path2RelayMode("/creative/relay/v1/mj/image/task_abc"))
}
