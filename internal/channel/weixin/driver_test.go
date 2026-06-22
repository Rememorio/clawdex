package weixin

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Rememorio/clawdex/internal/channel"
	"github.com/Rememorio/clawdex/internal/pairing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── Helper tests ──

func TestHashStringID(t *testing.T) {
	id1 := hashStringID("user1@im.wechat")
	id2 := hashStringID("user2@im.wechat")
	assert.NotEqual(t, id1, id2)
	// Same input → same output.
	assert.Equal(t, id1, hashStringID("user1@im.wechat"))
}

func TestSplitTextChunks(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		limit  int
		chunks int
	}{
		{"empty", "", 100, 1},
		{"short", "hello", 100, 1},
		{"exact", "abcde", 5, 1},
		{"split", "abcdef", 5, 2},
		{"multi", "1234567890", 3, 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := splitTextChunks(tc.text, tc.limit)
			assert.Equal(t, tc.chunks, len(result))
			assert.Equal(t, tc.text, strings.Join(result, ""))
		})
	}
}

func TestSplitTextChunksUnicode(t *testing.T) {
	text := "你好世界测试" // 6 runes
	chunks := splitTextChunks(text, 4)
	assert.Equal(t, 2, len(chunks))
	assert.Equal(t, "你好世界", chunks[0])
	assert.Equal(t, "测试", chunks[1])
}

func TestDetectMediaType(t *testing.T) {
	tests := []struct {
		path     string
		expected int
	}{
		{"/tmp/photo.jpg", uploadMediaTypeImage},
		{"/tmp/photo.PNG", uploadMediaTypeImage},
		{"/tmp/video.mp4", uploadMediaTypeVideo},
		{"/tmp/voice.amr", uploadMediaTypeVoice},
		{"/tmp/doc.pdf", uploadMediaTypeFile},
		{"/tmp/unknown", uploadMediaTypeFile},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.expected, detectMediaType(tc.path), tc.path)
	}
}

func TestGenerateClientID(t *testing.T) {
	id1 := generateClientID()
	time.Sleep(time.Millisecond)
	id2 := generateClientID()
	assert.NotEqual(t, id1, id2)
	assert.Contains(t, id1, "clawdex_")
}

// ── Access control tests ──

func TestCheckAccess(t *testing.T) {
	ps := pairing.NewStore(5 * time.Minute)

	t.Run("open policy allows all", func(t *testing.T) {
		d := New(Config{DMPolicy: "open", Token: "tok"}, ps)
		assert.Equal(t, accessAllowed, d.checkAccess("anyone@im.wechat"))
	})

	t.Run("allowlist denies unknown", func(t *testing.T) {
		d := New(Config{
			DMPolicy:  "allowlist",
			Token:     "tok",
			AllowFrom: []string{"allowed@im.wechat"},
		}, ps)
		assert.Equal(t, accessAllowed, d.checkAccess("allowed@im.wechat"))
		assert.Equal(t, accessDenied, d.checkAccess("unknown@im.wechat"))
	})

	t.Run("pairing policy for unknown users", func(t *testing.T) {
		d := New(Config{
			DMPolicy:  "pairing",
			Token:     "tok",
			AllowFrom: []string{"known@im.wechat"},
		}, ps)
		assert.Equal(t, accessAllowed, d.checkAccess("known@im.wechat"))
		assert.Equal(t, accessPairing, d.checkAccess("stranger@im.wechat"))
	})
}

func TestAddAllowedUser(t *testing.T) {
	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{DMPolicy: "pairing", Token: "tok"}, ps)

	assert.Equal(t, accessPairing, d.checkAccess("new@im.wechat"))
	d.AddAllowedUser("new@im.wechat")
	assert.Equal(t, accessAllowed, d.checkAccess("new@im.wechat"))
}

// ── Context token tests ──

func TestContextTokenCache(t *testing.T) {
	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{DMPolicy: "open", Token: "tok"}, ps)

	assert.Equal(t, "", d.getContextToken("user@im.wechat"))

	d.mu.Lock()
	d.contextTokens["user@im.wechat"] = "token123"
	d.mu.Unlock()

	assert.Equal(t, "token123", d.getContextToken("user@im.wechat"))
}

// ── Driver construction tests ──

