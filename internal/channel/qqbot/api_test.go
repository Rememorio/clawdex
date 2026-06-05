package qqbot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── Token management tests ──

func TestAPIClient_GetAccessToken_CachesToken(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":"7200"}`, callCount)
	}))
	defer srv.Close()

	client := &apiClient{
		appID:        "test-app",
		clientSecret: "test-secret",
		httpClient:   &http.Client{Transport: &rewriteTransport{target: srv.URL}},
	}

	tok1, err := client.getAccessToken()
	require.NoError(t, err)
	assert.Equal(t, "tok-1", tok1)

	// Second call should use cache.
	tok2, err := client.getAccessToken()
	require.NoError(t, err)
	assert.Equal(t, "tok-1", tok2)
	assert.Equal(t, 1, callCount, "token should be cached")
}

func TestAPIClient_GetAccessToken_RefreshesExpired(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":"7200"}`, callCount)
	}))
	defer srv.Close()

	client := &apiClient{
		appID:        "test-app",
		clientSecret: "test-secret",
		httpClient:   &http.Client{Transport: &rewriteTransport{target: srv.URL}},
	}

	tok1, err := client.getAccessToken()
	require.NoError(t, err)
	assert.Equal(t, "tok-1", tok1)

	// Force expiry.
	client.mu.Lock()
	client.expiresAt = time.Now().Add(-1 * time.Second)
	client.mu.Unlock()

	tok2, err := client.getAccessToken()
	require.NoError(t, err)
	assert.Equal(t, "tok-2", tok2)
	assert.Equal(t, 2, callCount)
}

func TestAPIClient_GetAccessToken_ErrorOnBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"message":"bad credentials"}`)
	}))
	defer srv.Close()

	client := &apiClient{
		appID:        "bad-app",
		clientSecret: "bad-secret",
		httpClient:   &http.Client{Transport: &rewriteTransport{target: srv.URL}},
	}

	_, err := client.getAccessToken()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

// ── Gateway URL tests ──

func TestAPIClient_GetGatewayURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gateway" {
			assert.Contains(t, r.Header.Get("Authorization"), "QQBot ")
			fmt.Fprint(w, `{"url":"wss://gateway.example.com"}`)
			return
		}
		// Token endpoint.
		fmt.Fprint(w, `{"access_token":"test-tok","expires_in":"7200"}`)
	}))
	defer srv.Close()

	client := &apiClient{
		appID:        "app",
		clientSecret: "secret",
		httpClient:   &http.Client{Transport: &rewriteTransport{target: srv.URL}},
	}

	url, err := client.getGatewayURL()
	require.NoError(t, err)
	assert.Equal(t, "wss://gateway.example.com", url)
}

// ── Message send tests ──

func TestSendC2CMessage(t *testing.T) {
	var receivedPath string
	var receivedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"id":"resp-1","timestamp":"123"}`)
	}))
	defer srv.Close()

	client := newCachedClient(srv.URL)
	err := client.sendC2CMessage("user-open-abc", "test message", "msg-trigger", 1)
	require.NoError(t, err)
	assert.Equal(t, "/v2/users/user-open-abc/messages", receivedPath)
	assert.Equal(t, "test message", receivedBody["content"])
	assert.Equal(t, float64(0), receivedBody["msg_type"])
	assert.Equal(t, "msg-trigger", receivedBody["msg_id"])
	assert.Equal(t, float64(1), receivedBody["msg_seq"])
}

func TestSendGroupMessage(t *testing.T) {
	var receivedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"id":"resp-2","timestamp":"456"}`)
	}))
	defer srv.Close()

	client := newCachedClient(srv.URL)
	err := client.sendGroupMessage("group-xyz", "group msg", "msg-trigger", 1)
	require.NoError(t, err)
	assert.Equal(t, "/v2/groups/group-xyz/messages", receivedPath)
}

func TestSendC2CMessage_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"message":"rate limited","code":11264}`)
	}))
	defer srv.Close()

	client := newCachedClient(srv.URL)
	err := client.sendC2CMessage("user-x", "hi", "msg-1", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "429")
}

// ── Token 401 retry tests ──

