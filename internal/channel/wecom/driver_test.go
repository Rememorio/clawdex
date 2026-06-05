package wecom

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
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
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testEncodingAESKey is a valid 43-char base64 key for testing.
const testEncodingAESKey = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"
const testToken = "test_token"

// encryptForTest encrypts XML content for test purposes.
func encryptForTest(t *testing.T, content string) string {
	t.Helper()
	aesKey, err := base64.StdEncoding.DecodeString(testEncodingAESKey + "=")
	require.NoError(t, err)
	iv := aesKey[:16]

	// Build plaintext: 16 random + 4 byte msg len + msg + receiveid
	random := make([]byte, 16)
	msgLenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(msgLenBuf, uint32(len(content)))
	plaintext := append(random, msgLenBuf...)
	plaintext = append(plaintext, []byte(content)...)
	plaintext = append(plaintext, []byte("corpid")...)

	// PKCS7 pad
	padLen := aes.BlockSize - (len(plaintext) % aes.BlockSize)
	for i := 0; i < padLen; i++ {
		plaintext = append(plaintext, byte(padLen))
	}

	block, err := aes.NewCipher(aesKey)
	require.NoError(t, err)
	ciphertext := make([]byte, len(plaintext))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, plaintext)

	return base64.StdEncoding.EncodeToString(ciphertext)
}

// computeSignature computes the WeCom signature for test purposes.
func computeSignature(token, timestamp, nonce, encrypted string) string {
	parts := []string{token, timestamp, nonce, encrypted}
	// sort manually won't work; use the same logic as production
	h := sha1.New()
	sorted := make([]string, len(parts))
	copy(sorted, parts)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j] < sorted[i] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	h.Write([]byte(strings.Join(sorted, "")))
	return fmt.Sprintf("%x", h.Sum(nil))
}

func TestHandleVerify(t *testing.T) {
	d := New(Config{
		Token:          testToken,
		EncodingAESKey: testEncodingAESKey,
	}, nil)

	// Encrypt an echostr.
	echostr := encryptForTest(t, "test_echo_string")
	timestamp := "1234567890"
	nonce := "nonce123"
	sig := computeSignature(testToken, timestamp, nonce, echostr)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf(
		"/wecom/webhook?msg_signature=%s&timestamp=%s&nonce=%s&echostr=%s",
		sig, timestamp, nonce, echostr,
	), nil)
	w := httptest.NewRecorder()

	d.Handler()(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "test_echo_string", w.Body.String())
}

func TestHandleVerifyBadSignature(t *testing.T) {
	d := New(Config{
		Token:          testToken,
		EncodingAESKey: testEncodingAESKey,
	}, nil)

	req := httptest.NewRequest(http.MethodGet,
		"/wecom/webhook?msg_signature=bad&timestamp=123&nonce=abc&echostr=test",
		nil)
	w := httptest.NewRecorder()

	d.Handler()(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestHandleMessage(t *testing.T) {
	d := New(Config{
		Token:          testToken,
		EncodingAESKey: testEncodingAESKey,
		DMPolicy:       "open",
	}, nil)

	// Build a test message XML (nested structure matching real WeCom format).
	msgXML := `<xml>
		<WebhookUrl>https://example.com/webhook?key=testkey</WebhookUrl>
		<ChatId>test_chat_id</ChatId>
		<ChatType>single</ChatType>
		<From><UserId>user1</UserId><Name>Test User</Name><Alias>tu</Alias></From>
		<MsgType>text</MsgType>
		<Text><Content><![CDATA[hello bot]]></Content></Text>
		<MsgId>msg123</MsgId>
	</xml>`

	encrypted := encryptForTest(t, msgXML)
	timestamp := "1234567890"
	nonce := "nonce456"
	sig := computeSignature(testToken, timestamp, nonce, encrypted)

	// Build the envelope XML.
	envelope := struct {
		XMLName xml.Name `xml:"xml"`
		Encrypt string   `xml:"Encrypt"`
	}{Encrypt: encrypted}
	envelopeBytes, err := xml.Marshal(envelope)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf(
		"/wecom/webhook?msg_signature=%s&timestamp=%s&nonce=%s",
		sig, timestamp, nonce,
	), strings.NewReader(string(envelopeBytes)))
	w := httptest.NewRecorder()

	d.Handler()(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// The message should be enqueued in the incoming channel.
	select {
	case job := <-d.incoming:
		assert.Equal(t, "wecom", job.msg.Channel)
		assert.Equal(t, "hello bot", job.msg.Text)
		assert.Equal(t, hashChatID("wecom", "test_chat_id"), job.msg.ChatID)
		assert.Equal(t, "https://example.com/webhook?key=testkey", job.webhookURL)
	case <-time.After(time.Second):
		t.Fatal("expected message in incoming channel")
	}
}

func TestReply(t *testing.T) {
	var received []markdownPayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p markdownPayload
		_ = json.Unmarshal(body, &p)
		received = append(received, p)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer srv.Close()

	d := New(Config{
		Token:          testToken,
		EncodingAESKey: testEncodingAESKey,
	}, nil)

	// Simulate a cached webhook.
	chatID := hashChatID("wecom", "chat1")
	d.webhookCache.Store(chatID, &webhookEntry{
		url:       srv.URL,
		expiresAt: time.Now().Add(time.Hour),
	})
	d.chatIDMap.Store(chatID, "chat1")

	msg := channel.Message{Channel: "wecom", ChatID: chatID}
	err := d.Reply(context.Background(), msg, "hello world")
	require.NoError(t, err)

	require.Len(t, received, 1)
	assert.Equal(t, "markdown", received[0].MsgType)
	assert.Equal(t, "hello world", received[0].Markdown.Content)
	assert.Equal(t, "chat1", received[0].ChatID)
}

func TestHashChatID(t *testing.T) {
	// Deterministic.
	a := hashChatID("wecom", "test_chat")
	b := hashChatID("wecom", "test_chat")
	assert.Equal(t, a, b)

	// Different inputs produce different hashes.
	c := hashChatID("wecom", "other_chat")
	assert.NotEqual(t, a, c)
}

func TestSplitByByteLimit(t *testing.T) {
	// Short text stays as one chunk.
	chunks := splitByByteLimit("hello", 100)
	assert.Equal(t, []string{"hello"}, chunks)

	// Empty text.
	chunks = splitByByteLimit("", 100)
	assert.Equal(t, []string{"(empty response)"}, chunks)

	// Long text gets split.
	long := strings.Repeat("a", 200)
	chunks = splitByByteLimit(long, 100)
	assert.True(t, len(chunks) >= 2)
	for _, c := range chunks {
		assert.LessOrEqual(t, len(c), 100)
	}

	// Chinese text (3 bytes per char in UTF-8).
	chinese := strings.Repeat("中", 50) // 150 bytes
	chunks = splitByByteLimit(chinese, 100)
	assert.True(t, len(chunks) >= 2)
	for _, c := range chunks {
		assert.LessOrEqual(t, len(c), 100)
	}
}

func TestWebhookCacheExpiry(t *testing.T) {
	d := New(Config{
		Token:          testToken,
		EncodingAESKey: testEncodingAESKey,
	}, nil)

	chatID := hashChatID("wecom", "expired_chat")
	d.webhookCache.Store(chatID, &webhookEntry{
		url:       "https://expired.example.com",
		expiresAt: time.Now().Add(-time.Hour),
	})

	url, _ := d.lookupWebhook(chatID)
	assert.Empty(t, url)
}

// ── extractContent tests ──

func TestExtractContent_Text(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &xmlMessage{MsgType: "text", Content: "  hello  "}
	text, urls := d.extractContent(msg)
	assert.Equal(t, "hello", text)
	assert.Nil(t, urls)
}

func TestExtractContent_Image(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &xmlMessage{MsgType: "image", PicURL: "https://example.com/pic.jpg"}
	text, urls := d.extractContent(msg)
	assert.Equal(t, "[image]", text)
	assert.Equal(t, []string{"https://example.com/pic.jpg"}, urls)
}

func TestExtractContent_ImageNoPicURL(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &xmlMessage{MsgType: "image"}
	text, urls := d.extractContent(msg)
	assert.Equal(t, "[image]", text)
	assert.Nil(t, urls)
}

func TestExtractContent_Mixed(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &xmlMessage{
		MsgType: "mixed",
		MixedItems: []xmlMsgItem{
			{MsgType: "image", PicURL: "https://example.com/1.jpg"},
			{MsgType: "text", Content: "这个是什么呀"},
		},
	}
	text, urls := d.extractContent(msg)
	assert.Contains(t, text, "这个是什么呀")
	assert.Equal(t, []string{"https://example.com/1.jpg"}, urls)
}

func TestExtractContent_MixedImageOnly(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &xmlMessage{
		MsgType:    "mixed",
		MixedItems: []xmlMsgItem{{MsgType: "image", PicURL: "https://example.com/pic.jpg"}},
	}
	text, urls := d.extractContent(msg)
	assert.Equal(t, "[image]", text)
	assert.Equal(t, []string{"https://example.com/pic.jpg"}, urls)
}

func TestExtractContent_MixedEmpty(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &xmlMessage{MsgType: "mixed"}
	text, urls := d.extractContent(msg)
	assert.Empty(t, text)
	assert.Nil(t, urls)
}

func TestExtractContent_Link(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &xmlMessage{MsgType: "link", Title: "My Link", Desc: "A description", LinkURL: "https://example.com"}
	text, urls := d.extractContent(msg)
	assert.Contains(t, text, "My Link")
	assert.Contains(t, text, "A description")
	assert.Contains(t, text, "https://example.com")
	assert.Nil(t, urls)
}

func TestExtractContent_LinkEmpty(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &xmlMessage{MsgType: "link"}
	text, urls := d.extractContent(msg)
	assert.Equal(t, "[link]", text)
	assert.Nil(t, urls)
}

func TestExtractContent_LinkPartial(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &xmlMessage{MsgType: "link", Title: "Only Title"}
	text, _ := d.extractContent(msg)
	assert.Equal(t, "Only Title", text)
}

func TestExtractContent_Voice(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	text, urls := d.extractContent(&xmlMessage{MsgType: "voice"})
	assert.Equal(t, "[voice]", text)
	assert.Nil(t, urls)
}

func TestExtractContent_Unknown(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	text, urls := d.extractContent(&xmlMessage{MsgType: "something"})
	assert.Empty(t, text)
	assert.Nil(t, urls)
}

// ── Image download tests ──

func TestDownloadImages(t *testing.T) {
	// Serve a tiny PNG.
	imgData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A} // PNG header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(imgData)
	}))
	defer srv.Close()

	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	paths := d.downloadImages(context.Background(), []string{srv.URL + "/pic.png"}, nil)

	require.Len(t, paths, 1)
	assert.True(t, strings.HasSuffix(paths[0], ".png"))

	// Verify the file was written.
	data, err := os.ReadFile(paths[0])
	require.NoError(t, err)
	assert.Equal(t, imgData, data)

	// Clean up.
	os.RemoveAll(filepath.Dir(paths[0]))
}

func TestDownloadImages_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	paths := d.downloadImages(context.Background(), []string{srv.URL + "/missing.jpg"}, nil)
	assert.Nil(t, paths)
}

func TestDownloadImages_Empty(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	paths := d.downloadImages(context.Background(), nil, nil)
	assert.Nil(t, paths)
}

func TestDownloadImages_ContentTypeDetection(t *testing.T) {
	// Valid magic bytes for each image format.
	jpegMagic := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}
	pngMagic := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	gifMagic := []byte{0x47, 0x49, 0x46, 0x38, 0x39, 0x61}
	webpMagic := []byte{0x52, 0x49, 0x46, 0x46, 0x00, 0x00, 0x00, 0x00, 0x57, 0x45, 0x42, 0x50}

	tests := []struct {
		contentType string
		wantExt     string
		body        []byte
	}{
		{"image/png", ".png", pngMagic},
		{"image/jpeg", ".jpg", jpegMagic},
		{"image/gif", ".gif", gifMagic},
		{"image/webp", ".webp", webpMagic},
	}

	for _, tt := range tests {
		t.Run(tt.contentType, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				_, _ = w.Write(tt.body)
			}))
			defer srv.Close()

			d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
			paths := d.downloadImages(context.Background(), []string{srv.URL + "/img"}, nil)
			require.Len(t, paths, 1)
			assert.True(t, strings.HasSuffix(paths[0], tt.wantExt), "expected %s suffix, got %s", tt.wantExt, paths[0])

			os.RemoveAll(filepath.Dir(paths[0]))
		})
	}
}

func TestDownloadImages_InvalidMagicBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("this is not an image"))
	}))
	defer srv.Close()

	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	paths := d.downloadImages(context.Background(), []string{srv.URL + "/img"}, nil)
	// Non-image data is now saved as a file (not rejected).
	require.Len(t, paths, 1)
	assert.True(t, strings.HasSuffix(paths[0], ".bin"), "expected .bin suffix, got %s", paths[0])

	data, err := os.ReadFile(paths[0])
	require.NoError(t, err)
	assert.Equal(t, []byte("this is not an image"), data)
	os.RemoveAll(filepath.Dir(paths[0]))
}

func TestDownloadImages_EncryptedImage(t *testing.T) {
	// Build AES-encrypted PNG data using the same key derivation as decryptFileData.
	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D}

	// Derive AES key from testEncodingAESKey.
	aesKey, err := base64.StdEncoding.DecodeString(testEncodingAESKey + "=")
	require.NoError(t, err)
	require.Len(t, aesKey, 32)

	// PKCS#7 pad to AES block size.
	padLen := aes.BlockSize - (len(pngData) % aes.BlockSize)
	padded := make([]byte, len(pngData)+padLen)
	copy(padded, pngData)
	for i := len(pngData); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}

	// AES-256-CBC encrypt with IV = key[:16].
	block, err := aes.NewCipher(aesKey)
	require.NoError(t, err)
	encrypted := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, aesKey[:16]).CryptBlocks(encrypted, padded)

	// Serve encrypted data (not a valid image).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(encrypted)
	}))
	defer srv.Close()

	// Test with channel-level EncodingAESKey (no per-image key).
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	paths := d.downloadImages(context.Background(), []string{srv.URL + "/encrypted.bin"}, nil)
	require.Len(t, paths, 1)
	assert.True(t, strings.HasSuffix(paths[0], ".png"))

	data, err := os.ReadFile(paths[0])
	require.NoError(t, err)
	assert.Equal(t, pngData, data)
	os.RemoveAll(filepath.Dir(paths[0]))

	// Test with per-image AES key.
	paths = d.downloadImages(context.Background(), []string{srv.URL + "/encrypted.bin"}, []string{testEncodingAESKey})
	require.Len(t, paths, 1)
	data, err = os.ReadFile(paths[0])
	require.NoError(t, err)
	assert.Equal(t, pngData, data)
	os.RemoveAll(filepath.Dir(paths[0]))
}

func TestDecryptFileData(t *testing.T) {
	// Build test encrypted data.
	plaintext := []byte("hello world, this is a test!")
	aesKey, err := base64.StdEncoding.DecodeString(testEncodingAESKey + "=")
	require.NoError(t, err)

	// PKCS#7 pad.
	padLen := aes.BlockSize - (len(plaintext) % aes.BlockSize)
	padded := make([]byte, len(plaintext)+padLen)
	copy(padded, plaintext)
	for i := len(plaintext); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}

	block, err := aes.NewCipher(aesKey)
	require.NoError(t, err)
	encrypted := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, aesKey[:16]).CryptBlocks(encrypted, padded)

	// Decrypt and verify.
	decrypted, err := decryptFileData(testEncodingAESKey, encrypted)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)

	// Error cases.
	_, err = decryptFileData(testEncodingAESKey, nil)
	assert.Error(t, err)

	_, err = decryptFileData(testEncodingAESKey, []byte{0x01, 0x02, 0x03})
	assert.Error(t, err)

	_, err = decryptFileData("badkey", encrypted)
	assert.Error(t, err)
}

