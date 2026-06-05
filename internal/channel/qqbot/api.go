package qqbot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Rememorio/clawdex/internal/logger"
)

const (
	tokenURL = "https://bots.qq.com/app/getAppAccessToken"
	apiBase  = "https://api.sgroup.qq.com"

	httpTimeout     = 30 * time.Second
	tokenRefreshGap = 60 * time.Second // refresh token 60s before expiry
)

// apiClient manages authentication and REST calls to the QQ Bot API.
type apiClient struct {
	appID        string
	clientSecret string
	httpClient   *http.Client

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

func newAPIClient(appID, clientSecret string) *apiClient {
	return &apiClient{
		appID:        appID,
		clientSecret: clientSecret,
		httpClient:   &http.Client{Timeout: httpTimeout},
	}
}

// getAccessToken returns a valid access token, refreshing if necessary.
func (c *apiClient) getAccessToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && time.Now().Before(c.expiresAt) {
		return c.token, nil
	}

	body, _ := json.Marshal(map[string]string{
		"appId":        c.appID,
		"clientSecret": c.clientSecret,
	})

	resp, err := c.httpClient.Post(tokenURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("qqbot: token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("qqbot: token request returned %d: %s", resp.StatusCode, string(raw))
	}

	var tokenResp tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("qqbot: decode token response: %w", err)
	}

	expiresIn, _ := strconv.Atoi(tokenResp.ExpiresIn)
	if expiresIn <= 0 {
		expiresIn = 7200
	}

	c.token = tokenResp.AccessToken
	c.expiresAt = time.Now().Add(time.Duration(expiresIn)*time.Second - tokenRefreshGap)
	return c.token, nil
}