func TestDoPost_Retries401(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.URL.Path == "/v2/users/u/messages" {
			if callCount == 1 {
				// First attempt: simulate expired token.
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"message":"token expired"}`)
				return
			}
			// Second attempt: success.
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"id":"ok","timestamp":"1"}`)
			return
		}
		// Token refresh endpoint.
		fmt.Fprint(w, `{"access_token":"fresh-tok","expires_in":"7200"}`)
	}))
	defer srv.Close()

	client := &apiClient{
		appID:        "app",
		clientSecret: "secret",
		httpClient:   &http.Client{Transport: &rewriteTransport{target: srv.URL}},
		token:        "stale-token",
		expiresAt:    time.Now().Add(1 * time.Hour), // looks valid but server rejects
	}

	err := client.sendC2CMessage("u", "hi", "m", 1)
	require.NoError(t, err)
	// Should have called the endpoint twice (1 fail + 1 retry) plus 1 token refresh.
	assert.GreaterOrEqual(t, callCount, 2)
}

// ── Input notify tests ──

func TestSendC2CInputNotify(t *testing.T) {
	var receivedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	client := newCachedClient(srv.URL)
	err := client.sendC2CInputNotify("user-abc", "msg-trigger")
	require.NoError(t, err)
	assert.Equal(t, float64(6), receivedBody["msg_type"])
	assert.Equal(t, "msg-trigger", receivedBody["msg_id"])
}

// ── Download attachment tests ──

func TestDownloadAttachment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("fake-image-data"))
	}))
	defer srv.Close()

	client := &apiClient{httpClient: srv.Client()}
	tmpDir := t.TempDir()

	path, err := client.downloadAttachment(srv.URL+"/image.png", tmpDir)
	require.NoError(t, err)
	assert.Contains(t, path, tmpDir)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "fake-image-data", string(data))
}

func TestDownloadAttachment_BadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &apiClient{httpClient: srv.Client()}
	_, err := client.downloadAttachment(srv.URL+"/missing", t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

// ── Media upload tests ──

func TestUploadMedia(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/files")
		assert.Contains(t, r.Header.Get("Content-Type"), "multipart/form-data")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"file_info":"fi-123","file_uuid":"uuid-456","ttl":300}`)
	}))
	defer srv.Close()

	client := newCachedClient(srv.URL)
	tmpFile := filepath.Join(t.TempDir(), "test.png")
	require.NoError(t, os.WriteFile(tmpFile, []byte("fake-png"), 0o644))

	fileInfo, err := client.uploadMedia("/v2/users/user-x/files", tmpFile)
	require.NoError(t, err)
	assert.Equal(t, "fi-123", fileInfo)
}

func TestUploadMedia_FileNotFound(t *testing.T) {
	client := newCachedClient("http://unused")
	_, err := client.uploadMedia("/v2/users/user-x/files", "/tmp/nonexistent-file-qqbot-test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open media file")
}

// ── File type resolution tests ──

func TestResolveFileType(t *testing.T) {
	tests := []struct {
		path     string
		expected int
	}{
		{"photo.jpg", 1},
		{"photo.JPEG", 1},
		{"photo.PNG", 1},
		{"photo.gif", 1},
		{"photo.webp", 1},
		{"video.mp4", 2},
		{"video.mov", 2},
		{"audio.mp3", 3},
		{"audio.wav", 3},
		{"audio.silk", 3},
		{"doc.pdf", 4},
		{"noext", 4},
		{"archive.zip", 4},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			assert.Equal(t, tt.expected, resolveFileType(tt.path))
		})
	}
}

// ── Helpers ──

// newCachedClient returns an apiClient with a pre-cached token for testing.
func newCachedClient(targetURL string) *apiClient {
	return &apiClient{
		appID:        "app",
		clientSecret: "secret",
		httpClient:   &http.Client{Transport: &rewriteTransport{target: targetURL}},
		token:        "cached-token",
		expiresAt:    time.Now().Add(1 * time.Hour),
	}
}

// rewriteTransport rewrites all requests to point at a test server.
type rewriteTransport struct {
	base   http.RoundTripper
	target string
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = t.target[len("http://"):]
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// ── getAccessToken additional tests ──

func TestAPIClient_GetAccessToken_DefaultExpiresIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// expires_in is "0" which should default to 7200
		fmt.Fprint(w, `{"access_token":"tok-default","expires_in":"0"}`)
	}))
	defer srv.Close()

	client := &apiClient{
		appID:        "app",
		clientSecret: "secret",
		httpClient:   &http.Client{Transport: &rewriteTransport{target: srv.URL}},
	}

	tok, err := client.getAccessToken()
	require.NoError(t, err)
	assert.Equal(t, "tok-default", tok)
	// Token should be cached with the default expiry
	assert.True(t, client.expiresAt.After(time.Now().Add(7000*time.Second)))
}

func TestAPIClient_GetAccessToken_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `not-json`)
	}))
	defer srv.Close()

	client := &apiClient{
		appID:        "app",
		clientSecret: "secret",
		httpClient:   &http.Client{Transport: &rewriteTransport{target: srv.URL}},
	}

	_, err := client.getAccessToken()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode token response")
}

func TestAPIClient_GetAccessToken_SendsCorrectBody(t *testing.T) {
	var receivedBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"tok","expires_in":"7200"}`)
	}))
	defer srv.Close()

	client := &apiClient{
		appID:        "my-app-id",
		clientSecret: "my-client-secret",
		httpClient:   &http.Client{Transport: &rewriteTransport{target: srv.URL}},
	}

	_, err := client.getAccessToken()
	require.NoError(t, err)
	assert.Equal(t, "my-app-id", receivedBody["appId"])
	assert.Equal(t, "my-client-secret", receivedBody["clientSecret"])
}