func TestDriverName(t *testing.T) {
	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{Name: "my-wx", Token: "tok"}, ps)
	assert.Equal(t, "my-wx", d.Name())

	d2 := New(Config{Token: "tok"}, ps)
	assert.Equal(t, "weixin", d2.Name())
}

func TestDriverDefaults(t *testing.T) {
	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{Token: "tok"}, ps)
	assert.Equal(t, defaultTextChunkLimit, d.cfg.TextChunkLimit)
	assert.Equal(t, "pairing", d.cfg.DMPolicy)
	assert.Equal(t, defaultBaseURL, d.cfg.BaseURL)
}

// ── mockHandler ──

type mockHandler struct {
	mu       sync.Mutex
	messages []channel.Message
}

func (h *mockHandler) Handle(_ context.Context, msg channel.Message, _ channel.Responder) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages = append(h.messages, msg)
}

func (h *mockHandler) getMessages() []channel.Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]channel.Message{}, h.messages...)
}

// ── processInbound tests ──

func TestDriverProcessInbound(t *testing.T) {
	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{DMPolicy: "open", Token: "tok"}, ps)

	handler := &mockHandler{}
	msg := &weixinMessage{
		MessageID:    42,
		FromUserID:   "sender@im.wechat",
		MessageType:  messageTypeUser,
		ContextToken: "ctx-token-abc",
		ItemList: []messageItem{
			{Type: itemTypeText, TextItem: &textItem{Text: "hello world"}},
		},
	}

	d.processInbound(context.Background(), msg, handler)

	msgs := handler.getMessages()
	require.Len(t, msgs, 1)
	assert.Equal(t, "hello world", msgs[0].Text)
	assert.Equal(t, d.name, msgs[0].Channel)
	assert.Equal(t, hashStringID("sender@im.wechat"), msgs[0].ChatID)
	assert.Equal(t, int64(42), msgs[0].MessageID)

	// Verify context token was cached.
	assert.Equal(t, "ctx-token-abc", d.getContextToken("sender@im.wechat"))
}

func TestDriverSkipsBotMessages(t *testing.T) {
	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{DMPolicy: "open", Token: "tok"}, ps)
	handler := &mockHandler{}

	msg := &weixinMessage{
		FromUserID:  "bot@im.bot",
		MessageType: messageTypeBot,
		ItemList:    []messageItem{{Type: itemTypeText, TextItem: &textItem{Text: "echo"}}},
	}
	d.processInbound(context.Background(), msg, handler)
	assert.Empty(t, handler.getMessages())
}

func TestDriverSkipsEmptyMessages(t *testing.T) {
	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{DMPolicy: "open", Token: "tok"}, ps)
	handler := &mockHandler{}

	msg := &weixinMessage{
		FromUserID:  "user@im.wechat",
		MessageType: messageTypeUser,
		ItemList:    []messageItem{}, // no content
	}
	d.processInbound(context.Background(), msg, handler)
	assert.Empty(t, handler.getMessages())
}

func TestDriverVoiceTranscription(t *testing.T) {
	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{DMPolicy: "open", Token: "tok"}, ps)
	handler := &mockHandler{}

	msg := &weixinMessage{
		MessageID:   1,
		FromUserID:  "user@im.wechat",
		MessageType: messageTypeUser,
		ItemList: []messageItem{
			{Type: itemTypeVoice, VoiceItem: &voiceItem{Text: "transcribed text"}},
		},
	}
	d.processInbound(context.Background(), msg, handler)

	msgs := handler.getMessages()
	require.Len(t, msgs, 1)
	assert.Equal(t, "transcribed text", msgs[0].Text)
}

func TestProcessInboundNoFromUserID(t *testing.T) {
	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{DMPolicy: "open", Token: "tok"}, ps)
	handler := &mockHandler{}

	msg := &weixinMessage{
		FromUserID:  "",
		MessageType: messageTypeUser,
		ItemList:    []messageItem{{Type: itemTypeText, TextItem: &textItem{Text: "hi"}}},
	}
	d.processInbound(context.Background(), msg, handler)
	assert.Empty(t, handler.getMessages())
}

func TestProcessInboundDeniedUser(t *testing.T) {
	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{DMPolicy: "allowlist", Token: "tok", AllowFrom: []string{"friend@im.wechat"}}, ps)
	handler := &mockHandler{}

	msg := &weixinMessage{
		MessageID:   1,
		FromUserID:  "stranger@im.wechat",
		MessageType: messageTypeUser,
		ItemList:    []messageItem{{Type: itemTypeText, TextItem: &textItem{Text: "hi"}}},
	}
	d.processInbound(context.Background(), msg, handler)
	assert.Empty(t, handler.getMessages())
}