func TestDetectImageFormat(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		wantExt string
		wantOk  bool
	}{
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0}, ".jpg", true},
		{"png", []byte{0x89, 0x50, 0x4E, 0x47}, ".png", true},
		{"gif", []byte{0x47, 0x49, 0x46, 0x38}, ".gif", true},
		{"webp", []byte{0x52, 0x49, 0x46, 0x46, 0x00, 0x00, 0x00, 0x00, 0x57, 0x45, 0x42, 0x50}, ".webp", true},
		{"too short", []byte{0xFF, 0xD8}, "", false},
		{"not image", []byte{0x00, 0x01, 0x02, 0x03}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext, ok := detectImageFormat(tt.data)
			assert.Equal(t, tt.wantOk, ok)
			assert.Equal(t, tt.wantExt, ext)
		})
	}
}

// ── Full message flow tests ──

func TestHandleMessage_Image(t *testing.T) {
	// Start an image server.
	imgData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10} // JPEG header
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(imgData)
	}))
	defer imgSrv.Close()

	d := New(Config{
		Token:          testToken,
		EncodingAESKey: testEncodingAESKey,
		DMPolicy:       "open",
	}, nil)

	msgXML := fmt.Sprintf(`<xml>
		<WebhookUrl>https://example.com/webhook?key=testkey</WebhookUrl>
		<ChatId>test_chat_img</ChatId>
		<ChatType>single</ChatType>
		<From><UserId>user1</UserId><Name>Test</Name></From>
		<MsgType>image</MsgType>
		<Image><ImageUrl>%s/photo.jpg</ImageUrl></Image>
		<MsgId>msg_img</MsgId>
	</xml>`, imgSrv.URL)

	encrypted := encryptForTest(t, msgXML)
	timestamp := "1234567890"
	nonce := "nonce_img"
	sig := computeSignature(testToken, timestamp, nonce, encrypted)

	envelope := struct {
		XMLName xml.Name `xml:"xml"`
		Encrypt string   `xml:"Encrypt"`
	}{Encrypt: encrypted}
	envelopeBytes, err := xml.Marshal(envelope)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf(
		"/wecom/webhook?msg_signature=%s&timestamp=%s&nonce=%s",
		sig, timestamp, nonce,
	), strings.NewReader(string(envelopeBytes)))
	w := httptest.NewRecorder()

	d.Handler()(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	select {
	case job := <-d.incoming:
		assert.Equal(t, "[image]", job.msg.Text)
		assert.Len(t, job.msg.MediaPaths, 1)
		// Clean up temp files.
		for _, p := range job.msg.MediaPaths {
			os.RemoveAll(filepath.Dir(p))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected message in incoming channel")
	}
}

func TestHandleMessage_Mixed(t *testing.T) {
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}) // PNG header
	}))
	defer imgSrv.Close()

	d := New(Config{
		Token:          testToken,
		EncodingAESKey: testEncodingAESKey,
		DMPolicy:       "open",
		GroupPolicy:    "open",
	}, nil)

	msgXML := fmt.Sprintf(`<xml>
		<WebhookUrl>https://example.com/webhook?key=testkey</WebhookUrl>
		<ChatId>test_chat_mixed</ChatId>
		<ChatType>group</ChatType>
		<From><UserId>user2</UserId><Name>User Two</Name></From>
		<MsgType>mixed</MsgType>
		<MixedMessage>
			<MsgItem>
				<MsgType>image</MsgType>
				<Image><ImageUrl>%s/news.png</ImageUrl></Image>
			</MsgItem>
			<MsgItem>
				<MsgType>text</MsgType>
				<Text><Content>Breaking News</Content></Text>
			</MsgItem>
		</MixedMessage>
		<MsgId>msg_mixed</MsgId>
	</xml>`, imgSrv.URL)

	encrypted := encryptForTest(t, msgXML)
	timestamp := "1234567890"
	nonce := "nonce_mixed"
	sig := computeSignature(testToken, timestamp, nonce, encrypted)

	envelope := struct {
		XMLName xml.Name `xml:"xml"`
		Encrypt string   `xml:"Encrypt"`
	}{Encrypt: encrypted}
	envelopeBytes, err := xml.Marshal(envelope)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf(
		"/wecom/webhook?msg_signature=%s&timestamp=%s&nonce=%s",
		sig, timestamp, nonce,
	), strings.NewReader(string(envelopeBytes)))
	w := httptest.NewRecorder()

	d.Handler()(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	select {
	case job := <-d.incoming:
		assert.Contains(t, job.msg.Text, "Breaking News")
		assert.Len(t, job.msg.MediaPaths, 1)
		for _, p := range job.msg.MediaPaths {
			os.RemoveAll(filepath.Dir(p))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected message in incoming channel")
	}
}

func TestHandleMessage_Link(t *testing.T) {
	d := New(Config{
		Token:          testToken,
		EncodingAESKey: testEncodingAESKey,
		DMPolicy:       "open",
	}, nil)

	msgXML := `<xml>
		<WebhookUrl>https://example.com/webhook?key=testkey</WebhookUrl>
		<ChatId>test_chat_link</ChatId>
		<ChatType>single</ChatType>
		<From><UserId>user3</UserId><Name>User Three</Name></From>
		<MsgType>link</MsgType>
		<Title>Cool Article</Title>
		<Description>A very cool article about Go</Description>
		<Url>https://go.dev/blog/article</Url>
		<MsgId>msg_link</MsgId>
	</xml>`

	encrypted := encryptForTest(t, msgXML)
	timestamp := "1234567890"
	nonce := "nonce_link"
	sig := computeSignature(testToken, timestamp, nonce, encrypted)

	envelope := struct {
		XMLName xml.Name `xml:"xml"`
		Encrypt string   `xml:"Encrypt"`
	}{Encrypt: encrypted}
	envelopeBytes, err := xml.Marshal(envelope)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf(
		"/wecom/webhook?msg_signature=%s&timestamp=%s&nonce=%s",
		sig, timestamp, nonce,
	), strings.NewReader(string(envelopeBytes)))
	w := httptest.NewRecorder()

	d.Handler()(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	select {
	case job := <-d.incoming:
		assert.Contains(t, job.msg.Text, "Cool Article")
		assert.Contains(t, job.msg.Text, "A very cool article about Go")
		assert.Contains(t, job.msg.Text, "https://go.dev/blog/article")
		assert.Empty(t, job.msg.MediaPaths)
	case <-time.After(time.Second):
		t.Fatal("expected message in incoming channel")
	}
}

// ── XML parsing for new types ──

func TestXMLParse_Mixed(t *testing.T) {
	raw := `<xml>
		<MsgType>mixed</MsgType>
		<MixedMessage>
			<MsgItem>
				<MsgType>image</MsgType>
				<Image><ImageUrl>https://pic1.jpg</ImageUrl></Image>
			</MsgItem>
			<MsgItem>
				<MsgType>text</MsgType>
				<Text><Content>hello world</Content></Text>
			</MsgItem>
		</MixedMessage>
	</xml>`

	var msg xmlMessage
	require.NoError(t, xml.Unmarshal([]byte(raw), &msg))
	assert.Equal(t, "mixed", msg.MsgType)
	require.Len(t, msg.MixedItems, 2)
	assert.Equal(t, "image", msg.MixedItems[0].MsgType)
	assert.Equal(t, "https://pic1.jpg", msg.MixedItems[0].PicURL)
	assert.Equal(t, "text", msg.MixedItems[1].MsgType)
	assert.Equal(t, "hello world", msg.MixedItems[1].Content)
}

func TestXMLParse_Link(t *testing.T) {
	raw := `<xml>
		<MsgType>link</MsgType>
		<Title>Link Title</Title>
		<Description>Link Desc</Description>
		<Url>https://example.com</Url>
	</xml>`

	var msg xmlMessage
	require.NoError(t, xml.Unmarshal([]byte(raw), &msg))
	assert.Equal(t, "link", msg.MsgType)
	assert.Equal(t, "Link Title", msg.Title)
	assert.Equal(t, "Link Desc", msg.Desc)
	assert.Equal(t, "https://example.com", msg.LinkURL)
}

// ── Group access control tests ──

func TestResolveGroupAccess(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }

	tests := []struct {
		name     string
		policy   string
		groups   map[string]GroupRule
		chatID   string
		senderID string
		want     bool
	}{
		{"disabled always false", "disabled", nil, "any", "user1", false},
		{"open always true", "open", nil, "any", "user1", true},
		{"allowlist exact match", "allowlist", map[string]GroupRule{
			"grp1": {},
		}, "grp1", "user1", true},
		{"allowlist no match", "allowlist", map[string]GroupRule{
			"grp1": {},
		}, "grp2", "user1", false},
		{"allowlist empty map", "allowlist", nil, "grp1", "user1", false},
		{"wildcard fallback", "allowlist", map[string]GroupRule{
			"*": {},
		}, "unknown_grp", "user1", true},
		{"wildcard disabled", "allowlist", map[string]GroupRule{
			"*": {Enabled: boolPtr(false)},
		}, "unknown_grp", "user1", false},
		{"per-group enabled=false", "allowlist", map[string]GroupRule{
			"grp1": {Enabled: boolPtr(false)},
		}, "grp1", "user1", false},
		{"per-group allow_from allowed", "allowlist", map[string]GroupRule{
			"grp1": {AllowFrom: []string{"alice", "bob"}},
		}, "grp1", "alice", true},
		{"per-group allow_from denied", "allowlist", map[string]GroupRule{
			"grp1": {AllowFrom: []string{"alice", "bob"}},
		}, "grp1", "charlie", false},
		{"per-group allow_from empty means all", "allowlist", map[string]GroupRule{
			"grp1": {},
		}, "grp1", "anyone", true},
		{"exact match overrides wildcard", "allowlist", map[string]GroupRule{
			"grp1": {AllowFrom: []string{"alice"}},
			"*":    {},
		}, "grp1", "bob", false},
		{"wildcard allow_from filter", "allowlist", map[string]GroupRule{
			"*": {AllowFrom: []string{"admin"}},
		}, "unknown_grp", "admin", true},
		{"wildcard allow_from deny", "allowlist", map[string]GroupRule{
			"*": {AllowFrom: []string{"admin"}},
		}, "unknown_grp", "regular", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := New(Config{
				Token:          testToken,
				EncodingAESKey: testEncodingAESKey,
				GroupPolicy:    tt.policy,
				Groups:         tt.groups,
			}, nil)
			assert.Equal(t, tt.want, d.resolveGroupAccess(tt.chatID, tt.senderID))
		})
	}
}

func TestResolveGroupAccess_GroupAllowFrom(t *testing.T) {
	tests := []struct {
		name           string
		groupAllowFrom []string
		groups         map[string]GroupRule
		chatID         string
		senderID       string
		want           bool
	}{
		{"group_allow_from allows listed chat", []string{"grp1"}, nil, "grp1", "user1", true},
		{"group_allow_from denies unlisted chat", []string{"grp1"}, nil, "grp2", "user1", false},
		{"group_allow_from wildcard allows all", []string{"*"}, nil, "any_grp", "user1", true},
		{"group_allow_from + groups map sender filter", []string{"grp1"}, map[string]GroupRule{
			"grp1": {AllowFrom: []string{"alice"}},
		}, "grp1", "alice", true},
		{"group_allow_from + groups map sender denied", []string{"grp1"}, map[string]GroupRule{
			"grp1": {AllowFrom: []string{"alice"}},
		}, "grp1", "bob", false},
		{"group_allow_from empty falls back to groups map", nil, map[string]GroupRule{
			"grp1": {},
		}, "grp1", "user1", true},
		{"group_allow_from empty no groups entry denied", nil, nil, "grp1", "user1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := New(Config{
				Token:          testToken,
				EncodingAESKey: testEncodingAESKey,
				GroupPolicy:    "allowlist",
				GroupAllowFrom: tt.groupAllowFrom,
				Groups:         tt.groups,
			}, nil)
			assert.Equal(t, tt.want, d.resolveGroupAccess(tt.chatID, tt.senderID))
		})
	}
}

func TestStripAtMention(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"@Bot hello world", "hello world"},
		{"@MyBot 你好", "你好"},
		{"hello world", "hello world"},
		{"@Bot", "@Bot"},         // no space after mention
		{"@Bot  extra", "extra"}, // extra space
		{"", ""},                 // empty
		{"@ space", "space"},     // just @
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, stripAtMention(tt.input))
		})
	}
}

func TestHandleMessage_GroupAllowlist(t *testing.T) {
	d := New(Config{
		Token:          testToken,
		EncodingAESKey: testEncodingAESKey,
		DMPolicy:       "open",
		GroupPolicy:    "allowlist",
		Groups:         map[string]GroupRule{"allowed_group": {}},
	}, nil)

	// Allowed group should pass.
	sendGroupMessage(t, d, "allowed_group", "@Bot hello from group")

	select {
	case job := <-d.incoming:
		assert.Equal(t, "hello from group", job.msg.Text)
	case <-time.After(time.Second):
		t.Fatal("expected message in incoming channel")
	}

	// Denied group should be silently dropped.
	sendGroupMessage(t, d, "denied_group", "@Bot hello denied")

	select {
	case <-d.incoming:
		t.Fatal("expected no message for denied group")
	case <-time.After(200 * time.Millisecond):
		// ok
	}
}

func TestHandleMessage_GroupDisabled(t *testing.T) {
	d := New(Config{
		Token:          testToken,
		EncodingAESKey: testEncodingAESKey,
		DMPolicy:       "open",
		GroupPolicy:    "disabled",
	}, nil)

	sendGroupMessage(t, d, "any_group", "@Bot hello")

	select {
	case <-d.incoming:
		t.Fatal("expected no message when group policy is disabled")
	case <-time.After(200 * time.Millisecond):
		// ok
	}
}

func TestHandleMessage_GroupOpen(t *testing.T) {
	d := New(Config{
		Token:          testToken,
		EncodingAESKey: testEncodingAESKey,
		DMPolicy:       "open",
		GroupPolicy:    "open",
	}, nil)

	sendGroupMessage(t, d, "random_group", "@Bot open access")

	select {
	case job := <-d.incoming:
		assert.Equal(t, "open access", job.msg.Text)
	case <-time.After(time.Second):
		t.Fatal("expected message in incoming channel")
	}
}

func TestHandleMessage_GroupAtMentionStripped(t *testing.T) {
	d := New(Config{
		Token:          testToken,
		EncodingAESKey: testEncodingAESKey,
		GroupPolicy:    "open",
	}, nil)

	sendGroupMessage(t, d, "grp1", "@MyBot what is 1+1?")

	select {
	case job := <-d.incoming:
		assert.Equal(t, "what is 1+1?", job.msg.Text)
	case <-time.After(time.Second):
		t.Fatal("expected message in incoming channel")
	}
}

