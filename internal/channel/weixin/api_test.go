package weixin

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPIGetUpdates(t *testing.T) {
	msgs := []weixinMessage{
		{
			MessageID:    1,
			FromUserID:   "user@im.wechat",
			MessageType:  messageTypeUser,
			ContextToken: "ctx-tok",
			ItemList: []messageItem{
				{Type: itemTypeText, TextItem: &textItem{Text: "hello"}},
			},
		},
	}
	resp := getUpdatesResp{
		Ret:           0,
		Msgs:          msgs,
		GetUpdatesBuf: "new-buf",
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "ilink_bot_token", r.Header.Get("AuthorizationType"))
		assert.Contains(t, r.Header.Get("Authorization"), "Bearer test-token")
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	client := newAPIClient(ts.URL, "test-token")
	got, err := client.getUpdates(context.Background(), "old-buf", 1000)
	require.NoError(t, err)
	assert.Equal(t, "new-buf", got.GetUpdatesBuf)
	require.Len(t, got.Msgs, 1)
	assert.Equal(t, "user@im.wechat", got.Msgs[0].FromUserID)
}

func TestAPISendMessage(t *testing.T) {
	var received sendMessageReq
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&received)
		json.NewEncoder(w).Encode(sendMessageResp{Ret: 0})
	}))
	defer ts.Close()

	client := newAPIClient(ts.URL, "test-token")
	msg := &weixinMessage{
		ToUserID:     "user@im.wechat",
		ClientID:     "test-client-id",
		MessageType:  messageTypeBot,
		MessageState: messageStateFinish,
		ItemList: []messageItem{
			{Type: itemTypeText, TextItem: &textItem{Text: "hi"}},
		},
		ContextToken: "ctx",
	}
	err := client.sendMessage(context.Background(), msg)
	require.NoError(t, err)
	assert.Equal(t, "user@im.wechat", received.Msg.ToUserID)
	assert.Equal(t, "ctx", received.Msg.ContextToken)
}

func TestAPISendMessageError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(sendMessageResp{Ret: -1, ErrMsg: "bad request"})
	}))
	defer ts.Close()

	client := newAPIClient(ts.URL, "test-token")
	err := client.sendMessage(context.Background(), &weixinMessage{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "bad request")
}

func TestAPISendTyping(t *testing.T) {
	var received sendTypingReq
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&received)
		json.NewEncoder(w).Encode(sendTypingResp{Ret: 0})
	}))
	defer ts.Close()

	client := newAPIClient(ts.URL, "test-token")
	err := client.sendTyping(context.Background(), "user@im.wechat", "ticket123")
	require.NoError(t, err)
	assert.Equal(t, "user@im.wechat", received.IlinkUserID)
	assert.Equal(t, "ticket123", received.TypingTicket)
	assert.Equal(t, 1, received.Status)
}

func TestAPISendTypingError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	client := newAPIClient(ts.URL, "tok")
	err := client.sendTyping(context.Background(), "user", "ticket")
	assert.Error(t, err)
}

func TestAPIGetConfig(t *testing.T) {
	var received map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&received)
		json.NewEncoder(w).Encode(getConfigResp{Ret: 0, TypingTicket: "fresh-ticket"})
	}))
	defer ts.Close()

	client := newAPIClient(ts.URL, "test-token")
	resp, err := client.getConfig(context.Background(), "user@im.wechat", "ctx-tok")
	require.NoError(t, err)
	assert.Equal(t, "fresh-ticket", resp.TypingTicket)
	assert.Equal(t, "user@im.wechat", received["ilink_user_id"])
	assert.Equal(t, "ctx-tok", received["context_token"])
}

func TestAPINotifyStartStop(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		json.NewEncoder(w).Encode(notifyResp{Ret: 0})
	}))
	defer ts.Close()

	client := newAPIClient(ts.URL, "test-token")
	assert.NoError(t, client.notifyStart(context.Background()))
	assert.NoError(t, client.notifyStop(context.Background()))
	assert.Equal(t, 2, calls)
}