// ── extractContent tests ──

func TestExtractContentImage(t *testing.T) {
	imageData := []byte("raw-image-bytes")
	imgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(imageData)
	}))
	defer imgServer.Close()

	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{BaseURL: imgServer.URL, Token: "tok", DMPolicy: "open"}, ps)

	msg := &weixinMessage{
		FromUserID:  "user@im.wechat",
		MessageType: messageTypeUser,
		ItemList: []messageItem{
			{Type: itemTypeImage, ImageItem: &imageItem{
				Media: &cdnMedia{FullURL: imgServer.URL + "/img.jpg"},
			}},
		},
	}

	text, mediaPaths, cleanupPaths := d.extractContent(context.Background(), msg)
	assert.Equal(t, "", text)
	require.Len(t, mediaPaths, 1)
	require.Len(t, cleanupPaths, 1)

	data, err := os.ReadFile(mediaPaths[0])
	require.NoError(t, err)
	assert.Equal(t, imageData, data)
	os.RemoveAll(filepath.Dir(mediaPaths[0]))
}

func TestExtractContentImageDownloadFail(t *testing.T) {
	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{BaseURL: "http://localhost:1", Token: "tok", DMPolicy: "open"}, ps)

	msg := &weixinMessage{
		FromUserID:  "user@im.wechat",
		MessageType: messageTypeUser,
		ItemList: []messageItem{
			{Type: itemTypeImage, ImageItem: &imageItem{
				Media: &cdnMedia{FullURL: "http://localhost:1/broken"},
			}},
		},
	}

	text, mediaPaths, _ := d.extractContent(context.Background(), msg)
	assert.Equal(t, "", text)
	assert.Empty(t, mediaPaths)
}

func TestExtractContentMultipleItems(t *testing.T) {
	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{Token: "tok", DMPolicy: "open"}, ps)

	msg := &weixinMessage{
		FromUserID:  "user@im.wechat",
		MessageType: messageTypeUser,
		ItemList: []messageItem{
			{Type: itemTypeText, TextItem: &textItem{Text: "line1"}},
			{Type: itemTypeText, TextItem: &textItem{Text: "line2"}},
			{Type: itemTypeVoice, VoiceItem: &voiceItem{Text: "voice text"}},
			{Type: itemTypeFile, FileItem: &fileItem{FileName: "doc.pdf"}},
			{Type: itemTypeVideo, VideoItem: &videoItem{VideoSize: 1024}},
		},
	}

	text, mediaPaths, _ := d.extractContent(context.Background(), msg)
	assert.Equal(t, "line1\nline2\nvoice text", text)
	assert.Empty(t, mediaPaths)
}

func TestExtractContentNilItems(t *testing.T) {
	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{Token: "tok", DMPolicy: "open"}, ps)

	msg := &weixinMessage{
		FromUserID:  "user@im.wechat",
		MessageType: messageTypeUser,
		ItemList: []messageItem{
			{Type: itemTypeText, TextItem: nil},
			{Type: itemTypeImage, ImageItem: nil},
			{Type: itemTypeVoice, VoiceItem: nil},
			{Type: itemTypeVoice, VoiceItem: &voiceItem{}},
		},
	}

	text, mediaPaths, _ := d.extractContent(context.Background(), msg)
	assert.Equal(t, "", text)
	assert.Empty(t, mediaPaths)
}

// ── downloadImage tests ──

func TestDownloadImageNoSource(t *testing.T) {
	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{Token: "tok", DMPolicy: "open"}, ps)

	_, err := d.downloadImage(context.Background(), &imageItem{}, "user")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no image source")
}

func TestDownloadImageWithEncryption(t *testing.T) {
	key := []byte("0123456789abcdef")
	plaintext := []byte("encrypted-image-content")
	ciphertext, err := aesECBEncrypt(plaintext, key)
	require.NoError(t, err)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(ciphertext)
	}))
	defer ts.Close()

	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{BaseURL: ts.URL, Token: "tok", DMPolicy: "open"}, ps)

	img := &imageItem{
		Media:  &cdnMedia{FullURL: ts.URL + "/img"},
		AESKey: hex.EncodeToString(key),
	}
	path, err := d.downloadImage(context.Background(), img, "user")
	require.NoError(t, err)
	defer os.RemoveAll(filepath.Dir(path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, plaintext, data)
}

