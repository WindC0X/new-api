package taskcommon

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnmarshalMetadataDoesNotOverrideServerSelectedModelAliases(t *testing.T) {
	target := struct {
		Model     string `json:"model"`
		ModelName string `json:"model_name"`
		ReqKey    string `json:"req_key"`
		Prompt    string `json:"prompt"`
	}{
		Model:     "server-model",
		ModelName: "server-model-name",
		ReqKey:    "server-req-key",
	}

	metadata := map[string]any{
		"model":      "client-model",
		"model_name": "client-model-name",
		"modelName":  "client-model-name-camel",
		"req_key":    "client-req-key",
		"prompt":     "safe prompt metadata",
	}

	require.NoError(t, UnmarshalMetadata(metadata, &target))

	require.Equal(t, "server-model", target.Model)
	require.Equal(t, "server-model-name", target.ModelName)
	require.Equal(t, "server-req-key", target.ReqKey)
	require.Equal(t, "safe prompt metadata", target.Prompt)
}