func TestAPIGetUploadURL(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req getUploadURLReq
		json.NewDecoder(r.Body).Decode(&req)
		assert.Equal(t, "test-key", req.FileKey)
		assert.Equal(t, uploadMediaTypeImage, req.MediaType)
		json.NewEncoder(w).Encode(getUploadURLResp{
			Ret:           0,
			UploadParam:   "upload-param-abc",
			UploadFullURL: "https://cdn.example.com/upload",
		})
	}))
	defer ts.Close()

	client := newAPIClient(ts.URL, "test-token")
	resp, err := client.getUploadURL(context.Background(), &getUploadURLReq{
		FileKey:   "test-key",
		MediaType: uploadMediaTypeImage,
	})
	require.NoError(t, err)
	assert.Equal(t, "upload-param-abc", resp.UploadParam)
	assert.Equal(t, "https://cdn.example.com/upload", resp.UploadFullURL)
}

func TestAPIGetUploadURLError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(getUploadURLResp{Ret: -1, ErrMsg: "quota exceeded"})
	}))
	defer ts.Close()

	client := newAPIClient(ts.URL, "test-token")
	_, err := client.getUploadURL(context.Background(), &getUploadURLReq{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "quota exceeded")
}

func TestAPIGetUploadURLAllFields(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(getUploadURLResp{
			Ret:           0,
			UploadParam:   "param",
			UploadFullURL: "https://cdn.test/upload",
		})
	}))
	defer ts.Close()

	client := newAPIClient(ts.URL, "tok")
	req := &getUploadURLReq{
		FileKey:     "k",
		MediaType:   uploadMediaTypeFile,
		ToUserID:    "u",
		RawSize:     100,
		RawFileMD5:  "abc",
		FileSize:    112,
		NoNeedThumb: true,
		AESKeyStr:   "0123456789abcdef",
	}
	resp, err := client.getUploadURL(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "param", resp.UploadParam)
}

func TestAPIUploadToCDN(t *testing.T) {
	var receivedData []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedData, _ = io.ReadAll(r.Body)
		assert.Equal(t, "application/octet-stream", r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := newAPIClient(ts.URL, "test-token")
	data := []byte("encrypted-content")
	err := client.uploadToCDN(context.Background(), ts.URL+"/upload", data)
	require.NoError(t, err)
	assert.Equal(t, data, receivedData)
}

func TestAPIUploadToCDNError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("forbidden"))
	}))
	defer ts.Close()

	client := newAPIClient(ts.URL, "test-token")
	err := client.uploadToCDN(context.Background(), ts.URL+"/upload", []byte("data"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "403")
}

func TestAPIDownloadMedia(t *testing.T) {
	// Serve a "encrypted" file (actually just plaintext since no key).
	content := []byte("image-data-here")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(content)
	}))
	defer ts.Close()

	client := newAPIClient(ts.URL, "test-token")
	tmpDir := t.TempDir()

	media := &cdnMedia{FullURL: ts.URL + "/image.jpg"}
	path, err := client.downloadMedia(context.Background(), media, "", tmpDir, "test.jpg")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(tmpDir, "test.jpg"), path)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, content, data)
}

func TestAPIDownloadMediaWithDecryption(t *testing.T) {
	// Encrypt some data and serve it.
	key := []byte("0123456789abcdef")
	plaintext := []byte("secret image content")
	ciphertext, err := aesECBEncrypt(plaintext, key)
	require.NoError(t, err)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(ciphertext)
	}))
	defer ts.Close()

	client := newAPIClient(ts.URL, "test-token")
	tmpDir := t.TempDir()
	media := &cdnMedia{FullURL: ts.URL + "/enc.jpg"}
	path, err := client.downloadMedia(context.Background(), media, hex.EncodeToString(key), tmpDir, "dec.jpg")
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, plaintext, data)
}

func TestAPIDownloadMediaNoURL(t *testing.T) {
	client := newAPIClient("http://unused", "tok")
	_, err := client.downloadMedia(context.Background(), nil, "", "/tmp", "f.jpg")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no media URL")

	_, err = client.downloadMedia(context.Background(), &cdnMedia{}, "", "/tmp", "f.jpg")
	assert.Error(t, err)
}

func TestAPIDownloadMediaHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	client := newAPIClient(ts.URL, "tok")
	_, err := client.downloadMedia(context.Background(), &cdnMedia{FullURL: ts.URL + "/missing"}, "", t.TempDir(), "f.jpg")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestAPIDownloadMediaBadAESKey(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(make([]byte, 32))
	}))
	defer ts.Close()

	client := newAPIClient(ts.URL, "tok")
	media := &cdnMedia{FullURL: ts.URL + "/img"}
	_, err := client.downloadMedia(context.Background(), media, "not-hex!", t.TempDir(), "test.jpg")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "decode aes key hex")
}