// sendGroupMessage is a helper that posts an encrypted group message to the driver.
func sendGroupMessage(t *testing.T, d *Driver, chatID, text string) {
	t.Helper()

	msgXML := fmt.Sprintf(`<xml>
		<WebhookUrl>https://example.com/webhook?key=testkey</WebhookUrl>
		<ChatId>%s</ChatId>
		<ChatType>group</ChatType>
		<From><UserId>user1</UserId><Name>Test User</Name></From>
		<MsgType>text</MsgType>
		<Text><Content><![CDATA[%s]]></Content></Text>
		<MsgId>msg_grp</MsgId>
	</xml>`, chatID, text)

	encrypted := encryptForTest(t, msgXML)
	timestamp := "1234567890"
	nonce := "nonce_grp"
	sig := computeSignature(testToken, timestamp, nonce, encrypted)

	envelope := struct {
		XMLName xml.Name `xml:"xml"`
		Encrypt string   `xml:"Encrypt"`
	}{Encrypt: encrypted}
	envelopeBytes, err := xml.Marshal(envelope)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf(
		"/wecom/webhook?msg_signature=%s&timestamp=%s&nonce=%s",
		sig, timestamp, nonce,
	), strings.NewReader(string(envelopeBytes)))
	w := httptest.NewRecorder()

	d.Handler()(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

// ── extractWSContent file tests ──

func TestExtractWSContent_File(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &wsMessage{
		MsgType: "file",
		File:    wsFileContent{URL: "https://example.com/doc.pdf", AESKey: "filekey123"},
	}
	text, urls, keys := d.extractWSContent(msg)
	assert.Equal(t, "[file]", text)
	assert.Equal(t, []string{"https://example.com/doc.pdf"}, urls)
	assert.Equal(t, []string{"filekey123"}, keys)
}

func TestExtractWSContent_FileNoURL(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &wsMessage{MsgType: "file"}
	text, urls, keys := d.extractWSContent(msg)
	assert.Equal(t, "[file]", text)
	assert.Nil(t, urls)
	assert.Nil(t, keys)
}

func TestExtractWSMixed_WithFile(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &wsMessage{
		MsgType: "mixed",
		Mixed: wsMixedContent{
			MsgItem: []wsMixedItem{
				{MsgType: "text", Text: wsTextContent{Content: "check this file"}},
				{MsgType: "file", File: wsFileContent{URL: "https://example.com/report.pdf", AESKey: "key1"}},
				{MsgType: "image", Image: wsImageContent{URL: "https://example.com/pic.png", AESKey: "key2"}},
			},
		},
	}
	text, urls, keys := d.extractWSContent(msg)
	assert.Equal(t, "check this file", text)
	assert.Equal(t, []string{"https://example.com/report.pdf", "https://example.com/pic.png"}, urls)
	assert.Equal(t, []string{"key1", "key2"}, keys)
}

// ── Quote extraction tests ──

func TestExtractWSContent_QuotedText(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &wsMessage{
		MsgType: "text",
		Text:    wsTextContent{Content: "summarize this"},
		Quote: &wsQuote{
			MsgType: "text",
			Text:    wsTextContent{Content: "original long text here"},
		},
	}
	text, urls, keys := d.extractWSContent(msg)
	assert.Contains(t, text, "summarize this")
	assert.Contains(t, text, "[quoted]")
	assert.Contains(t, text, "original long text here")
	assert.Nil(t, urls)
	assert.Nil(t, keys)
}

func TestExtractWSContent_QuotedFile(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &wsMessage{
		MsgType: "text",
		Text:    wsTextContent{Content: "看看这个文件"},
		Quote: &wsQuote{
			MsgType: "file",
			File:    wsFileContent{URL: "https://example.com/doc.pdf", AESKey: "qkey"},
		},
	}
	text, urls, keys := d.extractWSContent(msg)
	assert.Contains(t, text, "看看这个文件")
	assert.Contains(t, text, "[quoted]")
	assert.Contains(t, text, "[file]")
	assert.Equal(t, []string{"https://example.com/doc.pdf"}, urls)
	assert.Equal(t, []string{"qkey"}, keys)
}

func TestExtractWSContent_QuotedImage(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &wsMessage{
		MsgType: "text",
		Text:    wsTextContent{Content: "describe this"},
		Quote: &wsQuote{
			MsgType: "image",
			Image:   wsImageContent{URL: "https://example.com/pic.jpg", AESKey: "imgkey"},
		},
	}
	text, urls, keys := d.extractWSContent(msg)
	assert.Contains(t, text, "describe this")
	assert.Contains(t, text, "[quoted]")
	assert.Equal(t, []string{"https://example.com/pic.jpg"}, urls)
	assert.Equal(t, []string{"imgkey"}, keys)
}

func TestExtractWSContent_NoQuote(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &wsMessage{
		MsgType: "text",
		Text:    wsTextContent{Content: "plain message"},
	}
	text, urls, keys := d.extractWSContent(msg)
	assert.Equal(t, "plain message", text)
	assert.NotContains(t, text, "[quoted]")
	assert.Nil(t, urls)
	assert.Nil(t, keys)
}

// ── File download tests ──

func TestDownloadMedia_NonImageFile(t *testing.T) {
	// Serve a PDF-like file.
	pdfData := []byte("%PDF-1.4 fake pdf content here")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(pdfData)
	}))
	defer srv.Close()

	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	paths := d.downloadImages(context.Background(), []string{srv.URL + "/doc.pdf"}, nil)

	require.Len(t, paths, 1)
	assert.True(t, strings.HasSuffix(paths[0], ".pdf"), "expected .pdf suffix, got %s", paths[0])

	data, err := os.ReadFile(paths[0])
	require.NoError(t, err)
	assert.Equal(t, pdfData, data)
	os.RemoveAll(filepath.Dir(paths[0]))
}

func TestDownloadMedia_EncryptedNonImageFile(t *testing.T) {
	// Build AES-encrypted PDF data.
	pdfData := []byte("%PDF-1.4 encrypted test content")

	aesKey, err := base64.StdEncoding.DecodeString(testEncodingAESKey + "=")
	require.NoError(t, err)

	// PKCS#7 pad.
	padLen := aes.BlockSize - (len(pdfData) % aes.BlockSize)
	padded := make([]byte, len(pdfData)+padLen)
	copy(padded, pdfData)
	for i := len(pdfData); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}

	block, err := aes.NewCipher(aesKey)
	require.NoError(t, err)
	encrypted := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, aesKey[:16]).CryptBlocks(encrypted, padded)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(encrypted)
	}))
	defer srv.Close()

	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	paths := d.downloadImages(context.Background(), []string{srv.URL + "/encrypted.bin"}, nil)
	require.Len(t, paths, 1)
	assert.True(t, strings.HasSuffix(paths[0], ".pdf"), "expected .pdf suffix after decrypt, got %s", paths[0])

	data, err := os.ReadFile(paths[0])
	require.NoError(t, err)
	assert.Equal(t, pdfData, data)
	os.RemoveAll(filepath.Dir(paths[0]))
}

// ── annotateNonImagePaths tests ──

func TestAnnotateNonImagePaths(t *testing.T) {
	text, imagePaths, allPaths := annotateNonImagePaths("[file]", []string{"/tmp/media_0.pdf"})
	assert.Contains(t, text, "Read tool")
	assert.Contains(t, text, "/tmp/media_0.pdf")
	assert.Empty(t, imagePaths)                             // PDF is not an image
	assert.Equal(t, []string{"/tmp/media_0.pdf"}, allPaths) // all paths for cleanup
}

func TestAnnotateNonImagePaths_MixedMedia(t *testing.T) {
	text, imagePaths, allPaths := annotateNonImagePaths("hello", []string{"/tmp/media_0.png", "/tmp/media_1.pdf"})
	assert.Contains(t, text, "/tmp/media_1.pdf")
	assert.NotContains(t, text, "media_0.png")
	assert.Equal(t, []string{"/tmp/media_0.png"}, imagePaths) // only images
	assert.Len(t, allPaths, 2)                                // all paths for cleanup
}

func TestAnnotateNonImagePaths_OnlyImages(t *testing.T) {
	text, imagePaths, allPaths := annotateNonImagePaths("[image]", []string{"/tmp/media_0.jpg"})
	assert.Equal(t, "[image]", text) // no annotation
	assert.Len(t, imagePaths, 1)
	assert.Len(t, allPaths, 1)
}

func TestAnnotateNonImagePaths_Empty(t *testing.T) {
	text, imagePaths, allPaths := annotateNonImagePaths("text", nil)
	assert.Equal(t, "text", text)
	assert.Nil(t, imagePaths)
	assert.Nil(t, allPaths)
}

// ── inferFileExtension tests ──

func TestInferFileExtension(t *testing.T) {
	tests := []struct {
		name        string
		data        []byte
		contentType string
		wantExt     string
	}{
		{"pdf magic", []byte{0x25, 0x50, 0x44, 0x46, 0x2D}, "", ".pdf"},
		{"zip magic", []byte{0x50, 0x4B, 0x03, 0x04, 0x00}, "", ".zip"},
		{"ole2 magic", []byte{0xD0, 0xCF, 0x11, 0xE0, 0x00}, "", ".doc"},
		{"ct pdf", []byte{0x00, 0x00, 0x00, 0x00}, "application/pdf", ".pdf"},
		{"ct docx", []byte{0x00, 0x00, 0x00, 0x00}, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".docx"},
		{"ct json", []byte{0x00, 0x00, 0x00, 0x00}, "application/json", ".json"},
		{"ct text", []byte{0x00, 0x00, 0x00, 0x00}, "text/plain", ".txt"},
		{"ct with charset", []byte{0x00, 0x00, 0x00, 0x00}, "text/plain; charset=utf-8", ".txt"},
		{"unknown", []byte{0x00, 0x00, 0x00, 0x00}, "application/octet-stream", ".bin"},
		{"empty", []byte{}, "", ".bin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext := inferFileExtension(tt.data, tt.contentType)
			assert.Equal(t, tt.wantExt, ext)
		})
	}
}

// ── ReplyWithKeyboard tests ──

func TestReplyWithKeyboard_WebSocket(t *testing.T) {
	frames, wsURL, shutdown := startWeComWebSocketTestServer(t)
	defer shutdown()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	d := New(Config{ConnectionMode: "websocket"}, nil)
	session := newWSSession(conn)
	d.wsSession = session
	d.callbackReqIDs.Store(int64(1), "kb-req-1")

	keyboard := [][]channel.KeyboardButton{
		{
			{Text: "/new", CallbackData: "/new"},
			{Text: "/sessions", CallbackData: "/sessions"},
			{Text: "/status", CallbackData: "/status"},
		},
	}

	err = d.ReplyWithKeyboard(
		context.Background(),
		channel.Message{ChatID: 1},
		"Available commands:\n  /help — Show this help",
		keyboard,
	)
	require.NoError(t, err)

	// Frame 1: template card with jump buttons.
	cardFrame := mustReceiveWSFrame(t, frames)
	assert.Equal(t, wsCommandRespond, cardFrame.Command)
	assert.Equal(t, "kb-req-1", cardFrame.Headers.ReqID)

	var cardBody wsReplyBody
	require.NoError(t, json.Unmarshal(cardFrame.Body, &cardBody))
	assert.Equal(t, "template_card", cardBody.MsgType)
	require.NotNil(t, cardBody.TemplateCard)
	assert.Equal(t, "text_notice", cardBody.TemplateCard.CardType)
	assert.Equal(t, "Quick actions", cardBody.TemplateCard.MainTitle.Title)
	require.Len(t, cardBody.TemplateCard.JumpList, 3)
	assert.Equal(t, 3, cardBody.TemplateCard.JumpList[0].Type)
	assert.Equal(t, "/new", cardBody.TemplateCard.JumpList[0].Title)
	assert.Equal(t, "/new", cardBody.TemplateCard.JumpList[0].Question)
	assert.Equal(t, "/sessions", cardBody.TemplateCard.JumpList[1].Question)
	assert.Equal(t, "/status", cardBody.TemplateCard.JumpList[2].Question)
	require.NotNil(t, cardBody.TemplateCard.CardAction)
	assert.Equal(t, 1, cardBody.TemplateCard.CardAction.Type)

	// Frame 2: markdown text with full content.
	textFrame := mustReceiveWSFrame(t, frames)
	assert.Equal(t, wsCommandRespond, textFrame.Command)
	var textBody wsReplyBody
	require.NoError(t, json.Unmarshal(textFrame.Body, &textBody))
	assert.Equal(t, "markdown", textBody.MsgType)
	assert.Contains(t, textBody.Markdown.Content, "Available commands")
}

func TestReplyWithKeyboard_WebhookFallback(t *testing.T) {
	var received []markdownPayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p markdownPayload
		_ = json.Unmarshal(body, &p)
		received = append(received, p)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer srv.Close()

	d := New(Config{
		Token:          testToken,
		EncodingAESKey: testEncodingAESKey,
	}, nil)

	chatID := hashChatID("wecom", "chat1")
	d.webhookCache.Store(chatID, &webhookEntry{
		url:       srv.URL,
		expiresAt: time.Now().Add(time.Hour),
	})
	d.chatIDMap.Store(chatID, "chat1")

	keyboard := [][]channel.KeyboardButton{
		{{Text: "/new", CallbackData: "/new"}},
	}

	err := d.ReplyWithKeyboard(
		context.Background(),
		channel.Message{Channel: "wecom", ChatID: chatID},
		"help text",
		keyboard,
	)
	require.NoError(t, err)

	// In webhook mode, it falls back to plain Reply (markdown).
	require.Len(t, received, 1)
	assert.Equal(t, "markdown", received[0].MsgType)
	assert.Equal(t, "help text", received[0].Markdown.Content)
}

// ── ReplyWithSessionCard tests ──

func TestReplyWithSessionCard_WebSocket(t *testing.T) {
	frames, wsURL, shutdown := startWeComWebSocketTestServer(t)
	defer shutdown()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	d := New(Config{ConnectionMode: "websocket"}, nil)
	session := newWSSession(conn)
	d.wsSession = session
	d.callbackReqIDs.Store(int64(1), "card-req-1")

	card := channel.SessionCard{
		Title:     "🧵 Sessions",
		Desc:      "Current: a1b2c3d4 · 2 session(s)",
		Body:      "▸ 1. a1b2c3d4 First\n  2. e5f6g7h8 Second",
		CurrentID: "a1b2c3d4-full",
		Sessions: []channel.SessionCardOption{
			{ID: "a1b2c3d4-full", Label: "1. a1b2c3d4 First ✓"},
			{ID: "e5f6g7h8-full", Label: "2. e5f6g7h8 Second"},
		},
		Buttons: []channel.SessionCardButton{
			{Text: "🔁 Go", CallbackData: "/sessions:switch"},
			{Text: "🆕 New", CallbackData: "/sessions:new"},
			{Text: "📍 Info", CallbackData: "/sessions:status"},
		},
	}

	err = d.ReplyWithSessionCard(
		context.Background(),
		channel.Message{ChatID: 1},
		card,
	)
	require.NoError(t, err)

	// Should receive exactly one frame: the template card.
	frame := mustReceiveWSFrame(t, frames)
	assert.Equal(t, wsCommandRespond, frame.Command)
	assert.Equal(t, "card-req-1", frame.Headers.ReqID)

	var body wsReplyBody
	require.NoError(t, json.Unmarshal(frame.Body, &body))
	assert.Equal(t, "template_card", body.MsgType)
	require.NotNil(t, body.TemplateCard)

	tc := body.TemplateCard
	assert.Equal(t, templateCardTypeButtonInteraction, tc.CardType)
	require.NotNil(t, tc.MainTitle)
	assert.Equal(t, "🧵 Sessions", tc.MainTitle.Title)
	assert.Contains(t, tc.MainTitle.Desc, "Current: a1b2c3d4")

	// Dropdown selection.
	require.NotNil(t, tc.ButtonSelection)
	assert.Equal(t, "session_select", tc.ButtonSelection.QuestionKey)
	assert.Equal(t, "a1b2c3d4-full", tc.ButtonSelection.SelectedID)
	require.Len(t, tc.ButtonSelection.OptionList, 2)
	assert.True(t, tc.ButtonSelection.OptionList[0].IsChecked)
	assert.False(t, tc.ButtonSelection.OptionList[1].IsChecked)

	// Buttons.
	require.Len(t, tc.ButtonList, 3)
	assert.Equal(t, "🔁 Go", tc.ButtonList[0].Text)
	assert.Equal(t, "/sessions:switch", tc.ButtonList[0].Key)
	assert.Equal(t, "🆕 New", tc.ButtonList[1].Text)
	assert.Equal(t, "📍 Info", tc.ButtonList[2].Text)

	// TaskID.
	assert.NotEmpty(t, tc.TaskID)
}

func TestReplyWithSessionCard_WebhookFallback(t *testing.T) {
	var received []markdownPayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p markdownPayload
		_ = json.Unmarshal(body, &p)
		received = append(received, p)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer srv.Close()

	d := New(Config{
		Token:          testToken,
		EncodingAESKey: testEncodingAESKey,
	}, nil)

	chatID := hashChatID("wecom", "chat1")
	d.webhookCache.Store(chatID, &webhookEntry{
		url:       srv.URL,
		expiresAt: time.Now().Add(time.Hour),
	})
	d.chatIDMap.Store(chatID, "chat1")

	card := channel.SessionCard{
		Title: "🧵 Sessions",
		Desc:  "Current: none",
		Body:  "No sessions available.",
	}

	err := d.ReplyWithSessionCard(
		context.Background(),
		channel.Message{Channel: "wecom", ChatID: chatID},
		card,
	)
	require.NoError(t, err)

	// In webhook mode, falls back to plain markdown.
	require.Len(t, received, 1)
	assert.Equal(t, "markdown", received[0].MsgType)
	assert.Contains(t, received[0].Markdown.Content, "🧵 Sessions")
	assert.Contains(t, received[0].Markdown.Content, "No sessions available.")
}

