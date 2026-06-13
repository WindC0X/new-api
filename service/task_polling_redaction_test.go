package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactTaskResponseBodyForLogRemovesURLsSecretsAndLargePayloads(t *testing.T) {
	body := []byte(`{
		"status":"completed",
		"result_url":"https://storage.example.com/private/video.mp4?token=secret",
		"response":{"bytesBase64Encoded":"` + strings.Repeat("A", 256) + `","video":"` + strings.Repeat("B", 256) + `"},
		"api_key":"sk-secret"
	}`)

	redacted := redactTaskResponseBodyForLog(body)

	require.NotContains(t, redacted, "storage.example.com")
	require.NotContains(t, redacted, "token=secret")
	require.NotContains(t, redacted, "sk-secret")
	require.NotContains(t, redacted, strings.Repeat("A", 64))
	require.Contains(t, redacted, "[redacted]")
}

func TestRedactTaskResponseBodyForLogDoesNotEchoUnparseableBody(t *testing.T) {
	body := []byte("upstream error with https://storage.example.com/private?token=secret")

	redacted := redactTaskResponseBodyForLog(body)

	require.Contains(t, redacted, "[unparseable response body")
	require.NotContains(t, redacted, "storage.example.com")
	require.NotContains(t, redacted, "token=secret")
}
