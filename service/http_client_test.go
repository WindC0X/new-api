package service

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/require"
)

func TestDefaultHTTPClientFallbackKeepsRedirectPolicy(t *testing.T) {
	previous := httpClient
	httpClient = nil
	t.Cleanup(func() { httpClient = previous })

	client, err := GetHttpClientWithProxy("")
	require.NoError(t, err)
	require.NotNil(t, client)
	require.NotNil(t, client.CheckRedirect)
	require.NotSame(t, http.DefaultClient, client)

	proxyClient, err := NewProxyHttpClient("")
	require.NoError(t, err)
	require.NotNil(t, proxyClient)
	require.NotNil(t, proxyClient.CheckRedirect)
	require.NotSame(t, http.DefaultClient, proxyClient)
}

func TestRedirectPolicyStripsSensitiveHeadersOnCrossHostRedirect(t *testing.T) {
	fetchSetting := system_setting.GetFetchSetting()
	previousSSRFProtection := fetchSetting.EnableSSRFProtection
	fetchSetting.EnableSSRFProtection = false
	t.Cleanup(func() { fetchSetting.EnableSSRFProtection = previousSSRFProtection })

	redirectReq := &http.Request{
		URL: &url.URL{Scheme: "https", Host: "cdn.example", Path: "/next"},
		Header: http.Header{
			"Authorization":  []string{"Bearer secret"},
			"Cookie2":        []string{"legacy-cookie-secret"},
			"X-User-Cookie":  []string{"cookie-secret"},
			"X-Goog-Api-Key": []string{"google-secret"},
			"X-Api-Key":      []string{"api-secret"},
			"Accept":         []string{"video/mp4"},
		},
	}
	via := []*http.Request{{URL: &url.URL{Scheme: "https", Host: "provider.example", Path: "/video"}}}

	require.NoError(t, checkRedirect(redirectReq, via))
	require.Empty(t, redirectReq.Header.Values("Authorization"))
	require.Empty(t, redirectReq.Header.Values("Cookie2"))
	require.Empty(t, redirectReq.Header.Values("X-User-Cookie"))
	require.Empty(t, redirectReq.Header.Values("X-Goog-Api-Key"))
	require.Empty(t, redirectReq.Header.Values("X-Api-Key"))
	require.Equal(t, []string{"video/mp4"}, redirectReq.Header.Values("Accept"))
}

func TestRedirectPolicyErrorRedactsBlockedURLMaterial(t *testing.T) {
	fetchSetting := system_setting.GetFetchSetting()
	previousSSRFProtection := fetchSetting.EnableSSRFProtection
	previousAllowPrivateIP := fetchSetting.AllowPrivateIp
	fetchSetting.EnableSSRFProtection = true
	fetchSetting.AllowPrivateIp = false
	t.Cleanup(func() {
		fetchSetting.EnableSSRFProtection = previousSSRFProtection
		fetchSetting.AllowPrivateIp = previousAllowPrivateIP
	})

	redirectReq := &http.Request{
		URL: &url.URL{
			Scheme:   "http",
			Host:     "127.0.0.1:8080",
			Path:     "/private/object.mp4",
			RawQuery: "X-Amz-Signature=secret&token=leak",
		},
		Header: http.Header{"Accept": []string{"video/mp4"}},
	}

	err := checkRedirect(redirectReq, []*http.Request{{URL: &url.URL{Scheme: "https", Host: "provider.example"}}})

	require.Error(t, err)
	require.NotContains(t, err.Error(), "X-Amz-Signature")
	require.NotContains(t, err.Error(), "secret")
	require.NotContains(t, err.Error(), "token=leak")
	require.NotContains(t, err.Error(), "/private/object.mp4")
}