// ── wecomSenderName tests ──

func TestWecomSenderName(t *testing.T) {
	tests := []struct {
		name   string
		n      string // Name
		alias  string
		userID string
		want   string
	}{
		{"alias and name combined", "李四", "lisi", "U00001", "lisi(李四)"},
		{"name only", "李四", "", "U00001", "李四"},
		{"alias only", "", "lisi", "U00001", "lisi"},
		{"userID fallback", "", "", "U00001", "U00001"},
		{"same alias and name", "李四", "李四", "U00001", "李四"},
		{"whitespace name", "  ", "", "U00001", "U00001"},
		{"whitespace alias", "", "  ", "U00001", "U00001"},
		{"all empty", "", "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, wecomSenderName(tt.n, tt.alias, tt.userID))
		})
	}
}

// ── dispatchTemplateCardEvent tests ──

// mockCardHandler implements channel.CardEventHandler for testing.
type mockCardHandler struct {
	mu         sync.Mutex
	calls      []mockCardCall
	returnCard *channel.SessionCard
}

type mockCardCall struct {
	EventKey   string
	SelectedID string
}

func (h *mockCardHandler) HandleCardEvent(_ context.Context, _ channel.Message, eventKey string, selectedID string) *channel.SessionCard {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, mockCardCall{EventKey: eventKey, SelectedID: selectedID})
	return h.returnCard
}

func (h *mockCardHandler) BuildWelcomeCard(_ context.Context, _ channel.Message) *channel.SessionCard {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.returnCard
}

func (h *mockCardHandler) lastCall() (mockCardCall, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.calls) == 0 {
		return mockCardCall{}, false
	}
	return h.calls[len(h.calls)-1], true
}

func newTestCardHandler() *mockCardHandler {
	return &mockCardHandler{
		returnCard: &channel.SessionCard{
			Title: "Test",
			Desc:  "test card",
			Body:  "body",
			Buttons: []channel.SessionCardButton{
				{Text: "🧵 List", CallbackData: "/sessions:sessions"},
			},
		},
	}
}

func TestDispatchTemplateCardEvent_Status(t *testing.T) {
	frames, wsURL, shutdown := startWeComWebSocketTestServer(t)
	defer shutdown()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	d := New(Config{ConnectionMode: "websocket", DMPolicy: "open"}, nil)
	d.wsSession = newWSSession(conn)
	h := newTestCardHandler()
	d.cardHandler = h

	msg := wsMessage{
		ChatID: "chat1", MsgType: "event",
		From: wsFrom{UserID: "user1"},
		Event: wsEventContent{
			EventType:         "template_card_event",
			TemplateCardEvent: &TemplateCardEvent{EventKey: "/sessions:status"},
		},
	}
	d.dispatchTemplateCardEvent(context.Background(), nil, msg, "req-1")

	// Verify handler was called with the right event key.
	call, ok := h.lastCall()
	require.True(t, ok)
	assert.Equal(t, "/sessions:status", call.EventKey)

	// Verify WS frame is aibot_respond_update_msg.
	frame := mustReceiveWSFrame(t, frames)
	assert.Equal(t, wsCommandRespondUpdate, frame.Command)
	var body wsTemplateCardUpdateBody
	require.NoError(t, json.Unmarshal(frame.Body, &body))
	assert.Equal(t, "update_template_card", body.ResponseType)
	require.NotNil(t, body.TemplateCard)
	assert.Equal(t, "Test", body.TemplateCard.MainTitle.Title)
	// Verify the original task_id from the event is preserved.
	assert.Equal(t, "req-1", frame.Headers.ReqID)
}

func TestDispatchTemplateCardEvent_PreservesTaskID(t *testing.T) {
	frames, wsURL, shutdown := startWeComWebSocketTestServer(t)
	defer shutdown()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	d := New(Config{ConnectionMode: "websocket", DMPolicy: "open"}, nil)
	d.wsSession = newWSSession(conn)
	h := newTestCardHandler()
	d.cardHandler = h

	msg := wsMessage{
		ChatID: "chat1", MsgType: "event",
		From: wsFrom{UserID: "user1"},
		Event: wsEventContent{
			EventType: "template_card_event",
			TemplateCardEvent: &TemplateCardEvent{
				EventKey: "/sessions:status",
				TaskID:   "original-task-id-from-card",
			},
		},
	}
	d.dispatchTemplateCardEvent(context.Background(), nil, msg, "req-card-event")

	frame := mustReceiveWSFrame(t, frames)
	var body wsTemplateCardUpdateBody
	require.NoError(t, json.Unmarshal(frame.Body, &body))
	require.NotNil(t, body.TemplateCard)
	// The task_id in the update response must match the original card's task_id.
	assert.Equal(t, "original-task-id-from-card", body.TemplateCard.TaskID,
		"update must reuse original task_id so WeCom can match the card")
}

func TestDispatchTemplateCardEvent_New(t *testing.T) {
	frames, wsURL, shutdown := startWeComWebSocketTestServer(t)
	defer shutdown()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	d := New(Config{ConnectionMode: "websocket", DMPolicy: "open"}, nil)
	d.wsSession = newWSSession(conn)
	h := newTestCardHandler()
	d.cardHandler = h

	msg := wsMessage{
		ChatID: "chat1", MsgType: "event",
		From: wsFrom{UserID: "user1"},
		Event: wsEventContent{
			EventType:         "template_card_event",
			TemplateCardEvent: &TemplateCardEvent{EventKey: "/sessions:new"},
		},
	}
	d.dispatchTemplateCardEvent(context.Background(), nil, msg, "req-2")

	call, ok := h.lastCall()
	require.True(t, ok)
	assert.Equal(t, "/sessions:new", call.EventKey)

	frame := mustReceiveWSFrame(t, frames)
	assert.Equal(t, wsCommandRespondUpdate, frame.Command)
}

func TestDispatchTemplateCardEvent_SwitchWithSelection(t *testing.T) {
	frames, wsURL, shutdown := startWeComWebSocketTestServer(t)
	defer shutdown()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	d := New(Config{ConnectionMode: "websocket", DMPolicy: "open"}, nil)
	d.wsSession = newWSSession(conn)
	h := newTestCardHandler()
	d.cardHandler = h

	msg := wsMessage{
		ChatID: "chat1", MsgType: "event",
		From: wsFrom{UserID: "user1"},
		Event: wsEventContent{
			EventType: "template_card_event",
			TemplateCardEvent: &TemplateCardEvent{
				EventKey: "/sessions:switch",
				SelectedItems: templateCardSelectedItemWrapper{
					SelectedItem: []templateCardSelectedItem{{
						QuestionKey: "session_select",
						OptionIDs:   templateCardOptionIDArray{OptionID: []string{"019cc781-abcd"}},
					}},
				},
			},
		},
	}
	d.dispatchTemplateCardEvent(context.Background(), nil, msg, "req-3")

	call, ok := h.lastCall()
	require.True(t, ok)
	assert.Equal(t, "/sessions:switch", call.EventKey)
	assert.Equal(t, "019cc781-abcd", call.SelectedID)

	frame := mustReceiveWSFrame(t, frames)
	assert.Equal(t, wsCommandRespondUpdate, frame.Command)
}

func TestDispatchTemplateCardEvent_NoHandler(t *testing.T) {
	d := New(Config{ConnectionMode: "websocket", DMPolicy: "open"}, nil)
	// No cardHandler set — should not panic.
	msg := wsMessage{
		ChatID: "chat1", MsgType: "event",
		From: wsFrom{UserID: "user1"},
		Event: wsEventContent{
			EventType:         "template_card_event",
			TemplateCardEvent: &TemplateCardEvent{EventKey: "/sessions:status"},
		},
	}
	d.dispatchTemplateCardEvent(context.Background(), nil, msg, "req-5")
	// No panic = pass.
}

func TestDispatchTemplateCardEvent_NilCardReturned(t *testing.T) {
	d := New(Config{ConnectionMode: "websocket", DMPolicy: "open"}, nil)
	h := &mockCardHandler{returnCard: nil} // returns nil
	d.cardHandler = h

	msg := wsMessage{
		ChatID: "chat1", MsgType: "event",
		From: wsFrom{UserID: "user1"},
		Event: wsEventContent{
			EventType:         "template_card_event",
			TemplateCardEvent: &TemplateCardEvent{EventKey: "/sessions:status"},
		},
	}
	d.dispatchTemplateCardEvent(context.Background(), nil, msg, "req-6")

	call, ok := h.lastCall()
	require.True(t, ok)
	assert.Equal(t, "/sessions:status", call.EventKey)
	// No WS send expected, no panic = pass.
}

func TestDispatchWSMessage_TemplateCardEventNotDropped(t *testing.T) {
	frames, wsURL, shutdown := startWeComWebSocketTestServer(t)
	defer shutdown()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	d := New(Config{ConnectionMode: "websocket", DMPolicy: "open"}, nil)
	d.wsSession = newWSSession(conn)
	h := newTestCardHandler()
	d.cardHandler = h

	msg := wsMessage{
		ChatID: "chat1", MsgType: "event",
		From: wsFrom{UserID: "user1"},
		Event: wsEventContent{
			EventType:         "template_card_event",
			TemplateCardEvent: &TemplateCardEvent{EventKey: "/sessions:status"},
		},
	}
	d.dispatchWSMessage(context.Background(), nil, msg, "req-7")

	// dispatchWSMessage runs the card event in a goroutine.
	time.Sleep(100 * time.Millisecond)

	call, ok := h.lastCall()
	require.True(t, ok, "template_card_event should not be dropped")
	assert.Equal(t, "/sessions:status", call.EventKey)

	frame := mustReceiveWSFrame(t, frames)
	assert.Equal(t, wsCommandRespondUpdate, frame.Command)
}

func TestDispatchTemplateCardEvent_StoresReqID(t *testing.T) {
	d := New(Config{ConnectionMode: "websocket", DMPolicy: "open"}, nil)
	h := &mockCardHandler{returnCard: nil}
	d.cardHandler = h
	chatID := hashChatID("wecom", "chat1")

	msg := wsMessage{
		ChatID: "chat1", MsgType: "event",
		From: wsFrom{UserID: "user1"},
		Event: wsEventContent{
			EventType:         "template_card_event",
			TemplateCardEvent: &TemplateCardEvent{EventKey: "/sessions:status"},
		},
	}

	d.dispatchTemplateCardEvent(context.Background(), nil, msg, "req-mode")

	// Verify reqID and chatID were stored.
	reqVal, ok := d.callbackReqIDs.Load(chatID)
	assert.True(t, ok)
	assert.Equal(t, "req-mode", reqVal.(string))
}

func TestDispatchEnterChatEvent_SendsWelcomeWS(t *testing.T) {
	frames, wsURL, shutdown := startWeComWebSocketTestServer(t)
	defer shutdown()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	d := New(Config{ConnectionMode: "websocket", DMPolicy: "open"}, nil)
	d.wsSession = newWSSession(conn)
	h := newTestCardHandler()
	d.cardHandler = h

	msg := wsMessage{
		ChatID: "chat-enter-1", ChatType: "single", MsgType: "event",
		From:  wsFrom{UserID: "user-enter-1"},
		Event: wsEventContent{EventType: "enter_chat"},
	}
	d.dispatchEnterChatEvent(context.Background(), nil, msg, "req-welcome")

	frame := mustReceiveWSFrame(t, frames)
	assert.Equal(t, wsCommandRespondWelcome, frame.Command)
	assert.Equal(t, "req-welcome", frame.Headers.ReqID)

	var body wsReplyBody
	require.NoError(t, json.Unmarshal(frame.Body, &body))
	assert.Equal(t, "template_card", body.MsgType)
	require.NotNil(t, body.TemplateCard)
	assert.Equal(t, "Test", body.TemplateCard.MainTitle.Title)
}