// ── clearToken tests ──

func TestAPIClient_ClearToken(t *testing.T) {
	client := &apiClient{
		token:     "existing-token",
		expiresAt: time.Now().Add(1 * time.Hour),
	}

	client.clearToken()

	client.mu.Lock()
	defer client.mu.Unlock()
	assert.Equal(t, "", client.token)
	assert.True(t, client.expiresAt.IsZero())
}

func TestAPIClient_ClearToken_ForcesRefresh(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":"7200"}`, callCount)
	}))
	defer srv.Close()

	client := &apiClient{
		appID:        "app",
		clientSecret: "secret",
		httpClient:   &http.Client{Transport: &rewriteTransport{target: srv.URL}},
		token:        "old-token",
		expiresAt:    time.Now().Add(1 * time.Hour),
	}

	// Before clear, should use cached token
	tok, err := client.getAccessToken()
	require.NoError(t, err)
	assert.Equal(t, "old-token", tok)
	assert.Equal(t, 0, callCount)

	// After clear, should fetch new token
	client.clearToken()
	tok, err = client.getAccessToken()
	require.NoError(t, err)
	assert.Equal(t, "tok-1", tok)
	assert.Equal(t, 1, callCount)
}

// ── doPost tests ──

func TestDoPost_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "QQBot test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := newCachedClient(srv.URL)
	err := client.doPost("test-token", "/v2/test", map[string]string{"key": "val"})
	assert.NoError(t, err)
}

func TestDoPost_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"message":"internal error"}`)
	}))
	defer srv.Close()

	client := newCachedClient(srv.URL)
	err := client.doPost("token", "/v2/test", map[string]string{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestDoPost_401Retry_Success(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/test" {
			attempts++
			if attempts == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"message":"unauthorized"}`)
				return
			}
			// Verify new token is used on retry
			assert.Equal(t, "QQBot fresh-tok", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusOK)
			return
		}
		// Token refresh endpoint
		fmt.Fprint(w, `{"access_token":"fresh-tok","expires_in":"7200"}`)
	}))
	defer srv.Close()

	client := &apiClient{
		appID:        "app",
		clientSecret: "secret",
		httpClient:   &http.Client{Transport: &rewriteTransport{target: srv.URL}},
		token:        "stale-tok",
		expiresAt:    time.Now().Add(1 * time.Hour),
	}

	err := client.doPost("stale-tok", "/v2/test", map[string]string{})
	assert.NoError(t, err)
	assert.Equal(t, 2, attempts)
}