func TestAPIDownloadMediaDecryptionFallback(t *testing.T) {
	key := []byte("0123456789abcdef")
	// Craft ciphertext that decrypts but has bad PKCS7.
	// 15 bytes with pad=1 is valid, so use that to test the success path.
	ciphertext, _ := aesECBEncrypt([]byte("hello world 123"), key) // 15 bytes → valid

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(ciphertext)
	}))
	defer ts.Close()

	client := newAPIClient(ts.URL, "tok")
	media := &cdnMedia{FullURL: ts.URL + "/img"}
	path, err := client.downloadMedia(context.Background(), media, hex.EncodeToString(key), t.TempDir(), "test.jpg")
	require.NoError(t, err)
	assert.FileExists(t, path)
}

func TestAPIUploadMedia(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.jpg")
	require.NoError(t, os.WriteFile(filePath, []byte("fake-image-data"), 0o644))

	uploadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer uploadServer.Close()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "getuploadurl") {
			json.NewEncoder(w).Encode(getUploadURLResp{
				Ret:           0,
				UploadParam:   "enc-query-param-123",
				UploadFullURL: uploadServer.URL + "/upload",
			})
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer apiServer.Close()

	client := newAPIClient(apiServer.URL, "test-token")
	uploadParam, aesKeyHex, err := client.uploadMedia(context.Background(), filePath, "user@im.wechat", uploadMediaTypeImage)
	require.NoError(t, err)
	assert.Equal(t, "enc-query-param-123", uploadParam)
	assert.Equal(t, 32, len(aesKeyHex)) // 16 bytes hex-encoded
}

func TestAPIUploadMediaFileNotFound(t *testing.T) {
	client := newAPIClient("http://unused", "tok")
	_, _, err := client.uploadMedia(context.Background(), "/nonexistent/file.jpg", "user", uploadMediaTypeImage)
	assert.Error(t, err)
}

func TestAPIUploadMediaEncryptionPipeline(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "secret.txt")
	plaintext := []byte("top-secret-content-that-needs-encryption")
	require.NoError(t, os.WriteFile(filePath, plaintext, 0o644))

	var uploadedData []byte
	uploadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploadedData, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer uploadServer.Close()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(getUploadURLResp{
			Ret:           0,
			UploadParam:   "result-param",
			UploadFullURL: uploadServer.URL + "/upload",
		})
	}))
	defer apiServer.Close()

	client := newAPIClient(apiServer.URL, "tok")
	param, keyHex, err := client.uploadMedia(context.Background(), filePath, "user", uploadMediaTypeFile)
	require.NoError(t, err)
	assert.Equal(t, "result-param", param)
	assert.Equal(t, 32, len(keyHex))

	// Verify uploaded data is encrypted (not plaintext).
	assert.NotEqual(t, plaintext, uploadedData)
	assert.Equal(t, 0, len(uploadedData)%16)

	// Verify we can decrypt back.
	keyBytes, _ := hex.DecodeString(keyHex)
	decrypted, err := aesECBDecrypt(uploadedData, keyBytes)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestAPIDoHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("bad gateway"))
	}))
	defer ts.Close()

	client := newAPIClient(ts.URL, "tok")
	var resp notifyResp
	err := client.do(context.Background(), "some/path", &notifyReq{}, &resp, 0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "502")
}

func TestAPIDoInvalidJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json"))
	}))
	defer ts.Close()

	client := newAPIClient(ts.URL, "tok")
	var resp notifyResp
	err := client.do(context.Background(), "some/path", &notifyReq{}, &resp, 0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

func TestAPIDoContextCancelled(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Second)
	}))
	defer ts.Close()

	client := newAPIClient(ts.URL, "tok")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	err := client.do(ctx, "some/path", &notifyReq{}, nil, time.Second)
	assert.Error(t, err)
}

func TestAPIDoNilResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{}"))
	}))
	defer ts.Close()

	client := newAPIClient(ts.URL, "tok")
	err := client.do(context.Background(), "path", &notifyReq{}, nil, 0)
	assert.NoError(t, err)
}
