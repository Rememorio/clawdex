package weixin

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Rememorio/clawdex/internal/logger"
)

const (
	defaultHTTPTimeout   = 30 * time.Second
	longPollExtraTimeout = 10 * time.Second
	maxResponseBytes     = 10 * 1024 * 1024 // 10MB
	uploadMediaTypeImage = 1
	uploadMediaTypeVideo = 2
	uploadMediaTypeFile  = 3
	uploadMediaTypeVoice = 4
)

// apiClient wraps HTTP calls to the iLink bot API.
type apiClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
	info       baseInfo
}

// newAPIClient creates an API client for the given endpoint and token.
func newAPIClient(baseURL, token string) *apiClient {
	return &apiClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		httpClient: &http.Client{
			Timeout: defaultHTTPTimeout,
		},
		info: baseInfo{
			ChannelVersion: "clawdex/1.0",
			BotAgent:       "Clawdex",
		},
	}
}

// do performs a POST request to the given path with JSON body and decodes the response.
func (c *apiClient) do(ctx context.Context, path string, req, resp any, timeout time.Duration) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := c.baseURL + "/" + path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("AuthorizationType", "ilink_bot_token")
	httpReq.Header.Set("Authorization", "Bearer "+c.token)

	client := c.httpClient
	if timeout > 0 {
		client = &http.Client{Timeout: timeout}
	}

	httpResp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d: %s", httpResp.StatusCode, string(respBody))
	}

	if resp != nil {
		if err := json.Unmarshal(respBody, resp); err != nil {
			return fmt.Errorf("unmarshal response: %w", err)
		}
	}
	return nil
}

// getUpdates performs a long-poll for new messages.
func (c *apiClient) getUpdates(ctx context.Context, buf string, timeoutMs int64) (*getUpdatesResp, error) {
	req := getUpdatesReq{
		BaseInfo:      &c.info,
		GetUpdatesBuf: buf,
	}
	var resp getUpdatesResp
	// Long-poll timeout: server holds up to timeoutMs, add extra for network.
	httpTimeout := time.Duration(timeoutMs)*time.Millisecond + longPollExtraTimeout
	if err := c.do(ctx, "ilink/bot/getupdates", req, &resp, httpTimeout); err != nil {
		return nil, err
	}
	return &resp, nil
}

// sendMessage sends a text or media message to a user.
func (c *apiClient) sendMessage(ctx context.Context, msg *weixinMessage) error {
	req := sendMessageReq{
		BaseInfo: &c.info,
		Msg:      msg,
	}
	var resp sendMessageResp
	if err := c.do(ctx, "ilink/bot/sendmessage", req, &resp, 0); err != nil {
		return err
	}
	if resp.Ret != 0 {
		return fmt.Errorf("sendmessage ret=%d: %s", resp.Ret, resp.ErrMsg)
	}
	return nil
}

// sendTyping sends a typing indicator for the given user.
func (c *apiClient) sendTyping(ctx context.Context, userID, ticket string) error {
	req := sendTypingReq{
		BaseInfo:     &c.info,
		IlinkUserID:  userID,
		TypingTicket: ticket,
		Status:       1, // typing
	}
	var resp sendTypingResp
	if err := c.do(ctx, "ilink/bot/sendtyping", req, &resp, 0); err != nil {
		return err
	}
	return nil
}

// sendTypingCancel sends a cancel-typing signal for the given user.
func (c *apiClient) sendTypingCancel(ctx context.Context, userID, ticket string) error {
	req := sendTypingReq{
		BaseInfo:     &c.info,
		IlinkUserID:  userID,
		TypingTicket: ticket,
		Status:       2, // cancel
	}
	var resp sendTypingResp
	return c.do(ctx, "ilink/bot/sendtyping", req, &resp, 0)
}

// getConfig fetches bot config (including typing_ticket) for a user.
func (c *apiClient) getConfig(ctx context.Context, userID, contextToken string) (*getConfigResp, error) {
	req := struct {
		BaseInfo     *baseInfo `json:"base_info,omitempty"`
		IlinkUserID  string    `json:"ilink_user_id,omitempty"`
		ContextToken string    `json:"context_token,omitempty"`
	}{
		BaseInfo:     &c.info,
		IlinkUserID:  userID,
		ContextToken: contextToken,
	}
	var resp getConfigResp
	if err := c.do(ctx, "ilink/bot/getconfig", req, &resp, 0); err != nil {
		return nil, err
	}
	return &resp, nil
}