// ── Pairing flow ──

func TestDriverPairingFlow(t *testing.T) {
	ps := pairing.NewStore(5 * time.Minute)

	var sentTexts []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req sendMessageReq
		json.NewDecoder(r.Body).Decode(&req)
		if req.Msg != nil && len(req.Msg.ItemList) > 0 && req.Msg.ItemList[0].TextItem != nil {
			sentTexts = append(sentTexts, req.Msg.ItemList[0].TextItem.Text)
		}
		json.NewEncoder(w).Encode(sendMessageResp{Ret: 0})
	}))
	defer ts.Close()

	d := New(Config{DMPolicy: "pairing", BaseURL: ts.URL, Token: "tok"}, ps)
	handler := &mockHandler{}

	msg := &weixinMessage{
		FromUserID:  "stranger@im.wechat",
		MessageType: messageTypeUser,
		ItemList:    []messageItem{{Type: itemTypeText, TextItem: &textItem{Text: "hi"}}},
	}
	d.processInbound(context.Background(), msg, handler)

	assert.Empty(t, handler.getMessages())
	require.Len(t, sentTexts, 1)
	assert.Contains(t, sentTexts[0], "Pairing required")
}

// ── sendText error path ──

func TestSendTextError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{BaseURL: ts.URL, Token: "tok", DMPolicy: "open"}, ps)

	err := d.sendText(context.Background(), "user@im.wechat", "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "http 500")
}

func TestSendTextRetriesWithoutStaleContextToken(t *testing.T) {
	var contextTokens []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req sendMessageReq
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		contextTokens = append(contextTokens, req.Msg.ContextToken)
		if req.Msg.ContextToken == "stale-token" {
			json.NewEncoder(w).Encode(sendMessageResp{Ret: sendMessageStaleContextRet})
			return
		}
		json.NewEncoder(w).Encode(sendMessageResp{Ret: 0})
	}))
	defer ts.Close()

	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{BaseURL: ts.URL, Token: "tok", DMPolicy: "open"}, ps)
	d.mu.Lock()
	d.contextTokens["user@im.wechat"] = "stale-token"
	d.mu.Unlock()

	err := d.SendText(context.Background(), channel.DeliveryTarget{Target: "user@im.wechat"}, "test")
	require.NoError(t, err)
	assert.Equal(t, []string{"stale-token", ""}, contextTokens)
	assert.Equal(t, "", d.getContextToken("user@im.wechat"))
}

func TestSendTextReturnsRetError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(sendMessageResp{Ret: -9, ErrMsg: "blocked"})
	}))
	defer ts.Close()

	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{BaseURL: ts.URL, Token: "tok", DMPolicy: "open"}, ps)

	err := d.SendText(context.Background(), channel.DeliveryTarget{Target: "user@im.wechat"}, "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "weixin proactive send chunk 1/1")
	assert.Contains(t, err.Error(), "sendmessage ret=-9: blocked")
}

// ── getTypingTicket ──

func TestGetTypingTicketSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(getConfigResp{Ret: 0, TypingTicket: "new-ticket"})
	}))
	defer ts.Close()

	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{BaseURL: ts.URL, Token: "tok", DMPolicy: "open"}, ps)

	ticket := d.getTypingTicket(context.Background(), "user@im.wechat")
	assert.Equal(t, "new-ticket", ticket)

	// Second call uses cache.
	ticket2 := d.getTypingTicket(context.Background(), "user@im.wechat")
	assert.Equal(t, "new-ticket", ticket2)
}

func TestGetTypingTicketError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{BaseURL: ts.URL, Token: "tok", DMPolicy: "open"}, ps)

	ticket := d.getTypingTicket(context.Background(), "user@im.wechat")
	assert.Equal(t, "", ticket)
}

// ── Responder tests ──