// getGatewayURL retrieves the WebSocket gateway URL.
func (c *apiClient) getGatewayURL() (string, error) {
	token, err := c.getAccessToken()
	if err != nil {
		return "", err
	}

	req, _ := http.NewRequest("GET", apiBase+"/gateway", nil)
	req.Header.Set("Authorization", "QQBot "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("qqbot: gateway request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("qqbot: gateway returned %d: %s", resp.StatusCode, string(raw))
	}

	var gwResp gatewayResponse
	if err := json.NewDecoder(resp.Body).Decode(&gwResp); err != nil {
		return "", fmt.Errorf("qqbot: decode gateway response: %w", err)
	}
	return gwResp.URL, nil
}

// sendC2CMessage sends a text message to a C2C (DM) user.
func (c *apiClient) sendC2CMessage(openID, content, msgID string, msgSeq int) error {
	path := fmt.Sprintf("/v2/users/%s/messages", openID)
	body := messageBody{
		Content: content,
		MsgType: 0, // text
		MsgID:   msgID,
		MsgSeq:  msgSeq,
	}
	return c.postMessage(path, body)
}

// sendGroupMessage sends a text message to a group.
func (c *apiClient) sendGroupMessage(groupOpenID, content, msgID string, msgSeq int) error {
	path := fmt.Sprintf("/v2/groups/%s/messages", groupOpenID)
	body := messageBody{
		Content: content,
		MsgType: 0, // text
		MsgID:   msgID,
		MsgSeq:  msgSeq,
	}
	return c.postMessage(path, body)
}

// sendC2CMedia uploads and sends a media message to a C2C user.
func (c *apiClient) sendC2CMedia(openID, filePath, msgID string, msgSeq int) error {
	fileInfo, err := c.uploadMedia(fmt.Sprintf("/v2/users/%s/files", openID), filePath)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/v2/users/%s/messages", openID)
	return c.postMediaMessage(path, fileInfo, msgID, msgSeq)
}

// sendGroupMedia uploads and sends a media message to a group.
func (c *apiClient) sendGroupMedia(groupOpenID, filePath, msgID string, msgSeq int) error {
	fileInfo, err := c.uploadMedia(fmt.Sprintf("/v2/groups/%s/files", groupOpenID), filePath)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/v2/groups/%s/messages", groupOpenID)
	return c.postMediaMessage(path, fileInfo, msgID, msgSeq)
}

// sendC2CInputNotify sends a typing indicator to a C2C user.
func (c *apiClient) sendC2CInputNotify(openID, msgID string) error {
	token, err := c.getAccessToken()
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/v2/users/%s/messages", openID)
	body := map[string]any{
		"msg_type": 6,
		"input_notify": map[string]any{
			"input_type":   1,
			"input_second": 60,
		},
		"msg_seq": 1,
	}
	if msgID != "" {
		body["msg_id"] = msgID
	}
	return c.doPost(token, path, body)
}

// postMessage sends a text message body to the given path.
func (c *apiClient) postMessage(path string, body messageBody) error {
	token, err := c.getAccessToken()
	if err != nil {
		return err
	}
	return c.doPost(token, path, body)
}

// postMediaMessage sends a media message body.
func (c *apiClient) postMediaMessage(path, fileInfo, msgID string, msgSeq int) error {
	token, err := c.getAccessToken()
	if err != nil {
		return err
	}
	body := mediaMessageBody{
		MsgType: 7, // rich media
		MsgID:   msgID,
		MsgSeq:  msgSeq,
		Media:   mediaInfo{FileInfo: fileInfo},
	}
	return c.doPost(token, path, body)
}

// uploadMedia uploads a file and returns the file_info string.
func (c *apiClient) uploadMedia(path, filePath string) (string, error) {
	token, err := c.getAccessToken()
	if err != nil {
		return "", err
	}

	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("qqbot: open media file: %w", err)
	}
	defer file.Close()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// file_type: 1=image, 2=video, 3=audio, 4=file
	fileType := resolveFileType(filePath)
	_ = writer.WriteField("file_type", strconv.Itoa(fileType))
	_ = writer.WriteField("srv_send_msg", "false")

	part, err := writer.CreateFormFile("file_data", filepath.Base(filePath))
	if err != nil {
		return "", fmt.Errorf("qqbot: create form file: %w", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		return "", fmt.Errorf("qqbot: copy file data: %w", err)
	}
	writer.Close()

	req, _ := http.NewRequest("POST", apiBase+path, &buf)
	req.Header.Set("Authorization", "QQBot "+token)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("qqbot: upload media request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("qqbot: upload media returned %d: %s", resp.StatusCode, string(raw))
	}

	var uploadResp mediaUploadResponse
	if err := json.NewDecoder(resp.Body).Decode(&uploadResp); err != nil {
		return "", fmt.Errorf("qqbot: decode upload response: %w", err)
	}
	return uploadResp.FileInfo, nil
}

// doPost sends a JSON POST request. On 401 it clears the token cache and
// retries once with a fresh token.
func (c *apiClient) doPost(token, path string, body any) error {
	err := c.doPostOnce(token, path, body)
	if err == nil {
		return nil
	}
	// Retry on 401: token may have expired between getAccessToken and the request.
	if isUnauthorized(err) {
		c.clearToken()
		newToken, tokenErr := c.getAccessToken()
		if tokenErr != nil {
			return err // return original error
		}
		return c.doPostOnce(newToken, path, body)
	}
	return err
}

// doPostOnce performs a single JSON POST request without retry.
func (c *apiClient) doPostOnce(token, path string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("qqbot: marshal request: %w", err)
	}

	req, _ := http.NewRequest("POST", apiBase+path, bytes.NewReader(data))
	req.Header.Set("Authorization", "QQBot "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("qqbot: api request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		logger.Warn("qqbot api error", "path", path, "status", resp.StatusCode, "body", string(raw))
		return &apiError{status: resp.StatusCode, body: string(raw)}
	}
	return nil
}

// clearToken forces the next getAccessToken call to fetch a fresh token.
func (c *apiClient) clearToken() {
	c.mu.Lock()
	c.token = ""
	c.expiresAt = time.Time{}
	c.mu.Unlock()
}

// apiError carries an HTTP status code for retry decisions.
type apiError struct {
	status int
	body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("qqbot: api returned %d: %s", e.status, e.body)
}

// isUnauthorized checks if an error is a 401 response.
func isUnauthorized(err error) bool {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.status == http.StatusUnauthorized
	}
	return false
}

// downloadAttachment downloads an attachment URL to a temp file and returns the path.
func (c *apiClient) downloadAttachment(url, tmpDir string) (string, error) {
	resp, err := c.httpClient.Get(url)
	if err != nil {
		return "", fmt.Errorf("qqbot: download attachment: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("qqbot: download returned %d", resp.StatusCode)
	}

	ext := ".bin"
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		if exts, _ := mime.ExtensionsByType(ct); len(exts) > 0 {
			ext = exts[0]
		}
	}

	f, err := os.CreateTemp(tmpDir, "qqbot-media-*"+ext)
	if err != nil {
		return "", fmt.Errorf("qqbot: create temp file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("qqbot: write attachment: %w", err)
	}
	return f.Name(), nil
}

// resolveFileType determines the QQ file_type from file extension.
func resolveFileType(path string) int {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp":
		return 1 // image
	case ".mp4", ".mov", ".avi", ".mkv", ".webm":
		return 2 // video
	case ".mp3", ".wav", ".ogg", ".flac", ".aac", ".silk", ".amr":
		return 3 // audio
	default:
		return 4 // file
	}
}