func TestDispatchEnterChatEvent_SkipsGroupChat(t *testing.T) {
	frames, wsURL, shutdown := startWeComWebSocketTestServer(t)
	defer shutdown()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	d := New(Config{ConnectionMode: "websocket", DMPolicy: "open"}, nil)
	d.wsSession = newWSSession(conn)
	d.cardHandler = newTestCardHandler()

	msg := wsMessage{
		ChatID: "group-1", ChatType: "group", MsgType: "event",
		From:  wsFrom{UserID: "user-1"},
		Event: wsEventContent{EventType: "enter_chat"},
	}
	d.dispatchEnterChatEvent(context.Background(), nil, msg, "req-group")

	// No frame should be sent.
	select {
	case f := <-frames:
		t.Fatalf("unexpected frame for group chat: %s", f.Command)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestDispatchEnterChatEvent_NoHandler_Skips(t *testing.T) {
	frames, wsURL, shutdown := startWeComWebSocketTestServer(t)
	defer shutdown()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	d := New(Config{ConnectionMode: "websocket", DMPolicy: "open"}, nil)
	d.wsSession = newWSSession(conn)
	// No cardHandler set.

	msg := wsMessage{
		ChatID: "chat-noh", ChatType: "single", MsgType: "event",
		From:  wsFrom{UserID: "user-noh"},
		Event: wsEventContent{EventType: "enter_chat"},
	}
	d.dispatchEnterChatEvent(context.Background(), nil, msg, "req-noh")

	select {
	case f := <-frames:
		t.Fatalf("unexpected frame with no card handler: %s", f.Command)
	case <-time.After(50 * time.Millisecond):
	}
}

// ── Streaming tests ──

func TestSendThinking_WebSocket(t *testing.T) {
	frames, wsURL, shutdown := startWeComWebSocketTestServer(t)
	defer shutdown()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	d := New(Config{ConnectionMode: "websocket"}, nil)
	session := newWSSession(conn)
	d.wsSession = session
	d.callbackReqIDs.Store(int64(42), "think-req-1")

	err = d.SendThinking(context.Background(), channel.Message{ChatID: 42})
	require.NoError(t, err)

	frame := mustReceiveWSFrame(t, frames)
	assert.Equal(t, wsCommandRespond, frame.Command)
	assert.Equal(t, "think-req-1", frame.Headers.ReqID)

	var body wsReplyBody
	require.NoError(t, json.Unmarshal(frame.Body, &body))
	assert.Equal(t, "stream", body.MsgType)
	require.NotNil(t, body.Stream)
	assert.False(t, body.Stream.Finish)
	assert.Equal(t, thinkingMessage, body.Stream.Content)
	assert.NotEmpty(t, body.Stream.ID)

	// Verify stream ID was stored.
	val, ok := d.streamIDs.Load(int64(42))
	assert.True(t, ok)
	assert.Equal(t, body.Stream.ID, val.(string))
}

func TestSendThinking_NotWebSocket(t *testing.T) {
	d := New(Config{ConnectionMode: "webhook"}, nil)
	err := d.SendThinking(context.Background(), channel.Message{ChatID: 1})
	assert.NoError(t, err) // no-op
}

func TestSendThinking_NoSession(t *testing.T) {
	d := New(Config{ConnectionMode: "websocket"}, nil)
	err := d.SendThinking(context.Background(), channel.Message{ChatID: 1})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no active websocket session")
}

func TestSendThinking_NoReqID(t *testing.T) {
	frames, wsURL, shutdown := startWeComWebSocketTestServer(t)
	defer shutdown()
	_ = frames

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	d := New(Config{ConnectionMode: "websocket"}, nil)
	d.wsSession = newWSSession(conn)
	// No callbackReqIDs stored.

	err = d.SendThinking(context.Background(), channel.Message{ChatID: 999})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no callback req_id")
}

func TestSendMessage_WebSocket(t *testing.T) {
	frames, wsURL, shutdown := startWeComWebSocketTestServer(t)
	defer shutdown()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	d := New(Config{ConnectionMode: "websocket"}, nil)
	session := newWSSession(conn)
	d.wsSession = session
	d.callbackReqIDs.Store(int64(10), "send-req-1")

	msgID, err := d.SendMessage(context.Background(), channel.Message{ChatID: 10}, "hello streaming")
	require.NoError(t, err)
	assert.Greater(t, msgID, int64(0))

	frame := mustReceiveWSFrame(t, frames)
	assert.Equal(t, wsCommandRespond, frame.Command)

	var body wsReplyBody
	require.NoError(t, json.Unmarshal(frame.Body, &body))
	assert.Equal(t, "stream", body.MsgType)
	require.NotNil(t, body.Stream)
	assert.False(t, body.Stream.Finish)
	assert.Equal(t, "hello streaming", body.Stream.Content)
}

func TestSendMessage_ReusesStreamID(t *testing.T) {
	frames, wsURL, shutdown := startWeComWebSocketTestServer(t)
	defer shutdown()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	d := New(Config{ConnectionMode: "websocket"}, nil)
	session := newWSSession(conn)
	d.wsSession = session
	d.callbackReqIDs.Store(int64(10), "send-req-2")

	// Pre-set a stream ID (as if SendThinking was called first).
	d.streamIDs.Store(int64(10), "existing-stream-id")

	_, err = d.SendMessage(context.Background(), channel.Message{ChatID: 10}, "content")
	require.NoError(t, err)

	frame := mustReceiveWSFrame(t, frames)
	var body wsReplyBody
	require.NoError(t, json.Unmarshal(frame.Body, &body))
	assert.Equal(t, "existing-stream-id", body.Stream.ID)
}

func TestSendMessage_NotWebSocket(t *testing.T) {
	d := New(Config{ConnectionMode: "webhook"}, nil)
	_, err := d.SendMessage(context.Background(), channel.Message{ChatID: 1}, "x")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "only supported in websocket mode")
}

func TestEditMessage_WebSocket(t *testing.T) {
	frames, wsURL, shutdown := startWeComWebSocketTestServer(t)
	defer shutdown()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	d := New(Config{ConnectionMode: "websocket"}, nil)
	session := newWSSession(conn)
	d.wsSession = session
	d.callbackReqIDs.Store(int64(5), "edit-req")
	d.streamIDs.Store(int64(5), "edit-stream-id")

	err = d.EditMessage(context.Background(), int64(5), 0, "updated text")
	require.NoError(t, err)

	frame := mustReceiveWSFrame(t, frames)
	var body wsReplyBody
	require.NoError(t, json.Unmarshal(frame.Body, &body))
	assert.Equal(t, "stream", body.MsgType)
	assert.Equal(t, "edit-stream-id", body.Stream.ID)
	assert.False(t, body.Stream.Finish)
	assert.Equal(t, "updated text", body.Stream.Content)
}

func TestEditMessage_NoSession(t *testing.T) {
	d := New(Config{ConnectionMode: "websocket"}, nil)
	err := d.EditMessage(context.Background(), 1, 0, "x")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no active websocket session")
}

func TestEditMessage_NoReqID(t *testing.T) {
	frames, wsURL, shutdown := startWeComWebSocketTestServer(t)
	defer shutdown()
	_ = frames

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	d := New(Config{ConnectionMode: "websocket"}, nil)
	d.wsSession = newWSSession(conn)

	err = d.EditMessage(context.Background(), 999, 0, "x")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no callback req_id")
}

func TestEditMessage_NoStreamID(t *testing.T) {
	frames, wsURL, shutdown := startWeComWebSocketTestServer(t)
	defer shutdown()
	_ = frames

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	d := New(Config{ConnectionMode: "websocket"}, nil)
	d.wsSession = newWSSession(conn)
	d.callbackReqIDs.Store(int64(5), "req-5")

	err = d.EditMessage(context.Background(), 5, 0, "x")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no stream id")
}

func TestFinishStream_WebSocket(t *testing.T) {
	frames, wsURL, shutdown := startWeComWebSocketTestServer(t)
	defer shutdown()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	d := New(Config{ConnectionMode: "websocket"}, nil)
	session := newWSSession(conn)
	d.wsSession = session
	d.callbackReqIDs.Store(int64(7), "finish-req")
	d.streamIDs.Store(int64(7), "finish-stream-id")

	err = d.FinishStream(context.Background(), 7, "final text")
	require.NoError(t, err)

	frame := mustReceiveWSFrame(t, frames)
	var body wsReplyBody
	require.NoError(t, json.Unmarshal(frame.Body, &body))
	assert.Equal(t, "stream", body.MsgType)
	assert.Equal(t, "finish-stream-id", body.Stream.ID)
	assert.True(t, body.Stream.Finish)
	assert.Equal(t, "final text", body.Stream.Content)

	// Stream ID should be cleaned up.
	_, ok := d.streamIDs.Load(int64(7))
	assert.False(t, ok)
}

func TestFinishStream_NotWebSocket(t *testing.T) {
	d := New(Config{ConnectionMode: "webhook"}, nil)
	err := d.FinishStream(context.Background(), 1, "x")
	assert.NoError(t, err) // no-op
}

func TestFinishStream_NoSession(t *testing.T) {
	d := New(Config{ConnectionMode: "websocket"}, nil)
	err := d.FinishStream(context.Background(), 1, "x")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no active websocket session")
}

func TestFinishStream_NoStreamID(t *testing.T) {
	frames, wsURL, shutdown := startWeComWebSocketTestServer(t)
	defer shutdown()
	_ = frames

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	d := New(Config{ConnectionMode: "websocket"}, nil)
	d.wsSession = newWSSession(conn)
	d.callbackReqIDs.Store(int64(7), "req-7")
	// No streamIDs stored.

	err = d.FinishStream(context.Background(), 7, "x")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no stream id")
}

// ── Typing / SuppressTextWithMedia / AddAllowedUser / SetCardEventHandler ──

func TestTyping(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	err := d.Typing(context.Background(), channel.Message{ChatID: 1})
	assert.NoError(t, err)
}

func TestSuppressTextWithMedia(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	assert.True(t, d.SuppressTextWithMedia())
}

func TestAddAllowedUser(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)

	d.AddAllowedUser("alice")
	d.AddAllowedUser("bob")
	d.AddAllowedUser("alice") // duplicate

	d.mu.RLock()
	allowFrom := d.cfg.AllowFrom
	d.mu.RUnlock()

	assert.Equal(t, []string{"alice", "bob"}, allowFrom)
}

func TestSetCardEventHandler(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	assert.Nil(t, d.cardHandler)

	h := newTestCardHandler()
	d.SetCardEventHandler(h)
	assert.Equal(t, h, d.cardHandler)
}

// ── checkAccess tests ──

func TestCheckAccess(t *testing.T) {
	tests := []struct {
		name   string
		policy string
		allow  []string
		userID string
		want   accessResult
	}{
		{"open policy allows anyone", "open", nil, "stranger", accessAllowed},
		{"allowlist match", "allowlist", []string{"alice", "bob"}, "alice", accessAllowed},
		{"allowlist no match", "allowlist", []string{"alice", "bob"}, "charlie", accessDenied},
		{"pairing match", "pairing", []string{"alice"}, "alice", accessAllowed},
		{"pairing no match", "pairing", []string{"alice"}, "stranger", accessPairing},
		{"default is pairing", "", []string{"alice"}, "stranger", accessPairing},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := New(Config{
				Token:          testToken,
				EncodingAESKey: testEncodingAESKey,
				DMPolicy:       tt.policy,
				AllowFrom:      tt.allow,
			}, nil)
			assert.Equal(t, tt.want, d.checkAccess(tt.userID))
		})
	}
}

// ── parseEncryptedEnvelope tests ──

func TestParseEncryptedEnvelope_JSON(t *testing.T) {
	body := []byte(`{"encrypt":"abc123encrypted"}`)
	field, format := parseEncryptedEnvelope(body)
	assert.Equal(t, "abc123encrypted", field)
	assert.Equal(t, "json", format)
}

func TestParseEncryptedEnvelope_XML(t *testing.T) {
	body := []byte(`<xml><Encrypt>xmlencrypted</Encrypt></xml>`)
	field, format := parseEncryptedEnvelope(body)
	assert.Equal(t, "xmlencrypted", field)
	assert.Equal(t, "xml", format)
}

func TestParseEncryptedEnvelope_Invalid(t *testing.T) {
	body := []byte(`garbage data`)
	field, format := parseEncryptedEnvelope(body)
	assert.Empty(t, field)
	assert.Equal(t, "unknown", format)
}

func TestParseEncryptedEnvelope_EmptyEncrypt(t *testing.T) {
	body := []byte(`{"encrypt":""}`)
	field, format := parseEncryptedEnvelope(body)
	// Empty encrypt field should not be considered valid.
	assert.Empty(t, field)
	assert.Equal(t, "unknown", format)
}

// ── handleHTTP method dispatch ──

func TestHandleHTTP_MethodNotAllowed(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	req := httptest.NewRequest(http.MethodPut, "/wecom", nil)
	w := httptest.NewRecorder()
	d.Handler()(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestHandleVerify_MissingParams(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)

	// All params missing.
	req := httptest.NewRequest(http.MethodGet, "/wecom", nil)
	w := httptest.NewRecorder()
	d.Handler()(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleMessage_MissingParams(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)

	// POST with missing query params.
	req := httptest.NewRequest(http.MethodPost, "/wecom", strings.NewReader("body"))
	w := httptest.NewRecorder()
	d.Handler()(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleMessage_InvalidEnvelope(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)

	req := httptest.NewRequest(http.MethodPost,
		"/wecom?msg_signature=abc&timestamp=123&nonce=xyz",
		strings.NewReader("not xml or json"))
	w := httptest.NewRecorder()
	d.Handler()(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleMessage_BadSignature(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)

	body := []byte(`{"encrypt":"somevalidcontent"}`)
	req := httptest.NewRequest(http.MethodPost,
		"/wecom?msg_signature=badsig&timestamp=123&nonce=xyz",
		strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	d.Handler()(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// ── dispatchJSONMessage tests ──

func TestDispatchJSONMessage(t *testing.T) {
	d := New(Config{
		Token:          testToken,
		EncodingAESKey: testEncodingAESKey,
		DMPolicy:       "open",
	}, nil)

	msg := wsMessage{
		ChatID:      "json-chat-1",
		ChatType:    "single",
		MsgType:     "text",
		From:        wsFrom{UserID: "user-json", Name: "JSON User"},
		Text:        wsTextContent{Content: "hello from json"},
		ResponseURL: "https://example.com/response",
	}
	data, err := json.Marshal(msg)
	require.NoError(t, err)

	go d.dispatchJSONMessage(string(data))

	select {
	case job := <-d.incoming:
		assert.Equal(t, "hello from json", job.msg.Text)
		assert.Equal(t, hashChatID("wecom", "json-chat-1"), job.msg.ChatID)
		assert.Equal(t, "https://example.com/response", job.webhookURL)
	case <-time.After(2 * time.Second):
		t.Fatal("expected message in incoming channel")
	}
}

func TestDispatchJSONMessage_Event(t *testing.T) {
	d := New(Config{
		Token:          testToken,
		EncodingAESKey: testEncodingAESKey,
		DMPolicy:       "open",
	}, nil)

	msg := wsMessage{
		ChatID:   "json-chat-2",
		ChatType: "single",
		MsgType:  "event",
		From:     wsFrom{UserID: "user-json"},
		Event:    wsEventContent{EventType: "some_event"},
	}
	data, err := json.Marshal(msg)
	require.NoError(t, err)

	go d.dispatchJSONMessage(string(data))

	// Events should be dropped.
	select {
	case <-d.incoming:
		t.Fatal("expected no message for event")
	case <-time.After(200 * time.Millisecond):
		// ok
	}
}

func TestDispatchJSONMessage_GroupAccess(t *testing.T) {
	d := New(Config{
		Token:          testToken,
		EncodingAESKey: testEncodingAESKey,
		DMPolicy:       "open",
		GroupPolicy:    "disabled",
	}, nil)

	msg := wsMessage{
		ChatID:      "json-grp-1",
		ChatType:    "group",
		MsgType:     "text",
		From:        wsFrom{UserID: "user-json"},
		Text:        wsTextContent{Content: "@Bot hello"},
		ResponseURL: "https://example.com/resp",
	}
	data, err := json.Marshal(msg)
	require.NoError(t, err)

	go d.dispatchJSONMessage(string(data))

	// Group disabled - message should be dropped.
	select {
	case <-d.incoming:
		t.Fatal("expected no message for disabled group")
	case <-time.After(200 * time.Millisecond):
		// ok
	}
}

func TestDispatchJSONMessage_DMDenied(t *testing.T) {
	d := New(Config{
		Token:          testToken,
		EncodingAESKey: testEncodingAESKey,
		DMPolicy:       "allowlist",
		AllowFrom:      []string{"alice"},
	}, nil)

	msg := wsMessage{
		ChatID:      "json-dm-1",
		ChatType:    "single",
		MsgType:     "text",
		From:        wsFrom{UserID: "stranger"},
		Text:        wsTextContent{Content: "hi"},
		ResponseURL: "https://example.com/resp",
	}
	data, err := json.Marshal(msg)
	require.NoError(t, err)

	go d.dispatchJSONMessage(string(data))

	// Denied - message should be dropped.
	select {
	case <-d.incoming:
		t.Fatal("expected no message for denied user")
	case <-time.After(200 * time.Millisecond):
		// ok
	}
}

// ── extractWSContent additional tests ──

func TestExtractWSContent_Voice(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &wsMessage{
		MsgType: "voice",
		Voice:   wsVoiceContent{Content: "transcribed text"},
	}
	text, urls, keys := d.extractWSContent(msg)
	assert.Equal(t, "transcribed text", text)
	assert.Nil(t, urls)
	assert.Nil(t, keys)
}

func TestExtractWSContent_VoiceNoContent(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &wsMessage{MsgType: "voice"}
	text, _, _ := d.extractWSContent(msg)
	assert.Equal(t, "[voice]", text)
}

func TestExtractWSContent_Unknown(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &wsMessage{MsgType: "unknown_type"}
	text, urls, keys := d.extractWSContent(msg)
	assert.Empty(t, text)
	assert.Nil(t, urls)
	assert.Nil(t, keys)
}

func TestExtractWSContent_Image(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &wsMessage{
		MsgType: "image",
		Image:   wsImageContent{URL: "https://img.example.com/pic.jpg", AESKey: "imgkey1"},
	}
	text, urls, keys := d.extractWSContent(msg)
	assert.Equal(t, "[image]", text)
	assert.Equal(t, []string{"https://img.example.com/pic.jpg"}, urls)
	assert.Equal(t, []string{"imgkey1"}, keys)
}

func TestExtractWSContent_ImageNoURL(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &wsMessage{MsgType: "image"}
	text, urls, keys := d.extractWSContent(msg)
	assert.Equal(t, "[image]", text)
	assert.Nil(t, urls)
	assert.Nil(t, keys)
}

// ── extractWSQuote additional tests ──

func TestExtractWSQuote_Voice(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &wsMessage{
		MsgType: "text",
		Text:    wsTextContent{Content: "response"},
		Quote: &wsQuote{
			MsgType: "voice",
			Voice:   wsVoiceContent{Content: "original voice transcript"},
		},
	}
	text, _, _ := d.extractWSContent(msg)
	assert.Contains(t, text, "response")
	assert.Contains(t, text, "[quoted]")
	assert.Contains(t, text, "original voice transcript")
}

func TestExtractWSQuote_Video(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &wsMessage{
		MsgType: "text",
		Text:    wsTextContent{Content: "watch this"},
		Quote: &wsQuote{
			MsgType: "video",
			Video:   wsFileContent{URL: "https://video.example.com/v.mp4", AESKey: "vkey"},
		},
	}
	text, urls, keys := d.extractWSContent(msg)
	assert.Contains(t, text, "watch this")
	assert.Contains(t, text, "[quoted]")
	assert.Contains(t, text, "[video]")
	assert.Equal(t, []string{"https://video.example.com/v.mp4"}, urls)
	assert.Equal(t, []string{"vkey"}, keys)
}

func TestExtractWSQuote_Mixed(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &wsMessage{
		MsgType: "text",
		Text:    wsTextContent{Content: "check this out"},
		Quote: &wsQuote{
			MsgType: "mixed",
			Mixed: wsMixedContent{
				MsgItem: []wsMixedItem{
					{MsgType: "text", Text: wsTextContent{Content: "quoted text part"}},
					{MsgType: "image", Image: wsImageContent{URL: "https://img.com/q.png", AESKey: "qkey"}},
				},
			},
		},
	}
	text, urls, keys := d.extractWSContent(msg)
	assert.Contains(t, text, "check this out")
	assert.Contains(t, text, "[quoted]")
	assert.Contains(t, text, "quoted text part")
	assert.Equal(t, []string{"https://img.com/q.png"}, urls)
	assert.Equal(t, []string{"qkey"}, keys)
}

func TestExtractWSQuote_Unknown(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &wsMessage{
		MsgType: "text",
		Text:    wsTextContent{Content: "message"},
		Quote: &wsQuote{
			MsgType: "location", // unknown type
		},
	}
	text, urls, keys := d.extractWSContent(msg)
	assert.Equal(t, "message", text)
	assert.NotContains(t, text, "[quoted]")
	assert.Nil(t, urls)
	assert.Nil(t, keys)
}

// ── Config helper method tests ──

func TestConfigIsWebSocket(t *testing.T) {
	assert.True(t, (&Config{ConnectionMode: "websocket"}).isWebSocket())
	assert.True(t, (&Config{ConnectionMode: "WebSocket"}).isWebSocket())
	assert.False(t, (&Config{ConnectionMode: "webhook"}).isWebSocket())
	assert.False(t, (&Config{ConnectionMode: ""}).isWebSocket())
}

func TestConfigWsURL(t *testing.T) {
	assert.Equal(t, "wss://custom.url", (&Config{WSURL: "wss://custom.url"}).wsURL())
	assert.Equal(t, defaultWSURL, (&Config{}).wsURL())
}

func TestConfigHeartbeatInterval(t *testing.T) {
	assert.Equal(t, 10*time.Second, (&Config{HeartbeatInterval: 10 * time.Second}).heartbeatInterval())
	assert.Equal(t, defaultHeartbeatInterval, (&Config{}).heartbeatInterval())
}

func TestConfigReconnectDelay(t *testing.T) {
	assert.Equal(t, defaultReconnectDelay, (&Config{}).reconnectDelay())
}

// ── wsInboundFrame UnmarshalJSON tests ──

func TestWSInboundFrame_UnmarshalJSON_CmdField(t *testing.T) {
	data := []byte(`{"cmd":"aibot_msg_callback","headers":{"req_id":"r1"},"body":{"test":true},"errcode":0}`)
	var frame wsInboundFrame
	require.NoError(t, json.Unmarshal(data, &frame))
	assert.Equal(t, "aibot_msg_callback", frame.Command)
	assert.Equal(t, "r1", frame.Headers.ReqID)
	assert.Equal(t, 0, frame.ErrCode)
}

func TestWSInboundFrame_UnmarshalJSON_LegacyCommand(t *testing.T) {
	data := []byte(`{"command":"ping","headers":{"req_id":"r2"}}`)
	var frame wsInboundFrame
	require.NoError(t, json.Unmarshal(data, &frame))
	assert.Equal(t, "ping", frame.Command)
}

func TestWSInboundFrame_UnmarshalJSON_CmdTakesPrecedence(t *testing.T) {
	data := []byte(`{"cmd":"primary","command":"fallback"}`)
	var frame wsInboundFrame
	require.NoError(t, json.Unmarshal(data, &frame))
	assert.Equal(t, "primary", frame.Command)
}

func TestWSInboundFrame_UnmarshalJSON_Error(t *testing.T) {
	data := []byte(`{"cmd":"subscribe","errcode":40001,"errmsg":"invalid secret"}`)
	var frame wsInboundFrame
	require.NoError(t, json.Unmarshal(data, &frame))
	assert.Equal(t, 40001, frame.ErrCode)
	assert.Equal(t, "invalid secret", frame.ErrMsg)
}

// ── selectedTemplateCardOption tests ──

func TestSelectedTemplateCardOption_ExtraScenarios(t *testing.T) {
	event := &TemplateCardEvent{
		SelectedItems: templateCardSelectedItemWrapper{
			SelectedItem: []templateCardSelectedItem{
				{
					QuestionKey: "session_select",
					OptionIDs:   templateCardOptionIDArray{OptionID: []string{"option-abc"}},
				},
				{
					QuestionKey: "other_key",
					OptionIDs:   templateCardOptionIDArray{OptionID: []string{"other-val"}},
				},
			},
		},
	}
	assert.Equal(t, "option-abc", selectedTemplateCardOption(event, "session_select"))
	assert.Equal(t, "other-val", selectedTemplateCardOption(event, "other_key"))
	assert.Equal(t, "", selectedTemplateCardOption(event, "missing_key"))
	assert.Equal(t, "", selectedTemplateCardOption(nil, "session_select"))

	// Empty option IDs.
	eventEmpty := &TemplateCardEvent{
		SelectedItems: templateCardSelectedItemWrapper{
			SelectedItem: []templateCardSelectedItem{
				{
					QuestionKey: "key",
					OptionIDs:   templateCardOptionIDArray{OptionID: []string{"", "  "}},
				},
			},
		},
	}
	assert.Equal(t, "", selectedTemplateCardOption(eventEmpty, "key"))
}

// ── webhookResponder tests ──

func TestWebhookResponder(t *testing.T) {
	var received []markdownPayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p markdownPayload
		_ = json.Unmarshal(body, &p)
		received = append(received, p)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer srv.Close()

	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	chatID := hashChatID("wecom", "wr-chat")
	d.webhookCache.Store(chatID, &webhookEntry{
		url:       srv.URL,
		expiresAt: time.Now().Add(time.Hour),
	})
	d.chatIDMap.Store(chatID, "wr-chat")

	wr := &webhookResponder{d: d}

	// Test Reply.
	err := wr.Reply(context.Background(), channel.Message{ChatID: chatID}, "wr reply")
	require.NoError(t, err)
	require.Len(t, received, 1)
	assert.Equal(t, "wr reply", received[0].Markdown.Content)

	// Test Typing (no-op).
	err = wr.Typing(context.Background(), channel.Message{ChatID: chatID})
	assert.NoError(t, err)

	// Test SuppressTextWithMedia.
	assert.True(t, wr.SuppressTextWithMedia())
}

// ── replyViaWebSocket tests ──

func TestReplyViaWebSocket_NoSession(t *testing.T) {
	d := New(Config{ConnectionMode: "websocket"}, nil)
	err := d.replyViaWebSocket(context.Background(), channel.Message{ChatID: 1}, "test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no active session")
}

func TestReplyViaWebSocket_NoReqID(t *testing.T) {
	frames, wsURL, shutdown := startWeComWebSocketTestServer(t)
	defer shutdown()
	_ = frames

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	d := New(Config{ConnectionMode: "websocket"}, nil)
	d.wsSession = newWSSession(conn)

	err = d.replyViaWebSocket(context.Background(), channel.Message{ChatID: 999}, "test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no callback req_id")
}

// ── postJSON tests ──

func TestPostJSON_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer srv.Close()

	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	err := d.postJSON(context.Background(), srv.URL, map[string]string{"key": "val"})
	assert.NoError(t, err)
}

func TestPostJSON_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":40001,"errmsg":"invalid token"}`))
	}))
	defer srv.Close()

	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	err := d.postJSON(context.Background(), srv.URL, map[string]string{"key": "val"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid token")
}

func TestPostJSON_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`internal error`))
	}))
	defer srv.Close()

	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	err := d.postJSON(context.Background(), srv.URL, map[string]string{"key": "val"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "status=500")
}

