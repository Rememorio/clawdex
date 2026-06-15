// Package mcp exposes clawdex tools over the Model Context Protocol.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/Rememorio/clawdex/internal/version"
)

const (
	defaultGatewayURL      = "http://127.0.0.1:8080"
	defaultProtocolVersion = "2024-11-05"
	cronToolName           = "cron"
	cronServerInstructions = "Use the cron tool for user-requested reminders, delayed follow-ups, and recurring work. Never replace scheduled work with shell sleep or polling. If a schedule cannot be created, report the tool error clearly. The run action starts a job asynchronously; if it returns status=running, tell the user the job has started and do not block the current turn waiting for the final scheduled output."
)

type mcpFraming int

const (
	mcpFramingContentLength mcpFraming = iota
	mcpFramingJSONLine
)

type Server struct {
	in         io.Reader
	out        io.Writer
	gatewayURL string
	token      string
	httpClient *http.Client
}

func NewCronServer(in io.Reader, out io.Writer) *Server {
	gatewayURL := strings.TrimRight(strings.TrimSpace(os.Getenv("CLAWDEX_GATEWAY_URL")), "/")
	if gatewayURL == "" {
		gatewayURL = defaultGatewayURL
	}
	return &Server{
		in:         in,
		out:        out,
		gatewayURL: gatewayURL,
		token:      strings.TrimSpace(os.Getenv("CLAWDEX_CRON_CONTEXT_TOKEN")),
		httpClient: http.DefaultClient,
	}
}

func (s *Server) Serve(ctx context.Context) error {
	reader := bufio.NewReader(s.in)
	for {
		msg, framing, err := readMCPFrame(reader)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		var req rpcRequest
		if err := json.Unmarshal(msg, &req); err != nil {
			continue
		}
		if len(req.ID) == 0 {
			continue
		}
		result, rpcErr := s.handle(ctx, req)
		resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
		if rpcErr != nil {
			resp.Error = &rpcError{Code: -32000, Message: rpcErr.Error()}
		} else {
			resp.Result = result
		}
		if err := writeMCPFrame(s.out, resp, framing); err != nil {
			return err
		}
	}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *Server) handle(ctx context.Context, req rpcRequest) (any, error) {
	switch req.Method {
	case "initialize":
		return initializeResult(req.Params), nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": []any{cronToolDefinition()}}, nil
	case "tools/call":
		return s.handleToolCall(ctx, req.Params)
	case "resources/list":
		return map[string]any{"resources": []any{}}, nil
	case "prompts/list":
		return map[string]any{"prompts": []any{}}, nil
	default:
		return nil, fmt.Errorf("unsupported MCP method %s", req.Method)
	}
}

func initializeResult(raw json.RawMessage) map[string]any {
	protocolVersion := defaultProtocolVersion
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(raw) > 0 && json.Unmarshal(raw, &params) == nil && strings.TrimSpace(params.ProtocolVersion) != "" {
		protocolVersion = strings.TrimSpace(params.ProtocolVersion)
	}
	return map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "clawdex-cron",
			"version": version.Version,
		},
		"instructions": cronServerInstructions,
	}
}

func (s *Server) handleToolCall(ctx context.Context, raw json.RawMessage) (any, error) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &call); err != nil {
		return nil, err
	}
	if call.Name != cronToolName {
		return toolError("unknown tool: " + call.Name), nil
	}
	var args map[string]any
	if len(call.Arguments) == 0 || string(call.Arguments) == "null" {
		args = map[string]any{}
	} else if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return toolError("invalid cron arguments: " + err.Error()), nil
	}
	if s.token == "" {
		return toolError("missing CLAWDEX_CRON_CONTEXT_TOKEN"), nil
	}
	args["token"] = s.token
	result, err := s.callGateway(ctx, args)
	if err != nil {
		return toolError(err.Error()), nil
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	return toolText(string(data)), nil
}

func (s *Server) callGateway(ctx context.Context, args map[string]any) (any, error) {
	data, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.gatewayURL+"/cron/tool", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if !out.OK {
		if out.Error == "" {
			out.Error = resp.Status
		}
		return nil, fmt.Errorf("%s", out.Error)
	}
	var result any
	if len(out.Result) > 0 {
		if err := json.Unmarshal(out.Result, &result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func toolText(text string) map[string]any {
	return map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}},
	}
}