func TestDoPost_401Retry_TokenRefreshFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/test" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"unauthorized"}`)
			return
		}
		// Token refresh also fails
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"forbidden"}`)
	}))
	defer srv.Close()

	client := &apiClient{
		appID:        "app",
		clientSecret: "secret",
		httpClient:   &http.Client{Transport: &rewriteTransport{target: srv.URL}},
		token:        "bad-tok",
		expiresAt:    time.Now().Add(1 * time.Hour),
	}

	err := client.doPost("bad-tok", "/v2/test", map[string]string{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

// ── sendC2CMessage additional tests ──

func TestSendC2CMessage_VerifiesRequestFormat(t *testing.T) {
	var receivedBody map[string]any
	var receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := newCachedClient(srv.URL)
	err := client.sendC2CMessage("user-123", "hello world", "trigger-msg", 5)
	require.NoError(t, err)

	assert.Equal(t, "QQBot cached-token", receivedAuth)
	assert.Equal(t, "hello world", receivedBody["content"])
	assert.Equal(t, float64(0), receivedBody["msg_type"])
	assert.Equal(t, "trigger-msg", receivedBody["msg_id"])
	assert.Equal(t, float64(5), receivedBody["msg_seq"])
}

// ── sendGroupMessage additional tests ──

func TestSendGroupMessage_VerifiesRequestFormat(t *testing.T) {
	var receivedPath string
	var receivedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := newCachedClient(srv.URL)
	err := client.sendGroupMessage("group-abc", "group content", "msg-ref", 3)
	require.NoError(t, err)

	assert.Equal(t, "/v2/groups/group-abc/messages", receivedPath)
	assert.Equal(t, "group content", receivedBody["content"])
	assert.Equal(t, float64(0), receivedBody["msg_type"])
	assert.Equal(t, "msg-ref", receivedBody["msg_id"])
	assert.Equal(t, float64(3), receivedBody["msg_seq"])
}

func TestSendGroupMessage_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"no permission"}`)
	}))
	defer srv.Close()

	client := newCachedClient(srv.URL)
	err := client.sendGroupMessage("g-x", "msg", "m-1", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
}

// ── apiError Error() method tests ──

func TestAPIError_Error(t *testing.T) {
	err := &apiError{status: 429, body: `{"message":"rate limited"}`}
	assert.Equal(t, `qqbot: api returned 429: {"message":"rate limited"}`, err.Error())
}

func TestAPIError_ErrorEmptyBody(t *testing.T) {
	err := &apiError{status: 500, body: ""}
	assert.Equal(t, "qqbot: api returned 500: ", err.Error())
}

func TestIsUnauthorized_True(t *testing.T) {
	err := &apiError{status: 401, body: "unauthorized"}
	assert.True(t, isUnauthorized(err))
}

func TestIsUnauthorized_False(t *testing.T) {
	err := &apiError{status: 403, body: "forbidden"}
	assert.False(t, isUnauthorized(err))
}

func TestIsUnauthorized_NonAPIError(t *testing.T) {
	err := fmt.Errorf("some other error")
	assert.False(t, isUnauthorized(err))
}

// ── postMediaMessage tests ──

func TestPostMediaMessage_Success(t *testing.T) {
	var receivedBody map[string]any
	var receivedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := newCachedClient(srv.URL)
	err := client.postMediaMessage("/v2/users/user-x/messages", "fi-test-123", "msg-trigger", 2)
	require.NoError(t, err)

	assert.Equal(t, "/v2/users/user-x/messages", receivedPath)
	assert.Equal(t, float64(7), receivedBody["msg_type"]) // rich media type
	assert.Equal(t, "msg-trigger", receivedBody["msg_id"])
	assert.Equal(t, float64(2), receivedBody["msg_seq"])
	media, ok := receivedBody["media"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "fi-test-123", media["file_info"])
}