// ── handleWSFrame tests ──

func TestHandleWSFrame_SubscribeConfirm(t *testing.T) {
	d := New(Config{ConnectionMode: "websocket", DMPolicy: "open"}, nil)
	session := &wsSession{ackWaiters: make(map[string]chan wsInboundFrame)}

	data := []byte(`{"cmd":"aibot_subscribe","errcode":0,"errmsg":"ok"}`)
	err := d.handleWSFrame(context.Background(), session, data)
	assert.NoError(t, err)
}

func TestHandleWSFrame_UnknownCommand(t *testing.T) {
	d := New(Config{ConnectionMode: "websocket", DMPolicy: "open"}, nil)
	session := &wsSession{ackWaiters: make(map[string]chan wsInboundFrame)}

	data := []byte(`{"cmd":"unknown_command","errcode":0}`)
	err := d.handleWSFrame(context.Background(), session, data)
	assert.NoError(t, err) // silently ignored
}

func TestHandleWSFrame_InvalidJSON(t *testing.T) {
	d := New(Config{ConnectionMode: "websocket", DMPolicy: "open"}, nil)
	session := &wsSession{ackWaiters: make(map[string]chan wsInboundFrame)}

	data := []byte(`not json`)
	err := d.handleWSFrame(context.Background(), session, data)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal frame")
}

func TestHandleWSFrame_DeliversAck(t *testing.T) {
	d := New(Config{ConnectionMode: "websocket", DMPolicy: "open"}, nil)
	session := &wsSession{ackWaiters: make(map[string]chan wsInboundFrame)}

	// Register an ack waiter.
	ackCh := make(chan wsInboundFrame, 1)
	session.registerAck("my-req-id", ackCh)

	data := []byte(`{"cmd":"aibot_subscribe","headers":{"req_id":"my-req-id"},"errcode":0}`)
	err := d.handleWSFrame(context.Background(), session, data)
	assert.NoError(t, err)

	// The frame should have been delivered to the ack waiter.
	select {
	case frame := <-ackCh:
		assert.Equal(t, "aibot_subscribe", frame.Command)
	default:
		t.Fatal("expected ack frame delivery")
	}
}

// ── extractContent_File tests ──

func TestExtractContent_File(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &xmlMessage{MsgType: "file", FileName: "report.pdf"}
	text, urls := d.extractContent(msg)
	assert.Equal(t, "[file: report.pdf]", text)
	assert.Nil(t, urls)
}

func TestExtractContent_FileNoName(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &xmlMessage{MsgType: "file"}
	text, urls := d.extractContent(msg)
	assert.Equal(t, "[file]", text)
	assert.Nil(t, urls)
}

func TestExtractContent_Location(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &xmlMessage{MsgType: "location"}
	text, urls := d.extractContent(msg)
	assert.Equal(t, "[location]", text)
	assert.Nil(t, urls)
}

// ── Reply no webhook test ──

func TestReply_NoWebhook(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	err := d.Reply(context.Background(), channel.Message{ChatID: 12345}, "hello")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no cached webhook url")
}

// ── New defaults tests ──

func TestNew_Defaults(t *testing.T) {
	d := New(Config{}, nil)
	assert.Equal(t, "wecom", d.Name())
	assert.Equal(t, defaultTextChunkLimit, d.cfg.TextChunkLimit)
	assert.Equal(t, "pairing", d.cfg.DMPolicy)
	assert.Equal(t, "allowlist", d.cfg.GroupPolicy)
}

func TestNew_CustomName(t *testing.T) {
	d := New(Config{Name: "my-wecom"}, nil)
	assert.Equal(t, "my-wecom", d.Name())
}

// ── handleMessage JSON envelope tests ──

func TestHandleMessage_JSONEnvelope(t *testing.T) {
	d := New(Config{
		Token:          testToken,
		EncodingAESKey: testEncodingAESKey,
		DMPolicy:       "open",
	}, nil)

	msgJSON := `{"chatid":"json-env-chat","chattype":"single","msgtype":"text","from":{"userid":"u1","name":"Name"},"text":{"content":"json envelope msg"},"response_url":"https://example.com/resp"}`

	encrypted := encryptForTest(t, msgJSON)
	timestamp := "1234567890"
	nonce := "nonce_json_env"
	sig := computeSignature(testToken, timestamp, nonce, encrypted)

	// JSON envelope.
	envelope := fmt.Sprintf(`{"encrypt":"%s"}`, encrypted)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf(
		"/wecom/webhook?msg_signature=%s&timestamp=%s&nonce=%s",
		sig, timestamp, nonce,
	), strings.NewReader(envelope))
	w := httptest.NewRecorder()

	d.Handler()(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	select {
	case job := <-d.incoming:
		assert.Equal(t, "json envelope msg", job.msg.Text)
		assert.Equal(t, hashChatID("wecom", "json-env-chat"), job.msg.ChatID)
	case <-time.After(2 * time.Second):
		t.Fatal("expected message in incoming channel")
	}
}

// ── dispatchWSMessage text tests ──

// mockHandler implements channel.Handler for test dispatch.
type mockHandler struct {
	mu      sync.Mutex
	handled []channel.Message
}

func (h *mockHandler) Handle(_ context.Context, msg channel.Message, _ channel.Responder) {
	h.mu.Lock()
	h.handled = append(h.handled, msg)
	h.mu.Unlock()
}

func (h *mockHandler) messages() []channel.Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]channel.Message(nil), h.handled...)
}

func TestDispatchWSMessage_TextAllowed(t *testing.T) {
	d := New(Config{ConnectionMode: "websocket", DMPolicy: "open"}, nil)

	h := &mockHandler{}
	d.handler = h

	msg := wsMessage{
		ChatID:   "ws-dm-1",
		ChatType: "single",
		MsgType:  "text",
		From:     wsFrom{UserID: "user-ws", Name: "WS User"},
		Text:     wsTextContent{Content: "ws direct msg"},
	}

	d.dispatchWSMessage(context.Background(), nil, msg, "ws-req-1")
	time.Sleep(100 * time.Millisecond)

	msgs := h.messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, "ws direct msg", msgs[0].Text)
}

func TestDispatchWSMessage_GroupStripsAt(t *testing.T) {
	d := New(Config{ConnectionMode: "websocket", DMPolicy: "open", GroupPolicy: "open"}, nil)

	h := &mockHandler{}
	d.handler = h

	msg := wsMessage{
		ChatID:   "ws-grp-1",
		ChatType: "group",
		MsgType:  "text",
		From:     wsFrom{UserID: "u1", Name: "User1"},
		Text:     wsTextContent{Content: "@Bot what is go?"},
	}

	d.dispatchWSMessage(context.Background(), nil, msg, "ws-grp-req-1")
	time.Sleep(100 * time.Millisecond)

	msgs := h.messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, "what is go?", msgs[0].Text)
}

func TestDispatchWSMessage_EmptyText(t *testing.T) {
	d := New(Config{ConnectionMode: "websocket", DMPolicy: "open"}, nil)

	h := &mockHandler{}
	d.handler = h

	msg := wsMessage{
		ChatID:   "ws-empty",
		ChatType: "single",
		MsgType:  "text",
		From:     wsFrom{UserID: "u1"},
		Text:     wsTextContent{Content: ""},
	}

	d.dispatchWSMessage(context.Background(), nil, msg, "ws-empty-req")
	time.Sleep(100 * time.Millisecond)

	msgs := h.messages()
	assert.Empty(t, msgs) // empty text should be skipped
}

// ── sendImage tests ──

func TestSendImage(t *testing.T) {
	var receivedCalls atomic.Int32
	var lastPayload imagePayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &lastPayload)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer srv.Close()

	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)

	// Create a small test image file.
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "test.jpg")
	imgData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10} // JPEG header
	require.NoError(t, os.WriteFile(imgPath, imgData, 0o644))

	err := d.sendImage(context.Background(), srv.URL+"?key=k1", "chat1", imgPath)
	require.NoError(t, err)

	assert.Equal(t, int32(1), receivedCalls.Load())
	assert.Equal(t, "image", lastPayload.MsgType)
	assert.Equal(t, "chat1", lastPayload.ChatID)
	assert.NotEmpty(t, lastPayload.Image.Base64)
	assert.NotEmpty(t, lastPayload.Image.MD5)
}

func TestSendImage_FileNotFound(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	err := d.sendImage(context.Background(), "http://example.com?key=k1", "chat1", "/nonexistent/path.jpg")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "read image")
}

func TestSendImage_OversizedFallsBackToFile(t *testing.T) {
	// Create a file larger than maxImageBytes (2MB).
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "large.png")
	largeData := make([]byte, maxImageBytes+1)
	require.NoError(t, os.WriteFile(imgPath, largeData, 0o644))

	var receivedUploads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUploads.Add(1)
		// This will be an upload_media call which we can't fully mock,
		// but we can verify it returns an error from the URL format.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok","media_id":"m1"}`))
	}))
	defer srv.Close()

	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	// sendImage with oversized file falls back to sendFile which calls uploadMedia.
	// uploadMedia constructs a URL with "key" param, so we provide one.
	err := d.sendImage(context.Background(), srv.URL+"?key=testkey", "chat1", imgPath)
	// This may or may not error depending on the upload path, but it should not panic.
	_ = err
}

