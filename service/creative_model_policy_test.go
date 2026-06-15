package service

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

func TestNormalizeCreativeModelPolicyEmpty(t *testing.T) {
	policy, err := NormalizeCreativeModelPolicyJSON("")

	require.NoError(t, err)
	require.Equal(t, 1, policy.Version)
	require.Empty(t, policy.Global.Defaults)
	require.Empty(t, policy.Global.Recommended)
	require.Empty(t, policy.Groups)
}

func TestNormalizeCreativeModelPolicyDedupeAndTrim(t *testing.T) {
	policy, err := NormalizeCreativeModelPolicyJSON(`{
		"version": 1,
		"global": {
			"defaults": {"text": " gpt-4o ", "unknown": "dropped"},
			"recommended": {"text": [" gpt-4o ", "claude-sonnet", "gpt-4o", ""], "image": [" gpt-image-1 "]}
		},
		"ignored": {"display": true}
	}`)

	require.NoError(t, err)
	require.Equal(t, "gpt-4o", policy.Global.Defaults["text"])
	require.NotContains(t, policy.Global.Defaults, "unknown")
	require.Equal(t, []string{"gpt-4o", "claude-sonnet"}, policy.Global.Recommended["text"])
	require.Equal(t, []string{"gpt-image-1"}, policy.Global.Recommended["image"])
}

func TestNormalizeCreativeModelPolicyRejectsUnsafeFields(t *testing.T) {
	unsafePayloads := []string{
		`{"global":{"defaults":{"text":"gpt-4o"},"apiKey":"sk-test"}}`,
		`{"groups":{"default":{"recommended":{"text":["gpt-4o"]},"base_url":"https://upstream.example"}}}`,
		`{"global":{"defaults":{"text":"gpt-4o"}},"provider":{"name":"openai"}}`,
		`{"global":{"recommended":{"text":["gpt-4o"]}},"webhook":"https://example.test/callback"}`,
		`{"global":{"recommended":{"text":["gpt-4o"]}},"notificationUrl":"https://example.test/callback"}`,
		`{"global":{"recommended":{"text":["gpt-4o"]}},"notifyEndpoint":"https://example.test/callback"}`,
		`{"global":{"defaults":{"text":"gpt-4o"}},"ownerOverride":"vip"}`,
		`{"global":{"defaults":{"text":"gpt-4o"}},"modelOwner":"vip"}`,
		`{"global":{"defaults":{"text":"gpt-4o"}},"xOwnerId":"vip"}`,
		`{"global":{"defaults":{"text":"gpt-4o"}},"targetUserId":"42"}`,
		`{"global":{"defaults":{"text":"gpt-4o"},"routingGroup":"vip"}}`,
	}

	for _, raw := range unsafePayloads {
		t.Run(raw, func(t *testing.T) {
			_, err := NormalizeCreativeModelPolicyJSON(raw)
			require.Error(t, err)
		})
	}
}

func TestBuildEffectiveCreativeModelPolicyAppliesGroupOverrideAndFiltersStale(t *testing.T) {
	policy, err := NormalizeCreativeModelPolicyJSON(`{
		"version": 1,
		"global": {
			"defaults": {"text": "global-text", "image": "missing-image"},
			"recommended": {"text": ["global-text", "missing-text"], "audio": ["missing-audio"]}
		},
		"groups": {
			"vip": {
				"defaults": {"text": "vip-text", "video": "missing-video"},
				"recommended": {"text": ["vip-text", "global-text", "missing-text"]}
			}
		}
	}`)
	require.NoError(t, err)

	effective, version := BuildEffectiveCreativeModelPolicy(policy, "vip", []string{"global-text", "vip-text"})

	require.NotEmpty(t, version)
	require.Equal(t, 1, effective.Version)
	require.Equal(t, map[string]string{"text": "vip-text"}, effective.Defaults)
	require.Equal(t, []string{"vip-text", "global-text"}, effective.Recommended["text"])
	require.NotNil(t, effective.Stale)
	require.Equal(t, "missing-image", effective.Stale.Defaults["image"])
	require.Equal(t, "missing-video", effective.Stale.Defaults["video"])
	require.Equal(t, []string{"missing-text"}, effective.Stale.Recommended["text"])
	require.Equal(t, []string{"missing-audio"}, effective.Stale.Recommended["audio"])
}

func TestBuildEffectiveCreativeModelPolicyFiltersByModalityEndpoint(t *testing.T) {
	policy, err := NormalizeCreativeModelPolicyJSON(`{
		"version": 1,
		"global": {
			"defaults": {"text": "text-only", "image": "text-only", "video": "video-model", "audio": "suno_music"},
			"recommended": {"image": ["text-only", "gpt-image-1"], "video": ["text-only", "video-model"], "audio": ["text-only", "suno_music"]}
		}
	}`)
	require.NoError(t, err)

	effective, _ := BuildEffectiveCreativeModelPolicyForModels(policy, "default", []CreativeModelPolicyAvailableModel{
		{ID: "text-only", SupportedEndpointTypes: []constant.EndpointType{constant.EndpointTypeOpenAI}},
		{ID: "gpt-image-1", SupportedEndpointTypes: []constant.EndpointType{constant.EndpointTypeImageGeneration}},
		{ID: "video-model", SupportedEndpointTypes: []constant.EndpointType{constant.EndpointTypeOpenAIVideo}},
		{ID: "suno_music"},
	})

	require.Equal(t, "text-only", effective.Defaults["text"])
	require.NotContains(t, effective.Defaults, "image")
	require.Equal(t, "video-model", effective.Defaults["video"])
	require.Equal(t, "suno_music", effective.Defaults["audio"])
	require.Equal(t, []string{"gpt-image-1"}, effective.Recommended["image"])
	require.Equal(t, []string{"video-model"}, effective.Recommended["video"])
	require.Equal(t, []string{"suno_music"}, effective.Recommended["audio"])
	require.NotNil(t, effective.Stale)
	require.Equal(t, "text-only", effective.Stale.Defaults["image"])
	require.Equal(t, []string{"text-only"}, effective.Stale.Recommended["image"])
	require.Equal(t, []string{"text-only"}, effective.Stale.Recommended["video"])
	require.Equal(t, []string{"text-only"}, effective.Stale.Recommended["audio"])
}