func TestResponderReply(t *testing.T) {
	var sentMessages []sendMessageReq
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req sendMessageReq
		json.NewDecoder(r.Body).Decode(&req)
		sentMessages = append(sentMessages, req)
		json.NewEncoder(w).Encode(sendMessageResp{Ret: 0})
	}))
	defer ts.Close()

	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{
		BaseURL:        ts.URL,
		Token:          "tok",
		DMPolicy:       "open",
		TextChunkLimit: 10,
	}, ps)

	d.mu.Lock()
	d.contextTokens["user@im.wechat"] = "my-ctx"
	d.mu.Unlock()

	r := &weixinResponder{driver: d, userID: "user@im.wechat"}
	err := r.Reply(context.Background(), channel.Message{}, "short msg")
	require.NoError(t, err)

	require.Len(t, sentMessages, 1)
	assert.Equal(t, "user@im.wechat", sentMessages[0].Msg.ToUserID)
	assert.Equal(t, "my-ctx", sentMessages[0].Msg.ContextToken)
	assert.Equal(t, "short msg", sentMessages[0].Msg.ItemList[0].TextItem.Text)
}

func TestResponderReplyChunked(t *testing.T) {
	var sentCount int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sentCount++
		json.NewEncoder(w).Encode(sendMessageResp{Ret: 0})
	}))
	defer ts.Close()

	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{BaseURL: ts.URL, Token: "tok", DMPolicy: "open", TextChunkLimit: 5}, ps)

	r := &weixinResponder{driver: d, userID: "user@im.wechat"}
	err := r.Reply(context.Background(), channel.Message{}, "1234567890")
	require.NoError(t, err)
	assert.Equal(t, 2, sentCount)
}

func TestResponderReplyReturnsSendError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(sendMessageResp{Ret: -9, ErrMsg: "blocked"})
	}))
	defer ts.Close()

	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{BaseURL: ts.URL, Token: "tok", DMPolicy: "open"}, ps)
	r := &weixinResponder{driver: d, userID: "user@im.wechat"}

	err := r.Reply(context.Background(), channel.Message{}, "hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "weixin reply chunk 1/1")
	assert.Contains(t, err.Error(), "sendmessage ret=-9: blocked")
}

func TestResponderTypingWithTicket(t *testing.T) {
	var typingCalled bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ilink/bot/sendtyping" {
			typingCalled = true
		}
		// getConfig returns a ticket; sendTyping succeeds.
		if r.URL.Path == "/ilink/bot/getconfig" {
			json.NewEncoder(w).Encode(getConfigResp{Ret: 0, TypingTicket: "ticket"})
			return
		}
		json.NewEncoder(w).Encode(sendTypingResp{Ret: 0})
	}))
	defer ts.Close()

	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{BaseURL: ts.URL, Token: "tok", DMPolicy: "open"}, ps)

	r := &weixinResponder{driver: d, userID: "user@im.wechat"}
	err := r.Typing(context.Background(), channel.Message{})
	require.NoError(t, err)
	assert.True(t, typingCalled)
}

func TestResponderTypingNoTicket(t *testing.T) {
	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{Token: "tok", DMPolicy: "open"}, ps)
	r := &weixinResponder{driver: d, userID: "user@im.wechat"}
	err := r.Typing(context.Background(), channel.Message{})
	assert.NoError(t, err)
}

func TestResponderReplyEmpty(t *testing.T) {
	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{Token: "tok", DMPolicy: "open"}, ps)
	r := &weixinResponder{driver: d, userID: "user@im.wechat"}
	err := r.Reply(context.Background(), channel.Message{}, "")
	assert.NoError(t, err)
}

func TestResponderReplyWithMedia(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "photo.png")
	require.NoError(t, os.WriteFile(filePath, []byte("png-data"), 0o644))

	var sentRequests []sendMessageReq
	uploadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer uploadServer.Close()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "getuploadurl"):
			json.NewEncoder(w).Encode(getUploadURLResp{
				Ret: 0, UploadParam: "upload-param-img", UploadFullURL: uploadServer.URL + "/upload",
			})
		case strings.Contains(r.URL.Path, "sendmessage"):
			var req sendMessageReq
			json.NewDecoder(r.Body).Decode(&req)
			sentRequests = append(sentRequests, req)
			json.NewEncoder(w).Encode(sendMessageResp{Ret: 0})
		default:
			json.NewEncoder(w).Encode(notifyResp{Ret: 0})
		}
	}))
	defer apiServer.Close()

	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{BaseURL: apiServer.URL, Token: "tok", DMPolicy: "open"}, ps)

	r := &weixinResponder{driver: d, userID: "user@im.wechat"}
	err := r.ReplyWithMedia(context.Background(), channel.Message{}, "look at this", []string{filePath})
	require.NoError(t, err)

	require.Len(t, sentRequests, 2)
	assert.Equal(t, itemTypeText, sentRequests[0].Msg.ItemList[0].Type)
	assert.Equal(t, "look at this", sentRequests[0].Msg.ItemList[0].TextItem.Text)
	assert.Equal(t, itemTypeImage, sentRequests[1].Msg.ItemList[0].Type)
	assert.Equal(t, "upload-param-img", sentRequests[1].Msg.ItemList[0].ImageItem.Media.EncryptQueryParam)
}