func TestPostMediaMessage_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"no permission"}`)
	}))
	defer srv.Close()

	client := newCachedClient(srv.URL)
	err := client.postMediaMessage("/v2/users/user-x/messages", "fi-xxx", "msg-1", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
}

// ── sendC2CMedia tests ──

func TestSendC2CMedia_Success(t *testing.T) {
	var uploadCalled, sendCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/users/user-x/files" {
			uploadCalled = true
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"file_info":"fi-c2c","file_uuid":"uuid-1","ttl":300}`)
			return
		}
		if r.URL.Path == "/v2/users/user-x/messages" {
			sendCalled = true
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := newCachedClient(srv.URL)
	tmpFile := filepath.Join(t.TempDir(), "photo.jpg")
	require.NoError(t, os.WriteFile(tmpFile, []byte("fake-jpg"), 0o644))

	err := client.sendC2CMedia("user-x", tmpFile, "msg-1", 1)
	require.NoError(t, err)
	assert.True(t, uploadCalled)
	assert.True(t, sendCalled)
}

func TestSendC2CMedia_UploadFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"message":"upload error"}`)
	}))
	defer srv.Close()

	client := newCachedClient(srv.URL)
	tmpFile := filepath.Join(t.TempDir(), "video.mp4")
	require.NoError(t, os.WriteFile(tmpFile, []byte("fake-mp4"), 0o644))

	err := client.sendC2CMedia("user-x", tmpFile, "msg-1", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestSendC2CMedia_FileNotFound(t *testing.T) {
	client := newCachedClient("http://unused")
	err := client.sendC2CMedia("user-x", "/tmp/nonexistent-qqbot-media-test-file", "msg-1", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open media file")
}

// ── sendGroupMedia tests ──

func TestSendGroupMedia_Success(t *testing.T) {
	var uploadPath, sendPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/groups/grp-1/files" {
			uploadPath = r.URL.Path
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"file_info":"fi-grp","file_uuid":"uuid-2","ttl":300}`)
			return
		}
		if r.URL.Path == "/v2/groups/grp-1/messages" {
			sendPath = r.URL.Path
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := newCachedClient(srv.URL)
	tmpFile := filepath.Join(t.TempDir(), "doc.pdf")
	require.NoError(t, os.WriteFile(tmpFile, []byte("fake-pdf"), 0o644))

	err := client.sendGroupMedia("grp-1", tmpFile, "msg-2", 1)
	require.NoError(t, err)
	assert.Equal(t, "/v2/groups/grp-1/files", uploadPath)
	assert.Equal(t, "/v2/groups/grp-1/messages", sendPath)
}

func TestSendGroupMedia_FileNotFound(t *testing.T) {
	client := newCachedClient("http://unused")
	err := client.sendGroupMedia("grp-1", "/tmp/nonexistent-qqbot-media-test-file", "msg-1", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open media file")
}

// ── getAccessToken additional error cases ──

func TestAPIClient_GetAccessToken_EmptyToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"","expires_in":"7200"}`)
	}))
	defer srv.Close()

	client := &apiClient{
		appID:        "app",
		clientSecret: "secret",
		httpClient:   &http.Client{Transport: &rewriteTransport{target: srv.URL}},
	}

	tok, err := client.getAccessToken()
	require.NoError(t, err)
	// Even an empty token is stored — the API didn't error.
	assert.Equal(t, "", tok)
}

// ── getGatewayURL error paths ──

func TestAPIClient_GetGatewayURL_BadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gateway" {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `service down`)
			return
		}
		fmt.Fprint(w, `{"access_token":"tok","expires_in":"7200"}`)
	}))
	defer srv.Close()

	client := &apiClient{
		appID:        "app",
		clientSecret: "secret",
		httpClient:   &http.Client{Transport: &rewriteTransport{target: srv.URL}},
	}

	_, err := client.getGatewayURL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "503")
}

func TestAPIClient_GetGatewayURL_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gateway" {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `not-json`)
			return
		}
		fmt.Fprint(w, `{"access_token":"tok","expires_in":"7200"}`)
	}))
	defer srv.Close()

	client := &apiClient{
		appID:        "app",
		clientSecret: "secret",
		httpClient:   &http.Client{Transport: &rewriteTransport{target: srv.URL}},
	}

	_, err := client.getGatewayURL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode gateway response")
}

// ── doPost with nil body ──

func TestDoPost_NilBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := newCachedClient(srv.URL)
	// nil body should still marshal to "null" and succeed.
	err := client.doPost("cached-token", "/v2/test", nil)
	assert.NoError(t, err)
}
