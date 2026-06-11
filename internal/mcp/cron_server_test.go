package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func frameMessage(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(data), data)
}

func readResponse(t *testing.T, out *bytes.Buffer) rpcResponse {
	t.Helper()
	data, err := readMCPMessage(bufio.NewReader(out))
	require.NoError(t, err)
	var resp rpcResponse
	require.NoError(t, json.Unmarshal(data, &resp))
	return resp
}

func TestServeToolsList(t *testing.T) {
	var out bytes.Buffer
	in := bytes.NewBufferString(frameMessage(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	}))
	s := NewCronServer(in, &out)

	require.NoError(t, s.Serve(context.Background()))
	resp := readResponse(t, &out)
	require.Nil(t, resp.Error)
	result := resp.Result.(map[string]any)
	tools := result["tools"].([]any)
	require.Len(t, tools, 1)
	tool := tools[0].(map[string]any)
	assert.Equal(t, cronToolName, tool["name"])
	assert.Contains(t, tool["description"], "scheduled jobs")
}

func TestServeToolsListNotifyMode(t *testing.T) {
	t.Setenv("CLAWDEX_MCP_TOOLS", "notify")
	var out bytes.Buffer
	in := bytes.NewBufferString(frameMessage(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	}))
	s := NewCronServer(in, &out)

	require.NoError(t, s.Serve(context.Background()))
	resp := readResponse(t, &out)
	require.Nil(t, resp.Error)
	result := resp.Result.(map[string]any)
	tools := result["tools"].([]any)
	require.Len(t, tools, 1)
	tool := tools[0].(map[string]any)
	assert.Equal(t, notifyToolName, tool["name"])
	assert.Contains(t, tool["description"], "proactive message")
}

func TestServeResourcesList(t *testing.T) {
	var out bytes.Buffer
	in := bytes.NewBufferString(frameMessage(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      "resources",
		"method":  "resources/list",
	}))
	s := NewCronServer(in, &out)

	require.NoError(t, s.Serve(context.Background()))
	resp := readResponse(t, &out)
	require.Nil(t, resp.Error)
	result := resp.Result.(map[string]any)
	resources := result["resources"].([]any)
	assert.Empty(t, resources)
}

func TestToolCallForwardsToGateway(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/cron/tool", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "list", body["action"])
		assert.Equal(t, "token-123", body["token"])
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"jobs": []any{}},
		})
	}))
	defer gateway.Close()

	t.Setenv("CLAWDEX_GATEWAY_URL", gateway.URL)
	t.Setenv("CLAWDEX_CRON_CONTEXT_TOKEN", "token-123")

	var out bytes.Buffer
	in := bytes.NewBufferString(frameMessage(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      7,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      cronToolName,
			"arguments": map[string]any{"action": "list"},
		},
	}))
	s := NewCronServer(in, &out)
	s.httpClient = gateway.Client()

	require.NoError(t, s.Serve(context.Background()))
	resp := readResponse(t, &out)
	require.Nil(t, resp.Error)
	result := resp.Result.(map[string]any)
	content := result["content"].([]any)
	require.Len(t, content, 1)
	text := content[0].(map[string]any)["text"].(string)
	assert.Contains(t, text, `"jobs": []`)
}

func TestNotifyToolCallForwardsToGateway(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/cron/tool", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "notify", body["action"])
		assert.Equal(t, "token-123", body["token"])
		assert.Equal(t, "Batch 1", body["title"])
		assert.Equal(t, "hello", body["text"])
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"delivered": true},
		})
	}))
	defer gateway.Close()

	t.Setenv("CLAWDEX_GATEWAY_URL", gateway.URL)
	t.Setenv("CLAWDEX_CRON_CONTEXT_TOKEN", "token-123")
	t.Setenv("CLAWDEX_MCP_TOOLS", "notify")

	var out bytes.Buffer
	in := bytes.NewBufferString(frameMessage(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      7,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      notifyToolName,
			"arguments": map[string]any{"title": "Batch 1", "text": "hello"},
		},
	}))
	s := NewCronServer(in, &out)
	s.httpClient = gateway.Client()

	require.NoError(t, s.Serve(context.Background()))
	resp := readResponse(t, &out)
	require.Nil(t, resp.Error)
	result := resp.Result.(map[string]any)
	content := result["content"].([]any)
	require.Len(t, content, 1)
	text := content[0].(map[string]any)["text"].(string)
	assert.Contains(t, text, `"delivered": true`)
}