func TestResponderReplyWithMediaNoCaption(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "doc.pdf")
	require.NoError(t, os.WriteFile(filePath, []byte("pdf-data"), 0o644))

	uploadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer uploadServer.Close()

	var sentCount int
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "getuploadurl"):
			json.NewEncoder(w).Encode(getUploadURLResp{
				Ret: 0, UploadParam: "p", UploadFullURL: uploadServer.URL + "/upload",
			})
		case strings.Contains(r.URL.Path, "sendmessage"):
			sentCount++
			json.NewEncoder(w).Encode(sendMessageResp{Ret: 0})
		default:
			json.NewEncoder(w).Encode(notifyResp{Ret: 0})
		}
	}))
	defer apiServer.Close()

	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{BaseURL: apiServer.URL, Token: "tok", DMPolicy: "open"}, ps)

	r := &weixinResponder{driver: d, userID: "user@im.wechat"}
	err := r.ReplyWithMedia(context.Background(), channel.Message{}, "", []string{filePath})
	require.NoError(t, err)
	assert.Equal(t, 1, sentCount)
}

func TestResponderReplyWithMediaReturnsSendError(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "photo.png")
	require.NoError(t, os.WriteFile(filePath, []byte("png-data"), 0o644))

	uploadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer uploadServer.Close()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "getuploadurl"):
			json.NewEncoder(w).Encode(getUploadURLResp{
				Ret: 0, UploadParam: "upload-param-img", UploadFullURL: uploadServer.URL + "/upload",
			})
		case strings.Contains(r.URL.Path, "sendmessage"):
			json.NewEncoder(w).Encode(sendMessageResp{Ret: -9, ErrMsg: "blocked"})
		default:
			json.NewEncoder(w).Encode(notifyResp{Ret: 0})
		}
	}))
	defer apiServer.Close()

	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{BaseURL: apiServer.URL, Token: "tok", DMPolicy: "open"}, ps)

	r := &weixinResponder{driver: d, userID: "user@im.wechat"}
	err := r.ReplyWithMedia(context.Background(), channel.Message{}, "", []string{filePath})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "weixin media send photo.png")
	assert.Contains(t, err.Error(), "sendmessage ret=-9: blocked")
}

func TestResponderReplyWithMediaUploadFails(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "bad.jpg")
	require.NoError(t, os.WriteFile(filePath, []byte("data"), 0o644))

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "getuploadurl") {
			json.NewEncoder(w).Encode(getUploadURLResp{Ret: -1, ErrMsg: "quota"})
		} else {
			json.NewEncoder(w).Encode(sendMessageResp{Ret: 0})
		}
	}))
	defer apiServer.Close()

	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{BaseURL: apiServer.URL, Token: "tok", DMPolicy: "open"}, ps)

	r := &weixinResponder{driver: d, userID: "user@im.wechat"}
	err := r.ReplyWithMedia(context.Background(), channel.Message{}, "", []string{filePath})
	assert.NoError(t, err)
}

func TestReplyWithMediaNonImageFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "report.csv")
	require.NoError(t, os.WriteFile(filePath, []byte("a,b,c"), 0o644))

	uploadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer uploadServer.Close()

	var sentMsgs []sendMessageReq
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "getuploadurl"):
			json.NewEncoder(w).Encode(getUploadURLResp{
				Ret: 0, UploadParam: "file-param", UploadFullURL: uploadServer.URL + "/up",
			})
		case strings.Contains(r.URL.Path, "sendmessage"):
			var req sendMessageReq
			json.NewDecoder(r.Body).Decode(&req)
			sentMsgs = append(sentMsgs, req)
			json.NewEncoder(w).Encode(sendMessageResp{Ret: 0})
		default:
			json.NewEncoder(w).Encode(notifyResp{Ret: 0})
		}
	}))
	defer apiServer.Close()

	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{BaseURL: apiServer.URL, Token: "tok", DMPolicy: "open"}, ps)

	r := &weixinResponder{driver: d, userID: "user@im.wechat"}
	err := r.ReplyWithMedia(context.Background(), channel.Message{}, "", []string{filePath})
	require.NoError(t, err)

	require.Len(t, sentMsgs, 1)
	item := sentMsgs[0].Msg.ItemList[0]
	assert.Equal(t, itemTypeFile, item.Type)
	assert.Equal(t, "report.csv", item.FileItem.FileName)
}