func toolError(text string) map[string]any {
	result := toolText(text)
	result["isError"] = true
	return result
}

func cronToolDefinition() map[string]any {
	return map[string]any{
		"name": cronToolName,
		"description": strings.Join([]string{
			"Manage clawdex scheduled jobs for reminders, delayed follow-ups, and recurring work.",
			"Use this instead of shell sleep or polling when the user asks to do something later.",
			"Use action=\"list\" before answering natural-language questions about existing reminders, scheduled jobs, or what the user needs to do later.",
			"Use action=\"get\" for one job's details, action=\"run\" for trigger/run now, and update/remove for changes or cancellation.",
			"Create jobs only when the user provides a concrete date, time, interval, cadence, or cron expression.",
			"Jobs are automatically scoped to the current chat; do not invent another delivery target.",
			"Use payload.kind=\"message\" for fixed reminder text and payload.kind=\"agent\" when fresh reasoning should run at schedule time.",
			"Payload text must be the actual reminder/task, not scheduling control text such as \"trigger once now\".",
			"action=\"run\" starts the job asynchronously and returns status=\"running\" before final delivery completes.",
		}, " "),
		"inputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": true,
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"description": "status, list, get, add, update, remove, or run a scheduled job.",
					"enum":        []string{"status", "list", "get", "add", "update", "remove", "run"},
				},
				"include_disabled": map[string]any{
					"type":        "boolean",
					"description": "Set true only when the user asks for disabled, completed, or historical jobs.",
				},
				"id":     map[string]any{"type": "string"},
				"job_id": map[string]any{"type": "string"},
				"job":    cronJobSchema(),
				"patch":  cronPatchSchema(),
			},
			"required": []string{"action"},
		},
	}
}

func cronJobSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"name":     map[string]any{"type": "string"},
			"enabled":  map[string]any{"type": "boolean"},
			"schedule": cronScheduleSchema(),
			"payload":  cronPayloadSchema(),
		},
	}
}

func cronPatchSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"name":     map[string]any{"type": "string"},
			"enabled":  map[string]any{"type": "boolean"},
			"schedule": cronScheduleSchema(),
			"payload":  cronPayloadSchema(),
		},
	}
}

func cronScheduleSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"kind": map[string]any{"type": "string", "enum": []string{"at", "every", "cron"}},
			"at": map[string]any{
				"type":        "string",
				"description": "RFC3339 timestamp for one-shot jobs.",
			},
			"every_seconds": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"description": "Interval in seconds for recurring jobs.",
			},
			"anchor": map[string]any{
				"type":        "string",
				"description": "Optional RFC3339 interval anchor.",
			},
			"expr": map[string]any{
				"type":        "string",
				"description": "Five-field cron expression: minute hour day month weekday.",
			},
			"timezone": map[string]any{
				"type":        "string",
				"description": "Optional IANA timezone for cron expressions.",
			},
		},
		"required": []string{"kind"},
	}
}

func cronPayloadSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"kind": map[string]any{"type": "string", "enum": []string{"message", "agent"}},
			"text": map[string]any{
				"type":        "string",
				"description": "Reminder text or future agent instruction.",
			},
		},
		"required": []string{"kind", "text"},
	}
}

func readMCPFrame(r *bufio.Reader) ([]byte, mcpFraming, error) {
	contentLength := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, mcpFramingContentLength, err
		}
		line = strings.TrimRight(line, "\r\n")
		if contentLength < 0 && strings.HasPrefix(strings.TrimSpace(line), "{") {
			return []byte(strings.TrimSpace(line)), mcpFramingJSONLine, nil
		}
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, mcpFramingContentLength, err
			}
			contentLength = parsed
		}
	}
	if contentLength < 0 {
		return nil, mcpFramingContentLength, fmt.Errorf("missing Content-Length")
	}
	data := make([]byte, contentLength)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, mcpFramingContentLength, err
	}
	return data, mcpFramingContentLength, nil
}

func readMCPMessage(r *bufio.Reader) ([]byte, error) {
	data, _, err := readMCPFrame(r)
	return data, err
}

func writeMCPFrame(w io.Writer, v any, framing mcpFraming) error {
	if framing == mcpFramingJSONLine {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
		_, err = w.Write([]byte("\n"))
		return err
	}
	return writeMCPMessage(w, v)
}

func writeMCPMessage(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(data)); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}