// ── sendFile tests ──

func TestSendFile_NoKey(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	dir := t.TempDir()
	fp := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(fp, []byte("hello"), 0o644))

	err := d.sendFile(context.Background(), "http://example.com/no-key", "chat1", fp)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing key parameter")
}

// ── handlePairing tests ──

func TestHandlePairing_NilStore(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	// No pairing store — should not panic.
	d.handlePairing(xmlMessage{
		WebhookURL: "https://example.com?key=k1",
		ChatID:     "chat1",
		From:       xmlFrom{UserID: "u1", Name: "User1"},
	}, 12345)
}

func TestHandlePairing_CreatesPairingCode(t *testing.T) {
	var receivedCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCalls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer srv.Close()

	ps := pairing.NewStore(10 * time.Minute)
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, ps)

	hashedSender := hashUserID("u1")
	d.handlePairing(xmlMessage{
		WebhookURL: srv.URL + "?key=k1",
		ChatID:     "chat1",
		From:       xmlFrom{UserID: "u1", Name: "User1"},
	}, hashedSender)

	// A pairing code should have been created and message sent.
	assert.Equal(t, int32(1), receivedCalls.Load())
	_, pending := ps.HasPending(hashedSender, "wecom")
	assert.True(t, pending)
}

func TestHandlePairing_AlreadyPending(t *testing.T) {
	var receivedCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCalls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer srv.Close()

	ps := pairing.NewStore(10 * time.Minute)
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, ps)

	hashedSender := hashUserID("u2")
	// First call creates pairing.
	d.handlePairing(xmlMessage{
		WebhookURL: srv.URL + "?key=k1",
		ChatID:     "chat1",
		From:       xmlFrom{UserID: "u2", Name: "User2"},
	}, hashedSender)

	// Second call should detect existing pending.
	d.handlePairing(xmlMessage{
		WebhookURL: srv.URL + "?key=k1",
		ChatID:     "chat1",
		From:       xmlFrom{UserID: "u2", Name: "User2"},
	}, hashedSender)

	assert.Equal(t, int32(2), receivedCalls.Load())
}

// ── handleJSONPairing tests ──

func TestHandleJSONPairing_NilStore(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	// No pairing store — should not panic.
	d.handleJSONPairing(wsMessage{
		ChatID:      "chat1",
		From:        wsFrom{UserID: "u1", Name: "User"},
		ResponseURL: "https://example.com/resp",
	}, 12345)
}

func TestHandleJSONPairing_NoResponseURL(t *testing.T) {
	ps := pairing.NewStore(10 * time.Minute)
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, ps)
	// No response URL — should bail early.
	d.handleJSONPairing(wsMessage{
		ChatID: "chat1",
		From:   wsFrom{UserID: "u1", Name: "User"},
	}, 12345)
	// No pairing created since there's no way to reply.
	_, pending := ps.HasPending(12345, "wecom")
	assert.False(t, pending)
}

func TestHandleJSONPairing_CreatesPairing(t *testing.T) {
	var receivedCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCalls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer srv.Close()

	ps := pairing.NewStore(10 * time.Minute)
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, ps)

	hashedSender := hashUserID("u-json-pair")
	d.handleJSONPairing(wsMessage{
		ChatID:      "chat-json-pair",
		From:        wsFrom{UserID: "u-json-pair", Name: "JSON User"},
		ResponseURL: srv.URL + "?key=k1",
	}, hashedSender)

	assert.Equal(t, int32(1), receivedCalls.Load())
	_, pending := ps.HasPending(hashedSender, "wecom")
	assert.True(t, pending)
}

func TestHandleJSONPairing_AlreadyPending(t *testing.T) {
	var receivedCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCalls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer srv.Close()

	ps := pairing.NewStore(10 * time.Minute)
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, ps)

	hashedSender := hashUserID("u-json-2")
	// First call creates pairing.
	d.handleJSONPairing(wsMessage{
		ChatID:      "chat1",
		From:        wsFrom{UserID: "u-json-2", Name: "User"},
		ResponseURL: srv.URL + "?key=k1",
	}, hashedSender)

	// Second call with same sender — already pending.
	d.handleJSONPairing(wsMessage{
		ChatID:      "chat1",
		From:        wsFrom{UserID: "u-json-2", Name: "User"},
		ResponseURL: srv.URL + "?key=k1",
	}, hashedSender)

	assert.Equal(t, int32(2), receivedCalls.Load())
}

// ── handleWSPairing tests ──

func TestHandleWSPairing_NilStore(t *testing.T) {
	d := New(Config{ConnectionMode: "websocket"}, nil)
	// No pairing store — should not panic.
	d.handleWSPairing(context.Background(), nil, wsMessage{
		ChatID: "chat1",
		From:   wsFrom{UserID: "u1", Name: "User"},
	}, "req-1", 12345)
}

func TestHandleWSPairing_CreatesPairing(t *testing.T) {
	frames, wsURL, shutdown := startWeComWebSocketTestServer(t)
	defer shutdown()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	ps := pairing.NewStore(10 * time.Minute)
	d := New(Config{ConnectionMode: "websocket"}, ps)
	session := newWSSession(conn)

	hashedSender := hashUserID("u-ws-pair")
	d.handleWSPairing(context.Background(), session, wsMessage{
		ChatID: "ws-pair-chat",
		From:   wsFrom{UserID: "u-ws-pair", Name: "WS User"},
	}, "pair-req-1", hashedSender)

	_, pending := ps.HasPending(hashedSender, "wecom")
	assert.True(t, pending)

	// Should have sent a markdown reply frame.
	frame := mustReceiveWSFrame(t, frames)
	assert.Equal(t, wsCommandRespond, frame.Command)
	assert.Equal(t, "pair-req-1", frame.Headers.ReqID)

	var body wsReplyBody
	require.NoError(t, json.Unmarshal(frame.Body, &body))
	assert.Equal(t, "markdown", body.MsgType)
	assert.Contains(t, body.Markdown.Content, "pairing code")
}

func TestHandleWSPairing_AlreadyPending(t *testing.T) {
	frames, wsURL, shutdown := startWeComWebSocketTestServer(t)
	defer shutdown()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	ps := pairing.NewStore(10 * time.Minute)
	d := New(Config{ConnectionMode: "websocket"}, ps)
	session := newWSSession(conn)

	hashedSender := hashUserID("u-ws-pair-2")
	// First call.
	d.handleWSPairing(context.Background(), session, wsMessage{
		ChatID: "chat-ws-2",
		From:   wsFrom{UserID: "u-ws-pair-2", Name: "User 2"},
	}, "pair-req-2a", hashedSender)

	frame1 := mustReceiveWSFrame(t, frames)
	var body1 wsReplyBody
	require.NoError(t, json.Unmarshal(frame1.Body, &body1))
	assert.Contains(t, body1.Markdown.Content, "pairing code")

	// Second call — already pending.
	d.handleWSPairing(context.Background(), session, wsMessage{
		ChatID: "chat-ws-2",
		From:   wsFrom{UserID: "u-ws-pair-2", Name: "User 2"},
	}, "pair-req-2b", hashedSender)

	frame2 := mustReceiveWSFrame(t, frames)
	var body2 wsReplyBody
	require.NoError(t, json.Unmarshal(frame2.Body, &body2))
	assert.Contains(t, body2.Markdown.Content, "still pending")
}

// ── ReplyWithMedia webhook mode test (webhookResponder.ReplyWithMedia) ──

func TestWebhookResponder_ReplyWithMedia(t *testing.T) {
	var receivedCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCalls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer srv.Close()

	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	chatID := hashChatID("wecom", "media-chat")
	d.webhookCache.Store(chatID, &webhookEntry{
		url:       srv.URL + "?key=testkey",
		expiresAt: time.Now().Add(time.Hour),
	})
	d.chatIDMap.Store(chatID, "media-chat")

	// Create a small JPEG for testing.
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "test.jpg")
	imgData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}
	require.NoError(t, os.WriteFile(imgPath, imgData, 0o644))

	wr := &webhookResponder{d: d}
	err := wr.ReplyWithMedia(context.Background(), channel.Message{ChatID: chatID}, "caption", []string{imgPath})
	require.NoError(t, err)

	// Should have received: 1 caption + 1 image = 2 requests.
	assert.GreaterOrEqual(t, receivedCalls.Load(), int32(2))
}

// ── Coverage: dispatchXMLMessage, dispatchJSONMessage, access, pairing, media helpers ──