func TestToolCallRequiresContextToken(t *testing.T) {
	t.Setenv("CLAWDEX_CRON_CONTEXT_TOKEN", "")

	var out bytes.Buffer
	in := bytes.NewBufferString(frameMessage(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      8,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      cronToolName,
			"arguments": map[string]any{"action": "list"},
		},
	}))
	s := NewCronServer(in, &out)

	require.NoError(t, s.Serve(context.Background()))
	resp := readResponse(t, &out)
	require.Nil(t, resp.Error)
	result := resp.Result.(map[string]any)
	assert.Equal(t, true, result["isError"])
	content := result["content"].([]any)
	assert.Contains(t, content[0].(map[string]any)["text"], "missing CLAWDEX_CRON_CONTEXT_TOKEN")
}

func TestHandleInitializePingAndPrompts(t *testing.T) {
	s := NewCronServer(bytes.NewReader(nil), ioDiscard{})

	result, err := s.handle(context.Background(), rpcRequest{Method: "initialize"})
	require.NoError(t, err)
	init := result.(map[string]any)
	assert.Equal(t, "2024-11-05", init["protocolVersion"])

	result, err = s.handle(context.Background(), rpcRequest{Method: "ping"})
	require.NoError(t, err)
	assert.Empty(t, result.(map[string]any))

	result, err = s.handle(context.Background(), rpcRequest{Method: "prompts/list"})
	require.NoError(t, err)
	prompts := result.(map[string]any)["prompts"].([]any)
	assert.Empty(t, prompts)
}

func TestHandleUnsupportedMethod(t *testing.T) {
	s := NewCronServer(bytes.NewReader(nil), ioDiscard{})
	_, err := s.handle(context.Background(), rpcRequest{Method: "unknown"})
	assert.ErrorContains(t, err, "unsupported MCP method")
}

func TestHandleToolCallUnknownTool(t *testing.T) {
	s := NewCronServer(bytes.NewReader(nil), ioDiscard{})
	params, err := json.Marshal(map[string]any{"name": "other"})
	require.NoError(t, err)

	result, err := s.handleToolCall(context.Background(), params)
	require.NoError(t, err)
	out := result.(map[string]any)
	assert.Equal(t, true, out["isError"])
	content := out["content"].([]map[string]string)
	assert.Contains(t, content[0]["text"], "unknown tool")
}

func TestHandleToolCallInvalidParams(t *testing.T) {
	s := NewCronServer(bytes.NewReader(nil), ioDiscard{})
	_, err := s.handleToolCall(context.Background(), json.RawMessage("{"))
	assert.Error(t, err)
}

func TestToolCallReturnsGatewayErrorAsToolError(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": "bad cron request",
		})
	}))
	defer gateway.Close()

	t.Setenv("CLAWDEX_GATEWAY_URL", gateway.URL)
	t.Setenv("CLAWDEX_CRON_CONTEXT_TOKEN", "token-123")

	params, err := json.Marshal(map[string]any{
		"name":      cronToolName,
		"arguments": map[string]any{"action": "list"},
	})
	require.NoError(t, err)
	s := NewCronServer(bytes.NewReader(nil), ioDiscard{})
	s.httpClient = gateway.Client()

	result, err := s.handleToolCall(context.Background(), params)
	require.NoError(t, err)
	out := result.(map[string]any)
	assert.Equal(t, true, out["isError"])
	content := out["content"].([]map[string]string)
	assert.Contains(t, content[0]["text"], "bad cron request")
}

func TestReadMCPMessageErrors(t *testing.T) {
	_, err := readMCPMessage(bufio.NewReader(bytes.NewBufferString("\r\n{}")))
	assert.ErrorContains(t, err, "missing Content-Length")

	_, err = readMCPMessage(bufio.NewReader(bytes.NewBufferString("Content-Length: x\r\n\r\n{}")))
	assert.Error(t, err)
}

func TestWriteMCPMessageError(t *testing.T) {
	err := writeMCPMessage(errorWriter{}, map[string]any{"ok": true})
	assert.Error(t, err)
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

type errorWriter struct{}

func (errorWriter) Write(p []byte) (int, error) { return 0, errors.New("write failed") }