// ── Start/Stop integration tests ──

func TestDriverStartCancellation(t *testing.T) {
	var callCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := callCount.Add(1)
		switch {
		case n <= 2:
			json.NewEncoder(w).Encode(notifyResp{Ret: 0})
		default:
			json.NewEncoder(w).Encode(getUpdatesResp{Ret: 0, GetUpdatesBuf: "buf"})
		}
	}))
	defer ts.Close()

	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{BaseURL: ts.URL, Token: "tok", DMPolicy: "open"}, ps)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := d.Start(ctx, &mockHandler{})
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestDriverStartSessionExpired(t *testing.T) {
	var callCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := callCount.Add(1)
		switch {
		case n <= 2:
			json.NewEncoder(w).Encode(notifyResp{Ret: 0})
		default:
			json.NewEncoder(w).Encode(getUpdatesResp{
				Ret: -1, ErrCode: sessionExpiredErrCode, ErrMsg: "session expired",
			})
		}
	}))
	defer ts.Close()

	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{BaseURL: ts.URL, Token: "tok", DMPolicy: "open"}, ps)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := d.Start(ctx, &mockHandler{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session expired")
}

func TestDriverStartRetryOnError(t *testing.T) {
	var callCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := callCount.Add(1)
		switch {
		case n <= 2:
			json.NewEncoder(w).Encode(notifyResp{Ret: 0})
		case n == 3:
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("server error"))
		default:
			json.NewEncoder(w).Encode(getUpdatesResp{Ret: 0, GetUpdatesBuf: "new-buf"})
		}
	}))
	defer ts.Close()

	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{BaseURL: ts.URL, Token: "tok", DMPolicy: "open"}, ps)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := d.Start(ctx, &mockHandler{})
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Greater(t, int(callCount.Load()), 3)
}

func TestDriverStartDispatchesMessages(t *testing.T) {
	var callCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := callCount.Add(1)
		switch {
		case n <= 2:
			json.NewEncoder(w).Encode(notifyResp{Ret: 0})
		case n == 3:
			json.NewEncoder(w).Encode(getUpdatesResp{
				Ret: 0, GetUpdatesBuf: "buf2",
				Msgs: []weixinMessage{{
					MessageID: 100, FromUserID: "test@im.wechat",
					MessageType: messageTypeUser, ContextToken: "tok1",
					ItemList: []messageItem{{Type: itemTypeText, TextItem: &textItem{Text: "ping"}}},
				}},
			})
		default:
			json.NewEncoder(w).Encode(getUpdatesResp{Ret: 0, GetUpdatesBuf: "buf2"})
		}
	}))
	defer ts.Close()

	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{BaseURL: ts.URL, Token: "tok", DMPolicy: "open"}, ps)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	handler := &mockHandler{}
	_ = d.Start(ctx, handler)

	msgs := handler.getMessages()
	require.NotEmpty(t, msgs)
	assert.Equal(t, "ping", msgs[0].Text)
	assert.Equal(t, int64(100), msgs[0].MessageID)
}

func TestDriverStartUpdatesPollingTimeout(t *testing.T) {
	var callCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := callCount.Add(1)
		switch {
		case n <= 2:
			json.NewEncoder(w).Encode(getConfigResp{Ret: 0, TypingTicket: "t"})
		default:
			json.NewEncoder(w).Encode(getUpdatesResp{
				Ret: 0, GetUpdatesBuf: "updated-buf", LongPollingTimeoutMs: 5000,
			})
		}
	}))
	defer ts.Close()

	ps := pairing.NewStore(5 * time.Minute)
	d := New(Config{BaseURL: ts.URL, Token: "tok", DMPolicy: "open"}, ps)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_ = d.Start(ctx, &mockHandler{})
	assert.Greater(t, int(callCount.Load()), 2)
}