// notifyStart tells the server this channel client is starting.
func (c *apiClient) notifyStart(ctx context.Context) error {
	req := notifyReq{BaseInfo: &c.info}
	var resp notifyResp
	return c.do(ctx, "ilink/bot/msg/notifystart", req, &resp, 0)
}

// notifyStop tells the server this channel client is stopping.
func (c *apiClient) notifyStop(ctx context.Context) error {
	req := notifyReq{BaseInfo: &c.info}
	var resp notifyResp
	return c.do(ctx, "ilink/bot/msg/notifystop", req, &resp, 0)
}

// getUploadURL requests a pre-signed CDN upload URL for a media file.
func (c *apiClient) getUploadURL(ctx context.Context, req *getUploadURLReq) (*getUploadURLResp, error) {
	req.BaseInfo = &c.info
	var resp getUploadURLResp
	if err := c.do(ctx, "ilink/bot/getuploadurl", req, &resp, 0); err != nil {
		return nil, err
	}
	if resp.Ret != 0 {
		return nil, fmt.Errorf("getuploadurl ret=%d: %s", resp.Ret, resp.ErrMsg)
	}
	return &resp, nil
}

// uploadToCDN uploads encrypted data to the CDN URL.
func (c *apiClient) uploadToCDN(ctx context.Context, uploadURL string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create upload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("upload http %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// downloadMedia downloads and decrypts a media file from CDN.
// Returns the local file path where the decrypted content is saved.
func (c *apiClient) downloadMedia(ctx context.Context, media *cdnMedia, aesKeyHex, destDir, filename string) (string, error) {
	if media == nil || media.FullURL == "" {
		return "", fmt.Errorf("no media URL")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, media.FullURL, nil)
	if err != nil {
		return "", fmt.Errorf("create download request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download http %d", resp.StatusCode)
	}

	ciphertext, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("read download body: %w", err)
	}

	// Decrypt if AES key is provided.
	var plaintext []byte
	if aesKeyHex != "" {
		keyBytes, err := hex.DecodeString(aesKeyHex)
		if err != nil {
			return "", fmt.Errorf("decode aes key hex: %w", err)
		}
		plaintext, err = aesECBDecrypt(ciphertext, keyBytes)
		if err != nil {
			// Fallback: use raw data if decryption fails (some media may not be encrypted).
			logger.Warn("weixin media decryption failed, using raw data", "error", err)
			plaintext = ciphertext
		}
	} else {
		plaintext = ciphertext
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}

	path := filepath.Join(destDir, filename)
	if err := os.WriteFile(path, plaintext, 0o644); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}
	return path, nil
}

// uploadMedia encrypts and uploads a local file to CDN, returning the
// encrypt_query_param to use in a sendMessage media item.
func (c *apiClient) uploadMedia(ctx context.Context, filePath, toUserID string, mediaType int) (string, string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", "", fmt.Errorf("read file: %w", err)
	}

	aesKey, err := generateAESKey()
	if err != nil {
		return "", "", err
	}
	fileKey, err := generateFileKey()
	if err != nil {
		return "", "", err
	}

	rawMD5 := fileMD5(data)
	encSize := aesEncryptedSize(int64(len(data)))

	uploadReq := &getUploadURLReq{
		FileKey:     fileKey,
		MediaType:   mediaType,
		ToUserID:    toUserID,
		RawSize:     int64(len(data)),
		RawFileMD5:  rawMD5,
		FileSize:    encSize,
		NoNeedThumb: true,
		AESKeyStr:   hex.EncodeToString(aesKey),
	}

	uploadResp, err := c.getUploadURL(ctx, uploadReq)
	if err != nil {
		return "", "", err
	}

	ciphertext, err := aesECBEncrypt(data, aesKey)
	if err != nil {
		return "", "", fmt.Errorf("encrypt file: %w", err)
	}

	uploadURL := uploadResp.UploadFullURL
	if uploadURL == "" {
		return "", "", fmt.Errorf("no upload URL returned")
	}

	if err := c.uploadToCDN(ctx, uploadURL, ciphertext); err != nil {
		return "", "", err
	}

	return uploadResp.UploadParam, hex.EncodeToString(aesKey), nil
}