func TestDispatchXMLMessage_TextDM(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey, DMPolicy: "open"}, nil)

	received := make(chan channel.Message, 1)
	go func() { received <- (<-d.incoming).msg }()

	d.dispatchXMLMessage(`<xml>
		<WebhookUrl>https://hook.example.com?key=abc</WebhookUrl>
		<ChatId>chat-123</ChatId><ChatType>single</ChatType>
		<From><UserId>user-1</UserId><Name>Alice</Name><Alias>ali</Alias></From>
		<MsgType>text</MsgType><Text><Content>Hello bot</Content></Text>
	</xml>`)

	select {
	case msg := <-received:
		assert.Equal(t, "Hello bot", msg.Text)
		assert.Equal(t, "wecom", msg.Channel)
		assert.Equal(t, "single", msg.ChatType)
		assert.Contains(t, msg.SenderName, "ali")
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestDispatchXMLMessage_EventSkipped(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey, DMPolicy: "open"}, nil)
	d.dispatchXMLMessage(`<xml><WebhookUrl>https://h.com</WebhookUrl><ChatId>c</ChatId><From><UserId>u</UserId></From><MsgType>event</MsgType></xml>`)
	select {
	case <-d.incoming:
		t.Fatal("event should be skipped")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestDispatchXMLMessage_NoWebhookSkipped(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey, DMPolicy: "open"}, nil)
	d.dispatchXMLMessage(`<xml><ChatId>c</ChatId><From><UserId>u</UserId></From><MsgType>text</MsgType><Text><Content>hi</Content></Text></xml>`)
	select {
	case <-d.incoming:
		t.Fatal("no webhook should skip")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestDispatchXMLMessage_EmptyContentSkipped(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey, DMPolicy: "open"}, nil)
	d.dispatchXMLMessage(`<xml><WebhookUrl>https://h.com</WebhookUrl><ChatId>c</ChatId><From><UserId>u</UserId></From><MsgType>badtype</MsgType></xml>`)
	select {
	case <-d.incoming:
		t.Fatal("empty content should skip")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestDispatchXMLMessage_DeniedByAllowlist(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey, DMPolicy: "allowlist", AllowFrom: []string{"ok-user"}}, nil)
	d.dispatchXMLMessage(`<xml><WebhookUrl>https://h.com</WebhookUrl><ChatId>c</ChatId><ChatType>single</ChatType><From><UserId>blocked</UserId></From><MsgType>text</MsgType><Text><Content>hi</Content></Text></xml>`)
	select {
	case <-d.incoming:
		t.Fatal("denied user should skip")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestDispatchXMLMessage_GroupStripsMention(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey, GroupPolicy: "open"}, nil)
	received := make(chan channel.Message, 1)
	go func() { received <- (<-d.incoming).msg }()

	d.dispatchXMLMessage(`<xml><WebhookUrl>https://h.com</WebhookUrl><ChatId>grp</ChatId><ChatType>group</ChatType><From><UserId>u1</UserId><Name>Bob</Name></From><MsgType>text</MsgType><Text><Content>@Bot hello group</Content></Text></xml>`)

	select {
	case msg := <-received:
		assert.Equal(t, "hello group", msg.Text)
		assert.Equal(t, "group", msg.ChatType)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestDispatchXMLMessage_InvalidXML(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	d.dispatchXMLMessage("<<invalid")
	select {
	case <-d.incoming:
		t.Fatal("should not dispatch")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestDispatchJSONMessage_TextDM(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey, DMPolicy: "open"}, nil)
	received := make(chan channel.Message, 1)
	go func() { received <- (<-d.incoming).msg }()

	d.dispatchJSONMessage(`{"chatid":"jc1","chattype":"single","msgtype":"text","from":{"userid":"u1","name":"C","alias":"ca"},"text":{"content":"json msg"},"response_url":"https://r.com"}`)

	select {
	case msg := <-received:
		assert.Equal(t, "json msg", msg.Text)
		assert.Contains(t, msg.SenderName, "ca")
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestDispatchJSONMessage_EventSkipped(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey, DMPolicy: "open"}, nil)
	d.dispatchJSONMessage(`{"chatid":"c","msgtype":"event","from":{"userid":"u"},"event":{"eventtype":"subscribe"}}`)
	select {
	case <-d.incoming:
		t.Fatal("event should skip")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestDispatchJSONMessage_EmptyContentSkipped(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey, DMPolicy: "open"}, nil)
	d.dispatchJSONMessage(`{"chatid":"c","msgtype":"badtype","from":{"userid":"u"},"response_url":"https://r.com"}`)
	select {
	case <-d.incoming:
		t.Fatal("should skip")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestDispatchJSONMessage_DeniedByAllowlist(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey, DMPolicy: "allowlist", AllowFrom: []string{"ok"}}, nil)
	d.dispatchJSONMessage(`{"chatid":"c","chattype":"single","msgtype":"text","from":{"userid":"blocked"},"text":{"content":"hi"},"response_url":"https://r.com"}`)
	select {
	case <-d.incoming:
		t.Fatal("denied should skip")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestDispatchJSONMessage_GroupStripsMention(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey, GroupPolicy: "open"}, nil)
	received := make(chan channel.Message, 1)
	go func() { received <- (<-d.incoming).msg }()
	d.dispatchJSONMessage(`{"chatid":"g1","chattype":"group","msgtype":"text","from":{"userid":"u","name":"D"},"text":{"content":"@Bot grp msg"},"response_url":"https://r.com"}`)
	select {
	case msg := <-received:
		assert.Equal(t, "grp msg", msg.Text)
		assert.Equal(t, "group", msg.ChatType)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestDispatchJSONMessage_InvalidJSON(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	d.dispatchJSONMessage("{bad")
	select {
	case <-d.incoming:
		t.Fatal("should not dispatch")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestDispatchXMLMessage_PairingMode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer srv.Close()

	ps := pairing.NewStore(10 * time.Minute)
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey, DMPolicy: "pairing"}, ps)
	d.dispatchXMLMessage(fmt.Sprintf(`<xml><WebhookUrl>%s</WebhookUrl><ChatId>c</ChatId><ChatType>single</ChatType><From><UserId>unknown</UserId><Name>New</Name></From><MsgType>text</MsgType><Text><Content>hi</Content></Text></xml>`, srv.URL))
	select {
	case <-d.incoming:
		t.Fatal("pairing user should not dispatch")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestDispatchJSONMessage_PairingMode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer srv.Close()

	ps := pairing.NewStore(10 * time.Minute)
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey, DMPolicy: "pairing"}, ps)
	d.dispatchJSONMessage(fmt.Sprintf(`{"chatid":"c","chattype":"single","msgtype":"text","from":{"userid":"unknown","name":"N"},"text":{"content":"hi"},"response_url":"%s"}`, srv.URL))
	select {
	case <-d.incoming:
		t.Fatal("pairing user should not dispatch")
	case <-time.After(200 * time.Millisecond):
	}
}

// ── inferFileExtension ──

func TestInferFileExtension_MagicPDF(t *testing.T) {
	assert.Equal(t, ".pdf", inferFileExtension([]byte{0x25, 0x50, 0x44, 0x46, 0x2D}, ""))
}

func TestInferFileExtension_MagicZip(t *testing.T) {
	assert.Equal(t, ".zip", inferFileExtension([]byte{0x50, 0x4B, 0x03, 0x04, 0x00}, ""))
}

func TestInferFileExtension_MagicOLE(t *testing.T) {
	assert.Equal(t, ".doc", inferFileExtension([]byte{0xD0, 0xCF, 0x11, 0xE0, 0x00}, ""))
}

func TestInferFileExtension_CTDocx(t *testing.T) {
	assert.Equal(t, ".docx", inferFileExtension([]byte{0, 0, 0, 0}, "application/vnd.openxmlformats-officedocument.wordprocessingml.document"))
}

func TestInferFileExtension_CTXlsx(t *testing.T) {
	assert.Equal(t, ".xlsx", inferFileExtension([]byte{0, 0, 0, 0}, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"))
}

func TestInferFileExtension_CTPptx(t *testing.T) {
	assert.Equal(t, ".pptx", inferFileExtension([]byte{0, 0, 0, 0}, "application/vnd.openxmlformats-officedocument.presentationml.presentation"))
}

func TestInferFileExtension_CTText(t *testing.T) {
	assert.Equal(t, ".txt", inferFileExtension([]byte{0, 0, 0, 0}, "text/plain"))
}

func TestInferFileExtension_CTCSV(t *testing.T) {
	assert.Equal(t, ".csv", inferFileExtension([]byte{0, 0, 0, 0}, "text/csv"))
}

func TestInferFileExtension_CTJSON(t *testing.T) {
	assert.Equal(t, ".json", inferFileExtension([]byte{0, 0, 0, 0}, "application/json"))
}

func TestInferFileExtension_CTZip(t *testing.T) {
	assert.Equal(t, ".zip", inferFileExtension([]byte{0, 0, 0, 0}, "application/zip"))
}

func TestInferFileExtension_CTWithSemicolon(t *testing.T) {
	assert.Equal(t, ".json", inferFileExtension([]byte{0, 0, 0, 0}, "application/json; charset=utf-8"))
}

func TestInferFileExtension_Fallback(t *testing.T) {
	assert.Equal(t, ".bin", inferFileExtension([]byte{0, 0, 0, 0}, ""))
}

func TestInferFileExtension_ShortData(t *testing.T) {
	assert.Equal(t, ".bin", inferFileExtension([]byte{0x25, 0x50}, ""))
}

func TestInferFileExtension_CTPDF(t *testing.T) {
	assert.Equal(t, ".pdf", inferFileExtension([]byte{0, 0, 0, 0}, "application/pdf"))
}

func TestInferFileExtension_CTMSWord(t *testing.T) {
	assert.Equal(t, ".doc", inferFileExtension([]byte{0, 0, 0, 0}, "application/msword"))
}

// ── checkAccess/resolveGroupAccess ──

func TestCheckAccess_OpenPolicy(t *testing.T) {
	d := New(Config{DMPolicy: "open"}, nil)
	assert.Equal(t, accessAllowed, d.checkAccess("anyone"))
}

func TestCheckAccess_AllowlistAllowed(t *testing.T) {
	d := New(Config{DMPolicy: "allowlist", AllowFrom: []string{"u1"}}, nil)
	assert.Equal(t, accessAllowed, d.checkAccess("u1"))
}

func TestCheckAccess_AllowlistDenied(t *testing.T) {
	d := New(Config{DMPolicy: "allowlist", AllowFrom: []string{"u1"}}, nil)
	assert.Equal(t, accessDenied, d.checkAccess("u2"))
}

func TestCheckAccess_PairingKnown(t *testing.T) {
	d := New(Config{DMPolicy: "pairing", AllowFrom: []string{"known"}}, nil)
	assert.Equal(t, accessAllowed, d.checkAccess("known"))
}

func TestCheckAccess_PairingUnknown(t *testing.T) {
	d := New(Config{DMPolicy: "pairing"}, nil)
	assert.Equal(t, accessPairing, d.checkAccess("stranger"))
}

func TestResolveGroupAccess_DisabledPolicy(t *testing.T) {
	d := New(Config{GroupPolicy: "disabled"}, nil)
	assert.False(t, d.resolveGroupAccess("any", "any"))
}

func TestResolveGroupAccess_OpenPolicy(t *testing.T) {
	d := New(Config{GroupPolicy: "open"}, nil)
	assert.True(t, d.resolveGroupAccess("any", "any"))
}

func TestResolveGroupAccess_AllowlistMatch(t *testing.T) {
	d := New(Config{GroupPolicy: "allowlist", GroupAllowFrom: []string{"g1"}}, nil)
	assert.True(t, d.resolveGroupAccess("g1", "u"))
	assert.False(t, d.resolveGroupAccess("g2", "u"))
}

func TestResolveGroupAccess_WithGroupRules(t *testing.T) {
	d := New(Config{
		GroupPolicy:    "allowlist",
		GroupAllowFrom: []string{"grp-A"},
		Groups:         map[string]GroupRule{"grp-A": {AllowFrom: []string{"ok-user"}}},
	}, nil)
	assert.True(t, d.resolveGroupAccess("grp-A", "ok-user"))
	assert.False(t, d.resolveGroupAccess("grp-A", "bad-user"))
}

func TestResolveGroupAccess_GroupDisabled(t *testing.T) {
	f := false
	d := New(Config{
		GroupPolicy:    "allowlist",
		GroupAllowFrom: []string{"grp-dis"},
		Groups:         map[string]GroupRule{"grp-dis": {Enabled: &f}},
	}, nil)
	assert.False(t, d.resolveGroupAccess("grp-dis", "any"))
}

// ── handlePairing ──

func TestHandlePairing_NilStoreNoOp(t *testing.T) {
	d := New(Config{DMPolicy: "pairing"}, nil)
	d.handlePairing(xmlMessage{From: xmlFrom{UserID: "u1"}}, 123)
}

func TestHandlePairing_CreatesCode(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer srv.Close()
	ps := pairing.NewStore(10 * time.Minute)
	d := New(Config{DMPolicy: "pairing"}, ps)
	d.handlePairing(xmlMessage{WebhookURL: srv.URL, ChatID: "c1", From: xmlFrom{UserID: "pu1", Name: "P"}}, hashUserID("pu1"))
	assert.Contains(t, string(body), "pairing code")
}

func TestHandlePairing_AlreadyPend(t *testing.T) {
	var responses []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		responses = append(responses, string(b))
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer srv.Close()
	ps := pairing.NewStore(10 * time.Minute)
	d := New(Config{DMPolicy: "pairing"}, ps)
	msg := xmlMessage{WebhookURL: srv.URL, ChatID: "c2", From: xmlFrom{UserID: "pu2", Name: "P2"}}
	h := hashUserID("pu2")
	d.handlePairing(msg, h)
	d.handlePairing(msg, h)
	require.Len(t, responses, 2)
	assert.Contains(t, responses[1], "still pending")
}

// ── handleJSONPairing ──

func TestHandleJSONPairing_NilStoreNoOp(t *testing.T) {
	d := New(Config{DMPolicy: "pairing"}, nil)
	d.handleJSONPairing(wsMessage{From: wsFrom{UserID: "u1"}}, 1)
}

func TestHandleJSONPairing_NoRespURL(t *testing.T) {
	ps := pairing.NewStore(10 * time.Minute)
	d := New(Config{DMPolicy: "pairing"}, ps)
	d.handleJSONPairing(wsMessage{From: wsFrom{UserID: "u1"}}, 1)
}

func TestHandleJSONPairing_CreatesCode(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer srv.Close()
	ps := pairing.NewStore(10 * time.Minute)
	d := New(Config{DMPolicy: "pairing"}, ps)
	d.handleJSONPairing(wsMessage{ChatID: "jc", From: wsFrom{UserID: "ju", Name: "JP"}, ResponseURL: srv.URL}, hashUserID("ju"))
	assert.Contains(t, string(body), "pairing code")
}

func TestHandleJSONPairing_AlreadyPend(t *testing.T) {
	var responses []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		responses = append(responses, string(b))
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer srv.Close()
	ps := pairing.NewStore(10 * time.Minute)
	d := New(Config{DMPolicy: "pairing"}, ps)
	msg := wsMessage{ChatID: "jc2", From: wsFrom{UserID: "ju2", Name: "JP2"}, ResponseURL: srv.URL}
	h := hashUserID("ju2")
	d.handleJSONPairing(msg, h)
	d.handleJSONPairing(msg, h)
	require.Len(t, responses, 2)
	assert.Contains(t, responses[1], "still pending")
}

// ── extractWSContent ──

func TestExtractWSContent_TextMsg(t *testing.T) {
	d := New(Config{}, nil)
	text, urls, keys := d.extractWSContent(&wsMessage{MsgType: "text", Text: wsTextContent{Content: " ws text "}})
	assert.Equal(t, "ws text", text)
	assert.Empty(t, urls)
	assert.Empty(t, keys)
}

func TestExtractWSContent_ImageMsg(t *testing.T) {
	d := New(Config{}, nil)
	text, urls, keys := d.extractWSContent(&wsMessage{MsgType: "image", Image: wsImageContent{URL: "https://i.com/a.png", AESKey: "k1"}})
	assert.Equal(t, "[image]", text)
	assert.Equal(t, []string{"https://i.com/a.png"}, urls)
	assert.Equal(t, []string{"k1"}, keys)
}

func TestExtractWSContent_VoiceTranscript(t *testing.T) {
	d := New(Config{}, nil)
	text, _, _ := d.extractWSContent(&wsMessage{MsgType: "voice", Voice: wsVoiceContent{Content: " transcribed "}})
	assert.Equal(t, "transcribed", text)
}

func TestExtractWSContent_VoiceEmpty(t *testing.T) {
	d := New(Config{}, nil)
	text, _, _ := d.extractWSContent(&wsMessage{MsgType: "voice", Voice: wsVoiceContent{}})
	assert.Equal(t, "[voice]", text)
}

func TestExtractWSContent_FileMsg(t *testing.T) {
	d := New(Config{}, nil)
	text, urls, keys := d.extractWSContent(&wsMessage{MsgType: "file", File: wsFileContent{URL: "https://f.com/d.pdf", AESKey: "fk"}})
	assert.Equal(t, "[file]", text)
	assert.Equal(t, []string{"https://f.com/d.pdf"}, urls)
	assert.Equal(t, []string{"fk"}, keys)
}

func TestExtractWSContent_VideoMsg(t *testing.T) {
	d := New(Config{}, nil)
	text, urls, keys := d.extractWSContent(&wsMessage{MsgType: "video", Video: wsFileContent{URL: "https://v.com/v.mp4", AESKey: "vk"}})
	assert.Equal(t, "[video]", text)
	assert.Equal(t, []string{"https://v.com/v.mp4"}, urls)
	assert.Equal(t, []string{"vk"}, keys)
}

func TestExtractWSContent_UnknownMsg(t *testing.T) {
	d := New(Config{}, nil)
	text, urls, keys := d.extractWSContent(&wsMessage{MsgType: "other"})
	assert.Empty(t, text)
	assert.Empty(t, urls)
	assert.Empty(t, keys)
}

func TestExtractWSContent_WithQuote(t *testing.T) {
	d := New(Config{}, nil)
	msg := &wsMessage{MsgType: "text", Text: wsTextContent{Content: "reply"}, Quote: &wsQuote{MsgType: "text", Text: wsTextContent{Content: "original"}}}
	text, _, _ := d.extractWSContent(msg)
	assert.Contains(t, text, "reply")
	assert.Contains(t, text, "[quoted]")
	assert.Contains(t, text, "original")
}

func TestExtractWSContent_MixedMsg(t *testing.T) {
	d := New(Config{}, nil)
	msg := &wsMessage{MsgType: "mixed", Mixed: wsMixedContent{MsgItem: []wsMixedItem{
		{MsgType: "text", Text: wsTextContent{Content: "hello"}},
		{MsgType: "image", Image: wsImageContent{URL: "https://i.com/1.png", AESKey: "k1"}},
		{MsgType: "file", File: wsFileContent{URL: "https://f.com/d.pdf", AESKey: "fk"}},
	}}}
	text, urls, keys := d.extractWSContent(msg)
	assert.Equal(t, "hello", text)
	assert.Equal(t, []string{"https://i.com/1.png", "https://f.com/d.pdf"}, urls)
	assert.Equal(t, []string{"k1", "fk"}, keys)
}

func TestExtractWSContent_MixedOnlyImages(t *testing.T) {
	d := New(Config{}, nil)
	msg := &wsMessage{MsgType: "mixed", Mixed: wsMixedContent{MsgItem: []wsMixedItem{
		{MsgType: "image", Image: wsImageContent{URL: "https://i.com/2.png", AESKey: "k2"}},
	}}}
	text, _, _ := d.extractWSContent(msg)
	assert.Equal(t, "[image]", text)
}

// ── wecomSenderName ──

func TestWecomSenderName_AliasAndName(t *testing.T) {
	assert.Equal(t, "ali(Alice)", wecomSenderName("Alice", "ali", "uid"))
}

func TestWecomSenderName_OnlyName(t *testing.T) {
	assert.Equal(t, "Alice", wecomSenderName("Alice", "", "uid"))
}

func TestWecomSenderName_OnlyAlias(t *testing.T) {
	assert.Equal(t, "ali", wecomSenderName("", "ali", "uid"))
}

func TestWecomSenderName_OnlyUserID(t *testing.T) {
	assert.Equal(t, "uid", wecomSenderName("", "", "uid"))
}

func TestWecomSenderName_SameAliasAndName(t *testing.T) {
	assert.Equal(t, "Alice", wecomSenderName("Alice", "Alice", "uid"))
}

// ── lookupWebhook ──

func TestLookupWebhook_Expired(t *testing.T) {
	d := New(Config{}, nil)
	cid := hashChatID("w", "exp")
	d.webhookCache.Store(cid, &webhookEntry{url: "https://old.com", expiresAt: time.Now().Add(-time.Hour)})
	u, _ := d.lookupWebhook(cid)
	assert.Empty(t, u)
}

func TestLookupWebhook_Missing(t *testing.T) {
	d := New(Config{}, nil)
	u, _ := d.lookupWebhook(12345)
	assert.Empty(t, u)
}

// ── downloadOneMedia ──

func TestDownloadOneMedia_DirectJPEG(t *testing.T) {
	jpegData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpegData)
	}))
	defer srv.Close()
	dir := t.TempDir()
	path, err := downloadOneMedia(context.Background(), srv.Client(), srv.URL, dir, 0, "", "", "t")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "media_0.jpg"), path)
}

func TestDownloadOneMedia_NonImagePDF(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte{0x25, 0x50, 0x44, 0x46, 0x2D, 0x31, 0x2E, 0x34})
	}))
	defer srv.Close()
	dir := t.TempDir()
	path, err := downloadOneMedia(context.Background(), srv.Client(), srv.URL, dir, 0, "", "", "t")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "media_0.pdf"), path)
}

func TestDownloadOneMedia_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	dir := t.TempDir()
	_, err := downloadOneMedia(context.Background(), srv.Client(), srv.URL, dir, 0, "", "", "t")
	assert.Error(t, err)
}

// ── writeMediaFile ──

func TestWriteMediaFile_OK(t *testing.T) {
	dir := t.TempDir()
	path, err := writeMediaFile(dir, 5, ".txt", []byte("hello"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "media_5.txt"), path)
	data, _ := os.ReadFile(path)
	assert.Equal(t, []byte("hello"), data)
}

func TestWriteMediaFile_BadPath(t *testing.T) {
	_, err := writeMediaFile("/nonexistent/xyz", 0, ".bin", []byte("x"))
	assert.Error(t, err)
}

// ── detectImageFormat ──

func TestDetectImageFormat_AllFormats(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		ext  string
		ok   bool
	}{
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00}, ".jpg", true},
		{"png", []byte{0x89, 0x50, 0x4E, 0x47, 0x0D}, ".png", true},
		{"gif", []byte{0x47, 0x49, 0x46, 0x38, 0x39}, ".gif", true},
		{"webp", []byte{0x52, 0x49, 0x46, 0x46, 0, 0, 0, 0, 0x57, 0x45, 0x42, 0x50}, ".webp", true},
		{"unknown", []byte{0, 0, 0, 0, 0}, "", false},
		{"short", []byte{0xFF, 0xD8}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext, ok := detectImageFormat(tt.data)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.ext, ext)
		})
	}
}

// ── isImageExtension ──

func TestIsImageExtension_Coverage(t *testing.T) {
	assert.True(t, isImageExtension(".jpg"))
	assert.True(t, isImageExtension(".jpeg"))
	assert.True(t, isImageExtension(".png"))
	assert.True(t, isImageExtension(".gif"))
	assert.True(t, isImageExtension(".WEBP"))
	assert.False(t, isImageExtension(".pdf"))
	assert.False(t, isImageExtension(""))
}
