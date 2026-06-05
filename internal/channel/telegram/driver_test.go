package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rememorio/clawdex/internal/channel"
	"github.com/Rememorio/clawdex/internal/logger"
)

// ── Pure function tests ──

func TestSplitByRuneLimit_Empty(t *testing.T) {
	assert.Equal(t, []string{"(empty response)"}, splitByRuneLimit("", 100))
}

func TestSplitByRuneLimit_Whitespace(t *testing.T) {
	assert.Equal(t, []string{"(empty response)"}, splitByRuneLimit("   \n  ", 100))
}

func TestSplitByRuneLimit_Short(t *testing.T) {
	assert.Equal(t, []string{"hello"}, splitByRuneLimit("hello", 100))
}

func TestSplitByRuneLimit_ExactLimit(t *testing.T) {
	assert.Equal(t, []string{"abcde"}, splitByRuneLimit("abcde", 5))
}

func TestSplitByRuneLimit_Split(t *testing.T) {
	parts := splitByRuneLimit("abcdefghij", 3)
	assert.Equal(t, []string{"abc", "def", "ghi", "j"}, parts)
}

func TestSplitByRuneLimit_Unicode(t *testing.T) {
	parts := splitByRuneLimit("你好世界测试", 4)
	assert.Equal(t, []string{"你好世界", "测试"}, parts)
}

func TestSplitByNewline_Short(t *testing.T) {
	assert.Equal(t, []string{"hello"}, splitByNewline("hello", 100))
}

func TestSplitByNewline_Empty(t *testing.T) {
	assert.Equal(t, []string{"(empty response)"}, splitByNewline("", 100))
}

func TestSplitByNewline_SplitsOnParagraphs(t *testing.T) {
	text := "paragraph one\n\nparagraph two\n\nparagraph three"
	parts := splitByNewline(text, 30)
	assert.Equal(t, []string{"paragraph one\n\nparagraph two", "paragraph three"}, parts)
}

func TestSplitByNewline_LargeParagraph(t *testing.T) {
	text := "short\n\n" + strings.Repeat("x", 50)
	parts := splitByNewline(text, 30)
	require.Len(t, parts, 3)
	assert.Equal(t, "short", parts[0])
	assert.Len(t, []rune(parts[1]), 30)
	assert.Len(t, []rune(parts[2]), 20)
}

func TestSplitByNewline_FallsBackToHardSplit(t *testing.T) {
	// A single word longer than the limit.
	text := strings.Repeat("a", 20)
	parts := splitByNewline(text, 8)
	assert.Equal(t, []string{"aaaaaaaa", "aaaaaaaa", "aaaa"}, parts)
}

func TestFirstNonEmpty(t *testing.T) {
	assert.Equal(t, "b", firstNonEmpty("", "b", "c"))
	assert.Equal(t, "a", firstNonEmpty("a", "b"))
	assert.Equal(t, "", firstNonEmpty("", "  ", ""))
	assert.Equal(t, "", firstNonEmpty())
}

func TestPickMessage_Message(t *testing.T) {
	msg := &message{MessageID: 1}
	upd := update{Message: msg}
	assert.Equal(t, msg, pickMessage(upd))
}

func TestPickMessage_ChannelPost(t *testing.T) {
	msg := &message{MessageID: 2}
	upd := update{ChannelPost: msg}
	assert.Equal(t, msg, pickMessage(upd))
}

func TestPickMessage_PreferMessage(t *testing.T) {
	msg := &message{MessageID: 1}
	cp := &message{MessageID: 2}
	upd := update{Message: msg, ChannelPost: cp}
	assert.Equal(t, msg, pickMessage(upd))
}

func TestPickMessage_None(t *testing.T) {
	assert.Nil(t, pickMessage(update{}))
}

func TestProcessUpdate_LogsChatIDWhenGroupIsDenied(t *testing.T) {
	var buf bytes.Buffer
	logger.SetLevel(logger.LevelInfo)
	logger.SetOutput(&buf)
	t.Cleanup(func() {
		logger.SetLevel(logger.LevelInfo)
		logger.SetOutput(os.Stderr)
	})

	d := New(Config{BotToken: "tok", GroupPolicy: "allowlist"}, nil)
	h := &testHandler{fn: func(context.Context, channel.Message,
		channel.Responder) {
	}}
	upd := update{
		UpdateID: 42,
		Message: &message{
			MessageID: 7,
			Text:      "hello from group",
			Chat:      chat{ID: -1001234567890, Type: "supergroup"},
			From:      &user{ID: 31415926},
		},
	}

	d.processUpdate(context.Background(), upd, h)

	output := buf.String()
	assert.Contains(t, output, "telegram recv")
	assert.Contains(t, output, "telegram skip")
	assert.Contains(t, output, "chat_id=-1001234567890")
	assert.Contains(t, output, "sender_id=31415926")
	assert.Contains(t, output, "reason=access_denied")
	assert.Contains(t, output, "chat_type=supergroup")
}

// ── Constructor tests ──

func TestNew_Name(t *testing.T) {
	d := New(Config{BotToken: "tok"}, nil)
	assert.Equal(t, "telegram", d.Name())
}

func TestNew_CustomName(t *testing.T) {
	d := New(Config{Name: "tg-bot2", BotToken: "tok"}, nil)
	assert.Equal(t, "tg-bot2", d.Name())
}

func TestNew_DefaultProbeTimeout(t *testing.T) {
	d := New(Config{BotToken: "tok"}, nil)
	assert.Equal(t, 8*time.Second, d.cfg.StartupProbeTimeout)
}

func TestNew_CustomProbeTimeout(t *testing.T) {
	d := New(Config{BotToken: "tok", StartupProbeTimeout: 3 * time.Second}, nil)
	assert.Equal(t, 3*time.Second, d.cfg.StartupProbeTimeout)
}

func TestNew_DefaultLinkPreviewEnabled(t *testing.T) {
	d := New(Config{BotToken: "tok"}, nil)
	assert.False(t, d.api.disableLinkPreview)
}

func TestNew_NoGlobalHTTPClientTimeout(t *testing.T) {
	d := New(Config{BotToken: "tok"}, nil)
	assert.Zero(t, d.api.client.Timeout)
}

// ── httptest-based API tests ──

// newTestDriver creates a Driver pointed at an httptest server.
func newTestDriver(t *testing.T, handler http.Handler) (*Driver, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	d := &Driver{
		api: &apiClient{
			baseURL: ts.URL,
			client:  &http.Client{Timeout: 5 * time.Second},
		},
		name: "telegram",
		cfg: Config{
			BotToken:            "test-token",
			PollTimeout:         1,
			StartupProbeTimeout: 2 * time.Second,
			ChunkMode:           "length",
			TextChunkLimit:      3500,
			DMPolicy:            "open",
		},
	}
	return d, ts
}

func TestStartupProbe_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/getMe", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(meResponse{
			OK: true,
			Result: struct {
				ID       int64  `json:"id"`
				Username string `json:"username"`
			}{ID: 123, Username: "testbot"},
		})
	})
	mux.HandleFunc("/deleteWebhook", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	})

	d, _ := newTestDriver(t, mux)
	err := d.startupProbe(context.Background())
	assert.NoError(t, err)
}

func TestStartupProbe_Failure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/getMe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"ok":false,"description":"Unauthorized"}`)
	})

	d, _ := newTestDriver(t, mux)
	err := d.startupProbe(context.Background())
	assert.ErrorContains(t, err, "startup probe failed")
}

func TestStartupProbe_GetMeNotOK(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/getMe", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ok":false,"description":"bad token"}`)
	})

	d, _ := newTestDriver(t, mux)
	err := d.startupProbe(context.Background())
	assert.ErrorContains(t, err, "bad token")
}

func TestTyping(t *testing.T) {
	var gotChatID string
	mux := http.NewServeMux()
	mux.HandleFunc("/sendChatAction", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotChatID = r.FormValue("chat_id")
		fmt.Fprint(w, `{"ok":true}`)
	})

	d, _ := newTestDriver(t, mux)
	err := d.Typing(context.Background(), channel.Message{ChatID: 42})
	assert.NoError(t, err)
	assert.Equal(t, "42", gotChatID)
}

func TestReply_SingleChunk(t *testing.T) {
	var gotText, gotReplyTo string
	mux := http.NewServeMux()
	mux.HandleFunc("/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotText = r.FormValue("text")
		gotReplyTo = r.FormValue("reply_to_message_id")
		fmt.Fprint(w, `{"ok":true}`)
	})

	d, _ := newTestDriver(t, mux)
	err := d.Reply(context.Background(), channel.Message{MessageID: 5}, "hello")
	assert.NoError(t, err)
	assert.Equal(t, "hello", gotText)
	assert.Equal(t, "5", gotReplyTo)
}

func TestReply_MultiChunk(t *testing.T) {
	var mu sync.Mutex
	var calls []struct{ text, replyTo string }

	mux := http.NewServeMux()
	mux.HandleFunc("/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		mu.Lock()
		calls = append(calls, struct{ text, replyTo string }{
			text:    r.FormValue("text"),
			replyTo: r.FormValue("reply_to_message_id"),
		})
		mu.Unlock()
		fmt.Fprint(w, `{"ok":true}`)
	})

	d, _ := newTestDriver(t, mux)
	// Build text longer than 3500 runes
	longText := strings.Repeat("x", 4000)
	err := d.Reply(context.Background(), channel.Message{MessageID: 7}, longText)
	assert.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, calls, 2)
	// First chunk should reply to original
	assert.Equal(t, "7", calls[0].replyTo)
	assert.Len(t, []rune(calls[0].text), 3500)
	// Second chunk should not reply to original
	assert.Equal(t, "", calls[1].replyTo)
	assert.Len(t, []rune(calls[1].text), 500)
}

func TestReply_Empty(t *testing.T) {
	var gotText string
	mux := http.NewServeMux()
	mux.HandleFunc("/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotText = r.FormValue("text")
		fmt.Fprint(w, `{"ok":true}`)
	})

	d, _ := newTestDriver(t, mux)
	err := d.Reply(context.Background(), channel.Message{MessageID: 1}, "")
	assert.NoError(t, err)
	assert.Equal(t, "(empty response)", gotText)
}

func TestStart_ProcessesMessages(t *testing.T) {
	callCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/getMe", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(meResponse{
			OK: true,
			Result: struct {
				ID       int64  `json:"id"`
				Username string `json:"username"`
			}{ID: 1, Username: "bot"},
		})
	})
	mux.HandleFunc("/deleteWebhook", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("/getUpdates", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			json.NewEncoder(w).Encode(updateResponse{
				OK: true,
				Result: []update{
					{
						UpdateID: 100,
						Message: &message{
							MessageID: 1,
							Text:      "hello",
							Chat:      chat{ID: 42},
						},
					},
				},
			})
			return
		}
		// After delivering the message, wait until context is done
		<-r.Context().Done()
	})

	d, _ := newTestDriver(t, mux)

	var mu sync.Mutex
	var handled []channel.Message
	h := &testHandler{fn: func(ctx context.Context, msg channel.Message, resp channel.Responder) {
		mu.Lock()
		handled = append(handled, msg)
		mu.Unlock()
	}}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- d.Start(ctx, h) }()

	// Wait for handler to receive the message
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(handled) == 1
	}, 3*time.Second, 20*time.Millisecond)

	mu.Lock()
	assert.Equal(t, "hello", handled[0].Text)
	assert.Equal(t, int64(42), handled[0].ChatID)
	assert.Equal(t, "telegram", handled[0].Channel)
	mu.Unlock()

	cancel()
	<-errCh
}

func TestStart_FiltersByChatID(t *testing.T) {
	callCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/getMe", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(meResponse{
			OK: true,
			Result: struct {
				ID       int64  `json:"id"`
				Username string `json:"username"`
			}{ID: 1, Username: "bot"},
		})
	})
	mux.HandleFunc("/deleteWebhook", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("/getUpdates", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			json.NewEncoder(w).Encode(updateResponse{
				OK: true,
				Result: []update{
					{UpdateID: 1, Message: &message{MessageID: 1, Text: "allowed", Chat: chat{ID: 100, Type: "private"}, From: &user{ID: 100}}},
					{UpdateID: 2, Message: &message{MessageID: 2, Text: "blocked", Chat: chat{ID: 999, Type: "private"}, From: &user{ID: 999}}},
				},
			})
			return
		}
		<-r.Context().Done()
	})

	d, _ := newTestDriver(t, mux)
	d.cfg.AllowFrom = []int64{100}
	d.cfg.DMPolicy = "allowlist"

	var mu sync.Mutex
	var handled []channel.Message
	h := &testHandler{fn: func(ctx context.Context, msg channel.Message, resp channel.Responder) {
		mu.Lock()
		handled = append(handled, msg)
		mu.Unlock()
	}}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- d.Start(ctx, h) }()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(handled) == 1
	}, 3*time.Second, 20*time.Millisecond)

	mu.Lock()
	assert.Equal(t, "allowed", handled[0].Text)
	mu.Unlock()

	cancel()
	<-errCh
}

func TestStart_SkipsEmptyText(t *testing.T) {
	callCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/getMe", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(meResponse{OK: true, Result: struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		}{ID: 1, Username: "bot"}})
	})
	mux.HandleFunc("/deleteWebhook", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("/getUpdates", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			json.NewEncoder(w).Encode(updateResponse{
				OK: true,
				Result: []update{
					{UpdateID: 1, Message: &message{MessageID: 1, Text: "", Chat: chat{ID: 1}}},
					{UpdateID: 2, Message: &message{MessageID: 2, Text: "  ", Chat: chat{ID: 1}}},
					{UpdateID: 3, Message: &message{MessageID: 3, Text: "valid", Chat: chat{ID: 1}}},
				},
			})
			return
		}
		<-r.Context().Done()
	})

	d, _ := newTestDriver(t, mux)

	var mu sync.Mutex
	var handled []channel.Message
	h := &testHandler{fn: func(ctx context.Context, msg channel.Message, resp channel.Responder) {
		mu.Lock()
		handled = append(handled, msg)
		mu.Unlock()
	}}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- d.Start(ctx, h) }()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(handled) == 1
	}, 3*time.Second, 20*time.Millisecond)

	mu.Lock()
	assert.Equal(t, "valid", handled[0].Text)
	mu.Unlock()

	cancel()
	<-errCh
}

func TestResolveImageFileIDs_Photo(t *testing.T) {
	msg := &message{Photo: []photoSize{{FileID: "small"}, {FileID: "large"}}}
	ids := resolveImageFileIDs(msg)
	assert.Equal(t, []string{"large"}, ids)
}

func TestResolveImageFileIDs_Document(t *testing.T) {
	msg := &message{Document: &document{FileID: "doc-1", FileName: "test.pdf"}}
	ids := resolveImageFileIDs(msg)
	assert.Empty(t, ids)
}

func TestResolveImageFileIDs_Video(t *testing.T) {
	msg := &message{Video: &video{FileID: "vid-1"}}
	ids := resolveImageFileIDs(msg)
	assert.Empty(t, ids)
}

func TestResolveImageFileIDs_Audio(t *testing.T) {
	msg := &message{Audio: &audio{FileID: "aud-1"}}
	ids := resolveImageFileIDs(msg)
	assert.Empty(t, ids)
}

func TestResolveImageFileIDs_Voice(t *testing.T) {
	msg := &message{Voice: &voice{FileID: "voice-1"}}
	ids := resolveImageFileIDs(msg)
	assert.Empty(t, ids)
}

func TestResolveImageFileIDs_StaticSticker(t *testing.T) {
	msg := &message{Sticker: &sticker{FileID: "stk-1"}}
	ids := resolveImageFileIDs(msg)
	assert.Equal(t, []string{"stk-1"}, ids)
}

func TestResolveImageFileIDs_AnimatedStickerSkipped(t *testing.T) {
	msg := &message{Sticker: &sticker{FileID: "stk-anim", IsAnimated: true}}
	ids := resolveImageFileIDs(msg)
	assert.Empty(t, ids)
}

func TestResolveImageFileIDs_VideoStickerSkipped(t *testing.T) {
	msg := &message{Sticker: &sticker{FileID: "stk-vid", IsVideo: true}}
	ids := resolveImageFileIDs(msg)
	assert.Empty(t, ids)
}

func TestResolveImageFileIDs_None(t *testing.T) {
	msg := &message{Text: "just text"}
	ids := resolveImageFileIDs(msg)
	assert.Empty(t, ids)
}

func TestMediaPlaceholder_Photo(t *testing.T) {
	assert.Equal(t, "[image]", mediaPlaceholder(&message{Photo: []photoSize{{FileID: "x"}}}))
}

func TestMediaPlaceholder_Video(t *testing.T) {
	assert.Equal(t, "[video]", mediaPlaceholder(&message{Video: &video{FileID: "x"}}))
}

func TestMediaPlaceholder_VideoNote(t *testing.T) {
	assert.Equal(t, "[video]", mediaPlaceholder(&message{VideoNote: &videoNote{FileID: "x"}}))
}

func TestMediaPlaceholder_Audio(t *testing.T) {
	assert.Equal(t, "[audio]", mediaPlaceholder(&message{Audio: &audio{FileID: "x"}}))
}

func TestMediaPlaceholder_Voice(t *testing.T) {
	assert.Equal(t, "[audio]", mediaPlaceholder(&message{Voice: &voice{FileID: "x"}}))
}

func TestMediaPlaceholder_Document(t *testing.T) {
	assert.Equal(t, "[document: report.pdf]", mediaPlaceholder(&message{Document: &document{FileID: "x", FileName: "report.pdf"}}))
}

func TestMediaPlaceholder_DocumentNoName(t *testing.T) {
	assert.Equal(t, "[document]", mediaPlaceholder(&message{Document: &document{FileID: "x"}}))
}

func TestMediaPlaceholder_Sticker(t *testing.T) {
	assert.Equal(t, "[sticker]", mediaPlaceholder(&message{Sticker: &sticker{FileID: "x"}}))
}

func TestMediaPlaceholder_Fallback(t *testing.T) {
	assert.Equal(t, "[media]", mediaPlaceholder(&message{}))
}

func TestDownloadMedia_Integration(t *testing.T) {
	// Test the full getFile + download flow with a mock server.
	fileContent := []byte("fake image content")

	mux := http.NewServeMux()
	mux.HandleFunc("/bot/getFile", func(w http.ResponseWriter, r *http.Request) {
		fid := r.URL.Query().Get("file_id")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]string{"file_path": "photos/" + fid + ".jpg"},
		})
	})
	mux.HandleFunc("/file/bot/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fileContent)
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	d := &Driver{
		api: &apiClient{
			baseURL: ts.URL + "/bot",
			client:  &http.Client{Timeout: 5 * time.Second},
		},
	}

	paths, err := d.downloadMedia(context.Background(), []string{"photo-abc"})
	require.NoError(t, err)
	require.Len(t, paths, 1)
	assert.Contains(t, paths[0], "photo-abc.jpg")

	// Verify file content.
	data, err := os.ReadFile(paths[0])
	require.NoError(t, err)
	assert.Equal(t, fileContent, data)

	// Clean up temp dir.
	os.RemoveAll(filepath.Dir(paths[0]))
}

func TestDownloadMedia_GetFileFails(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/bot/getFile", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "server error")
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	d := &Driver{
		api: &apiClient{
			baseURL: ts.URL + "/bot",
			client:  &http.Client{Timeout: 5 * time.Second},
		},
	}

	paths, err := d.downloadMedia(context.Background(), []string{"photo-abc"})
	assert.Error(t, err)
	assert.Nil(t, paths)
}

func TestFileBaseURL(t *testing.T) {
	a := &apiClient{baseURL: "https://api.telegram.org/bot123:ABC"}
	assert.Equal(t, "https://api.telegram.org/file/bot123:ABC", a.fileBaseURL())
}

func TestStart_PhotoMessage(t *testing.T) {
	// Verify that a photo message with no text gets handled with a placeholder + imagePaths.
	callCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/bot/getMe", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(meResponse{OK: true, Result: struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		}{ID: 1, Username: "bot"}})
	})
	mux.HandleFunc("/bot/deleteWebhook", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("/bot/getFile", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]string{"file_path": "photos/test.jpg"},
		})
	})
	mux.HandleFunc("/file/bot/", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("fake image"))
	})
	mux.HandleFunc("/bot/getUpdates", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// Photo message with no text/caption.
			resp := updateResponse{
				OK: true,
				Result: []update{{
					UpdateID: 100,
					Message: &message{
						MessageID: 1,
						Chat:      chat{ID: 42},
						Photo:     []photoSize{{FileID: "small"}, {FileID: "large"}},
					},
				}},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		<-r.Context().Done()
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	d := &Driver{
		api: &apiClient{
			baseURL: ts.URL + "/bot",
			client:  &http.Client{Timeout: 5 * time.Second},
		},
		cfg: Config{
			BotToken:            "test-token",
			PollTimeout:         1,
			StartupProbeTimeout: 2 * time.Second,
			DMPolicy:            "open",
		},
	}

	var mu sync.Mutex
	var handled []channel.Message
	h := &testHandler{fn: func(ctx context.Context, msg channel.Message, resp channel.Responder) {
		mu.Lock()
		handled = append(handled, msg)
		mu.Unlock()
	}}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- d.Start(ctx, h) }()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(handled) == 1
	}, 3*time.Second, 20*time.Millisecond)

	mu.Lock()
	assert.Equal(t, "[image]", handled[0].Text)
	assert.Len(t, handled[0].MediaPaths, 1)
	assert.Contains(t, handled[0].MediaPaths[0], "test.jpg")
	mu.Unlock()

	cancel()
	<-errCh

	// Cleanup temp files.
	for _, p := range handled[0].MediaPaths {
		os.RemoveAll(filepath.Dir(p))
	}
}

// testHandler implements channel.Handler for tests.
type testHandler struct {
	fn func(ctx context.Context, msg channel.Message, resp channel.Responder)
}

func (h *testHandler) Handle(ctx context.Context, msg channel.Message, resp channel.Responder) {
	h.fn(ctx, msg, resp)
}

// ── Additional edge case tests ──

func TestReply_SendMessageError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/sendMessage", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"ok":false,"description":"bad request"}`)
	})

	d, _ := newTestDriver(t, mux)
	err := d.Reply(context.Background(), channel.Message{MessageID: 1}, "text")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "status=400")
}

func TestTyping_Error(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/sendChatAction", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `server error`)
	})

	d, _ := newTestDriver(t, mux)
	err := d.Typing(context.Background(), channel.Message{ChatID: 42})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "status=500")
}

func TestStartupProbe_DeleteWebhookFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/getMe", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(meResponse{
			OK: true,
			Result: struct {
				ID       int64  `json:"id"`
				Username string `json:"username"`
			}{ID: 123, Username: ""},
		})
	})
	mux.HandleFunc("/deleteWebhook", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `server error`)
	})

	d, _ := newTestDriver(t, mux)
	// Should still succeed — deleteWebhook failure is non-fatal
	err := d.startupProbe(context.Background())
	assert.NoError(t, err)
}

func TestStartupProbe_Timeout(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/getMe", func(w http.ResponseWriter, r *http.Request) {
		// Block until context is cancelled
		<-r.Context().Done()
	})

	d, _ := newTestDriver(t, mux)
	d.cfg.StartupProbeTimeout = 100 * time.Millisecond

	err := d.startupProbe(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "startup probe failed")
}

func TestStart_ContextCancelledBeforePoll(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/getMe", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(meResponse{OK: true, Result: struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		}{ID: 1, Username: "bot"}})
	})
	mux.HandleFunc("/deleteWebhook", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("/getUpdates", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	d, _ := newTestDriver(t, mux)

	ctx, cancel := context.WithCancel(context.Background())
	h := &testHandler{fn: func(ctx context.Context, msg channel.Message, resp channel.Responder) {}}

	errCh := make(chan error, 1)
	go func() { errCh <- d.Start(ctx, h) }()

	// Give it a moment to start polling
	time.Sleep(100 * time.Millisecond)
	cancel()

	err := <-errCh
	assert.ErrorIs(t, err, context.Canceled)
}

func TestStart_GetUpdatesNotOK(t *testing.T) {
	callCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/getMe", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(meResponse{OK: true, Result: struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		}{ID: 1, Username: "bot"}})
	})
	mux.HandleFunc("/deleteWebhook", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("/getUpdates", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount <= 2 {
			// Return not-ok to exercise the error/retry path
			fmt.Fprint(w, `{"ok":false,"description":"rate limited"}`)
			return
		}
		// Block until context done on subsequent calls
		<-r.Context().Done()
	})

	d, _ := newTestDriver(t, mux)

	ctx, cancel := context.WithCancel(context.Background())
	h := &testHandler{fn: func(ctx context.Context, msg channel.Message, resp channel.Responder) {}}

	errCh := make(chan error, 1)
	go func() { errCh <- d.Start(ctx, h) }()

	// Let it retry a couple times
	time.Sleep(500 * time.Millisecond)
	cancel()

	err := <-errCh
	assert.ErrorIs(t, err, context.Canceled)
}

func TestStart_CaptionMessage(t *testing.T) {
	callCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/getMe", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(meResponse{OK: true, Result: struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		}{ID: 1, Username: "bot"}})
	})
	mux.HandleFunc("/deleteWebhook", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("/getUpdates", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// Send a message with caption but no text
			json.NewEncoder(w).Encode(updateResponse{
				OK: true,
				Result: []update{
					{
						UpdateID: 100,
						Message: &message{
							MessageID: 1,
							Text:      "",
							Caption:   "photo caption",
							Chat:      chat{ID: 42},
						},
					},
				},
			})
			return
		}
		<-r.Context().Done()
	})

	d, _ := newTestDriver(t, mux)

	var mu sync.Mutex
	var handled []channel.Message
	h := &testHandler{fn: func(ctx context.Context, msg channel.Message, resp channel.Responder) {
		mu.Lock()
		handled = append(handled, msg)
		mu.Unlock()
	}}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- d.Start(ctx, h) }()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(handled) == 1
	}, 3*time.Second, 20*time.Millisecond)

	mu.Lock()
	assert.Equal(t, "photo caption", handled[0].Text)
	mu.Unlock()

	cancel()
	<-errCh
}

// ── Feature 1: Inline Keyboards ──

func TestSendMessageAndGetID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"message_id": 42},
		})
	})

	d, _ := newTestDriver(t, mux)
	id, err := d.api.sendMessageAndGetID(context.Background(), 100, "hello", 0, nil, 0)
	assert.NoError(t, err)
	assert.Equal(t, int64(42), id)
}

func TestSendMessageAndGetID_WithMarkup(t *testing.T) {
	var gotMarkup string
	mux := http.NewServeMux()
	mux.HandleFunc("/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotMarkup = r.FormValue("reply_markup")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"message_id": 7},
		})
	})

	d, _ := newTestDriver(t, mux)
	markup := []byte(`{"inline_keyboard":[[{"text":"Go","callback_data":"/go"}]]}`)
	id, err := d.api.sendMessageAndGetID(context.Background(), 100, "pick", 0, markup, 0)
	assert.NoError(t, err)
	assert.Equal(t, int64(7), id)
	assert.Contains(t, gotMarkup, "inline_keyboard")
}

func TestEditMessageText(t *testing.T) {
	var gotChatID, gotMsgID, gotText string
	mux := http.NewServeMux()
	mux.HandleFunc("/editMessageText", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotChatID = r.FormValue("chat_id")
		gotMsgID = r.FormValue("message_id")
		gotText = r.FormValue("text")
		fmt.Fprint(w, `{"ok":true}`)
	})

	d, _ := newTestDriver(t, mux)
	err := d.api.editMessageText(context.Background(), 100, 42, "updated text", nil)
	assert.NoError(t, err)
	assert.Equal(t, "100", gotChatID)
	assert.Equal(t, "42", gotMsgID)
	assert.Equal(t, "updated text", gotText)
}

func TestAnswerCallbackQuery(t *testing.T) {
	var gotID string
	mux := http.NewServeMux()
	mux.HandleFunc("/answerCallbackQuery", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotID = r.FormValue("callback_query_id")
		fmt.Fprint(w, `{"ok":true}`)
	})

	d, _ := newTestDriver(t, mux)
	err := d.api.answerCallbackQuery(context.Background(), "cq-123")
	assert.NoError(t, err)
	assert.Equal(t, "cq-123", gotID)
}

func TestReplyWithKeyboard(t *testing.T) {
	var gotText, gotMarkup, gotReplyTo string
	mux := http.NewServeMux()
	mux.HandleFunc("/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotText = r.FormValue("text")
		gotMarkup = r.FormValue("reply_markup")
		gotReplyTo = r.FormValue("reply_to_message_id")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"message_id": 10},
		})
	})

	d, _ := newTestDriver(t, mux)
	msg := channel.Message{ChatID: 42, MessageID: 5}
	keyboard := [][]channel.KeyboardButton{
		{
			{Text: "New", CallbackData: "/new"},
			{Text: "Status", CallbackData: "/status"},
		},
	}
	err := d.ReplyWithKeyboard(context.Background(), msg, "Pick one:", keyboard)
	assert.NoError(t, err)
	assert.Equal(t, "Pick one:", gotText)
	assert.Equal(t, "5", gotReplyTo)
	assert.Contains(t, gotMarkup, `"callback_data":"/new"`)
	assert.Contains(t, gotMarkup, `"callback_data":"/status"`)
}

func TestStart_CallbackQuery(t *testing.T) {
	callCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/getMe", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(meResponse{OK: true, Result: struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		}{ID: 1, Username: "bot"}})
	})
	mux.HandleFunc("/deleteWebhook", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("/answerCallbackQuery", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("/getUpdates", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			json.NewEncoder(w).Encode(updateResponse{
				OK: true,
				Result: []update{
					{
						UpdateID: 100,
						CallbackQuery: &callbackQuery{
							ID:   "cq-1",
							From: &user{ID: 99},
							Message: &message{
								MessageID: 10,
								Chat:      chat{ID: 42},
							},
							Data: "/status",
						},
					},
				},
			})
			return
		}
		<-r.Context().Done()
	})

	d, _ := newTestDriver(t, mux)

	var mu sync.Mutex
	var handled []channel.Message
	h := &testHandler{fn: func(ctx context.Context, msg channel.Message, resp channel.Responder) {
		mu.Lock()
		handled = append(handled, msg)
		mu.Unlock()
	}}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- d.Start(ctx, h) }()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(handled) == 1
	}, 3*time.Second, 20*time.Millisecond)

	mu.Lock()
	assert.Equal(t, "/status", handled[0].Text)
	assert.Equal(t, int64(42), handled[0].ChatID)
	mu.Unlock()

	cancel()
	<-errCh
}

func TestStart_CallbackQuery_Filtered(t *testing.T) {
	callCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/getMe", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(meResponse{OK: true, Result: struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		}{ID: 1, Username: "bot"}})
	})
	mux.HandleFunc("/deleteWebhook", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("/answerCallbackQuery", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("/getUpdates", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			json.NewEncoder(w).Encode(updateResponse{
				OK: true,
				Result: []update{
					{
						UpdateID: 100,
						CallbackQuery: &callbackQuery{
							ID:   "cq-1",
							From: &user{ID: 99},
							Message: &message{
								MessageID: 10,
								Chat:      chat{ID: 999, Type: "group"}, // Not allowed
							},
							Data: "/status",
						},
					},
					{
						UpdateID: 101,
						Message: &message{
							MessageID: 11,
							Text:      "allowed msg",
							Chat:      chat{ID: 100, Type: "private"},
							From:      &user{ID: 100},
						},
					},
				},
			})
			return
		}
		<-r.Context().Done()
	})

	d, _ := newTestDriver(t, mux)
	d.cfg.AllowFrom = []int64{100}
	d.cfg.DMPolicy = "allowlist"

	var mu sync.Mutex
	var handled []channel.Message
	h := &testHandler{fn: func(ctx context.Context, msg channel.Message, resp channel.Responder) {
		mu.Lock()
		handled = append(handled, msg)
		mu.Unlock()
	}}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- d.Start(ctx, h) }()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(handled) == 1
	}, 3*time.Second, 20*time.Millisecond)

	mu.Lock()
	// Only the allowed message should be handled, not the filtered callback query.
	assert.Equal(t, "allowed msg", handled[0].Text)
	mu.Unlock()

	cancel()
	<-errCh
}

// ── Feature 2: Streaming ──

func TestSendMessage_ReturnsID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"message_id": 55},
		})
	})

	d, _ := newTestDriver(t, mux)
	id, err := d.SendMessage(context.Background(), channel.Message{ChatID: 42, MessageID: 1}, "streaming start")
	assert.NoError(t, err)
	assert.Equal(t, int64(55), id)
}

func TestEditMessage_Success(t *testing.T) {
	var gotText string
	mux := http.NewServeMux()
	mux.HandleFunc("/editMessageText", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotText = r.FormValue("text")
		fmt.Fprint(w, `{"ok":true}`)
	})

	d, _ := newTestDriver(t, mux)
	err := d.EditMessage(context.Background(), 42, 55, "updated text")
	assert.NoError(t, err)
	assert.Equal(t, "updated text", gotText)
}

// ── Feature 3: Mention Mode ──

func TestShouldRespond_PrivateChat(t *testing.T) {
	d := &Driver{}
	msg := &message{Text: "hello", Chat: chat{Type: "private"}}
	text, respond := d.shouldRespond(msg)
	assert.True(t, respond)
	assert.Equal(t, "hello", text)
}

func TestShouldRespond_PrivateChat_EmptyType(t *testing.T) {
	d := &Driver{}
	msg := &message{Text: "hello", Chat: chat{Type: ""}}
	text, respond := d.shouldRespond(msg)
	assert.True(t, respond)
	assert.Equal(t, "hello", text)
}

func TestShouldRespond_NonPrivateRejected(t *testing.T) {
	d := &Driver{}
	for _, chatType := range []string{"channel", "group", "supergroup"} {
		msg := &message{Text: "hello", Chat: chat{Type: chatType}}
		_, respond := d.shouldRespond(msg)
		assert.False(t, respond, "should reject chat type %q", chatType)
	}
}

// ── Feature 4: Outbound Media ──

func TestPostMultipart(t *testing.T) {
	var gotChatID, gotCaption, gotFieldName string
	var gotFileContent []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/sendPhoto", func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(1 << 20)
		gotChatID = r.FormValue("chat_id")
		gotCaption = r.FormValue("caption")
		file, header, _ := r.FormFile("photo")
		gotFieldName = header.Filename
		gotFileContent, _ = io.ReadAll(file)
		fmt.Fprint(w, `{"ok":true}`)
	})

	d, _ := newTestDriver(t, mux)

	// Create a temp file to upload.
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.jpg")
	require.NoError(t, os.WriteFile(tmpFile, []byte("fake image data"), 0o644))

	err := d.api.sendPhoto(context.Background(), 42, tmpFile, "caption text", 0, 0)
	assert.NoError(t, err)
	assert.Equal(t, "42", gotChatID)
	assert.Equal(t, "caption text", gotCaption)
	assert.Equal(t, "test.jpg", gotFieldName)
	assert.Equal(t, []byte("fake image data"), gotFileContent)
}

func TestSendDocument(t *testing.T) {
	var gotFieldName string
	mux := http.NewServeMux()
	mux.HandleFunc("/sendDocument", func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(1 << 20)
		_, header, _ := r.FormFile("document")
		gotFieldName = header.Filename
		fmt.Fprint(w, `{"ok":true}`)
	})

	d, _ := newTestDriver(t, mux)
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "report.pdf")
	require.NoError(t, os.WriteFile(tmpFile, []byte("pdf content"), 0o644))

	err := d.api.sendDocument(context.Background(), 42, tmpFile, "", 0, 0)
	assert.NoError(t, err)
	assert.Equal(t, "report.pdf", gotFieldName)
}

func TestReplyWithMedia_PhotoByExtension(t *testing.T) {
	var calledEndpoints []string
	mux := http.NewServeMux()
	mux.HandleFunc("/sendPhoto", func(w http.ResponseWriter, r *http.Request) {
		calledEndpoints = append(calledEndpoints, "sendPhoto")
		fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("/sendDocument", func(w http.ResponseWriter, r *http.Request) {
		calledEndpoints = append(calledEndpoints, "sendDocument")
		fmt.Fprint(w, `{"ok":true}`)
	})

	d, _ := newTestDriver(t, mux)
	tmpDir := t.TempDir()

	jpgFile := filepath.Join(tmpDir, "photo.jpg")
	pdfFile := filepath.Join(tmpDir, "doc.pdf")
	require.NoError(t, os.WriteFile(jpgFile, []byte("jpg"), 0o644))
	require.NoError(t, os.WriteFile(pdfFile, []byte("pdf"), 0o644))

	msg := channel.Message{ChatID: 42, MessageID: 1}
	err := d.ReplyWithMedia(context.Background(), msg, "look!", []string{jpgFile, pdfFile})
	assert.NoError(t, err)
	assert.Equal(t, []string{"sendPhoto", "sendDocument"}, calledEndpoints)
}

func TestReplyWithMedia_CaptionTruncation(t *testing.T) {
	var gotCaption string
	var extraTextSent string
	mux := http.NewServeMux()
	mux.HandleFunc("/sendPhoto", func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(1 << 20)
		gotCaption = r.FormValue("caption")
		fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		extraTextSent = r.FormValue("text")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"message_id": 1},
		})
	})

	d, _ := newTestDriver(t, mux)
	tmpDir := t.TempDir()
	jpgFile := filepath.Join(tmpDir, "photo.png")
	require.NoError(t, os.WriteFile(jpgFile, []byte("data"), 0o644))

	// Caption longer than 1024 chars.
	longCaption := strings.Repeat("a", 1100)
	msg := channel.Message{ChatID: 42, MessageID: 1}
	err := d.ReplyWithMedia(context.Background(), msg, longCaption, []string{jpgFile})
	assert.NoError(t, err)
	assert.Len(t, []rune(gotCaption), 1024)
	assert.Equal(t, strings.Repeat("a", 76), extraTextSent) // 1100 - 1024 = 76
}

func TestReplyWithMedia_VoiceNote(t *testing.T) {
	var gotFieldName string
	mux := http.NewServeMux()
	mux.HandleFunc("/sendVoice", func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(10 << 20)
		if r.MultipartForm != nil {
			for name := range r.MultipartForm.File {
				gotFieldName = name
			}
		}
		fmt.Fprint(w, `{"ok":true}`)
	})

	d, _ := newTestDriver(t, mux)

	tmpDir := t.TempDir()
	oggFile := filepath.Join(tmpDir, "note.ogg")
	require.NoError(t, os.WriteFile(oggFile, []byte("ogg-data"), 0o644))

	msg := channel.Message{ChatID: 42, MessageID: 1}
	err := d.ReplyWithMedia(context.Background(), msg, "voice", []string{oggFile})
	assert.NoError(t, err)
	assert.Equal(t, "voice", gotFieldName)
}

// ── Feature: Access Control ──

func TestCheckAccess_PrivateOpen(t *testing.T) {
	d := &Driver{cfg: Config{DMPolicy: "open"}}
	assert.Equal(t, accessAllowed, d.checkAccess("private", 1, 100))
	assert.Equal(t, accessAllowed, d.checkAccess("", 1, 100))
}

func TestCheckAccess_PrivateAllowlist(t *testing.T) {
	d := &Driver{cfg: Config{DMPolicy: "allowlist", AllowFrom: []int64{100, 200}}}
	assert.Equal(t, accessAllowed, d.checkAccess("private", 1, 100))
	assert.Equal(t, accessDenied, d.checkAccess("private", 1, 999))
}

func TestCheckAccess_GroupDisabled(t *testing.T) {
	d := &Driver{cfg: Config{DMPolicy: "open", GroupPolicy: "disabled"}}
	assert.Equal(t, accessDenied, d.checkAccess("group", 123, 100))
	assert.Equal(t, accessDenied, d.checkAccess("supergroup", 456, 100))
}

func TestCheckAccess_GroupOpen(t *testing.T) {
	d := &Driver{cfg: Config{DMPolicy: "open", GroupPolicy: "open"}}
	assert.Equal(t, accessAllowed, d.checkAccess("group", 123, 100))
	assert.Equal(t, accessAllowed, d.checkAccess("supergroup", 456, 200))
}

func TestCheckAccess_GroupAllowlist(t *testing.T) {
	enabled := true
	d := &Driver{cfg: Config{
		DMPolicy:       "open",
		GroupPolicy:    "allowlist",
		GroupAllowFrom: []int64{123, 456},
		Groups: map[int64]GroupRule{
			123: {Enabled: &enabled, AllowFrom: []int64{100, 200}},
		},
	}}
	// Group in allowlist, user allowed.
	assert.Equal(t, accessAllowed, d.checkAccess("group", 123, 100))
	// Group in allowlist, user not allowed.
	assert.Equal(t, accessDenied, d.checkAccess("group", 123, 999))
	// Group not in allowlist.
	assert.Equal(t, accessDenied, d.checkAccess("group", 789, 100))
}

func TestCheckAccess_PairingMode(t *testing.T) {
	d := &Driver{cfg: Config{DMPolicy: "pairing", AllowFrom: []int64{100}}}
	assert.Equal(t, accessAllowed, d.checkAccess("private", 1, 100))
	assert.Equal(t, accessPairing, d.checkAccess("private", 1, 999))
}

func TestCheckAccess_PairingDefault(t *testing.T) {
	d := &Driver{cfg: Config{}} // default DMPolicy is empty → treated as "pairing"
	assert.Equal(t, accessPairing, d.checkAccess("private", 1, 100))
}

// ── Feature: Forum Thread Support ──

func TestStart_ForumMessage(t *testing.T) {
	callCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/getMe", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(meResponse{OK: true, Result: struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		}{ID: 1, Username: "bot"}})
	})
	mux.HandleFunc("/deleteWebhook", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("/setMyCommands", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("/getUpdates", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			json.NewEncoder(w).Encode(updateResponse{
				OK: true,
				Result: []update{{
					UpdateID: 100,
					Message: &message{
						MessageID:       1,
						MessageThreadID: 42,
						Text:            "forum msg",
						Chat:            chat{ID: 10},
					},
				}},
			})
			return
		}
		<-r.Context().Done()
	})

	d, _ := newTestDriver(t, mux)

	var mu sync.Mutex
	var handled []channel.Message
	h := &testHandler{fn: func(ctx context.Context, msg channel.Message, resp channel.Responder) {
		mu.Lock()
		handled = append(handled, msg)
		mu.Unlock()
	}}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- d.Start(ctx, h) }()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(handled) == 1
	}, 3*time.Second, 20*time.Millisecond)

	mu.Lock()
	assert.Equal(t, int64(42), handled[0].ThreadID)
	assert.Equal(t, "forum msg", handled[0].Text)
	mu.Unlock()

	cancel()
	<-errCh
}

func TestReply_WithThreadID(t *testing.T) {
	var gotThreadID string
	mux := http.NewServeMux()
	mux.HandleFunc("/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotThreadID = r.FormValue("message_thread_id")
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
	})

	d, _ := newTestDriver(t, mux)
	err := d.Reply(context.Background(), channel.Message{ChatID: 42, MessageID: 5, ThreadID: 99}, "hello")
	assert.NoError(t, err)
	assert.Equal(t, "99", gotThreadID)
}

func TestTyping_WithThreadID(t *testing.T) {
	var gotThreadID string
	mux := http.NewServeMux()
	mux.HandleFunc("/sendChatAction", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotThreadID = r.FormValue("message_thread_id")
		fmt.Fprint(w, `{"ok":true}`)
	})

	d, _ := newTestDriver(t, mux)
	err := d.Typing(context.Background(), channel.Message{ChatID: 42, ThreadID: 77})
	assert.NoError(t, err)
	assert.Equal(t, "77", gotThreadID)
}

// ── Feature: Status Reactions ──

func TestSetReaction(t *testing.T) {
	var gotChatID, gotMsgID, gotReaction string
	mux := http.NewServeMux()
	mux.HandleFunc("/setMessageReaction", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotChatID = r.FormValue("chat_id")
		gotMsgID = r.FormValue("message_id")
		gotReaction = r.FormValue("reaction")
		fmt.Fprint(w, `{"ok":true}`)
	})

	d, _ := newTestDriver(t, mux)
	err := d.SetReaction(context.Background(), 42, 10, "👀")
	assert.NoError(t, err)
	assert.Equal(t, "42", gotChatID)
	assert.Equal(t, "10", gotMsgID)
	assert.Contains(t, gotReaction, "👀")
}

// ── Feature: setMyCommands ──

func TestSetMyCommands(t *testing.T) {
	var gotBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/setMyCommands", func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		fmt.Fprint(w, `{"ok":true}`)
	})

	d, _ := newTestDriver(t, mux)
	commands := []botCommand{
		{Command: "help", Description: "Show help"},
		{Command: "new", Description: "Start fresh"},
	}
	err := d.api.setMyCommands(context.Background(), commands)
	assert.NoError(t, err)
	assert.Contains(t, string(gotBody), `"command":"help"`)
	assert.Contains(t, string(gotBody), `"command":"new"`)
}

// ── Feature: Draft Streaming ──

func TestSendDraft(t *testing.T) {
	var gotChatID, gotText, gotParseMode string
	mux := http.NewServeMux()
	mux.HandleFunc("/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotChatID = r.FormValue("chat_id")
		gotText = r.FormValue("text")
		gotParseMode = r.FormValue("parse_mode")
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":55}}`)
	})

	d, _ := newTestDriver(t, mux)
	msg := channel.Message{ChatID: 42, MessageID: 5}
	id, err := d.SendDraft(context.Background(), msg, "hello **bold**")
	assert.NoError(t, err)
	assert.Equal(t, int64(55), id)
	assert.Equal(t, "42", gotChatID)
	assert.Contains(t, gotText, "<b>bold</b>")
	assert.Equal(t, "HTML", gotParseMode)
}

func TestFinalizeDraft(t *testing.T) {
	var gotChatID, gotMsgID, gotText string
	mux := http.NewServeMux()
	mux.HandleFunc("/editMessageText", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotChatID = r.FormValue("chat_id")
		gotMsgID = r.FormValue("message_id")
		gotText = r.FormValue("text")
		fmt.Fprint(w, `{"ok":true}`)
	})

	d, _ := newTestDriver(t, mux)
	err := d.FinalizeDraft(context.Background(), 42, 99, "final **text**")
	assert.NoError(t, err)
	assert.Equal(t, "42", gotChatID)
	assert.Equal(t, "99", gotMsgID)
	assert.Contains(t, gotText, "<b>text</b>")
}

// ── Additional coverage tests for telegram driver ──

func TestUpdateKind(t *testing.T) {
	tests := []struct {
		name string
		upd  update
		want string
	}{
		{"callback_query", update{CallbackQuery: &callbackQuery{}}, "callback_query"},
		{"message", update{Message: &message{}}, "message"},
		{"channel_post", update{ChannelPost: &message{}}, "channel_post"},
		{"empty update", update{}, "update"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, updateKind(tt.upd))
		})
	}
}

func TestTelegramChatType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"group", "group"},
		{"supergroup", "group"},
		{"private", ""},
		{"channel", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, telegramChatType(tt.input))
		})
	}
}

func TestTelegramSenderName(t *testing.T) {
	tests := []struct {
		name string
		u    *user
		want string
	}{
		{"nil user", nil, ""},
		{"username present", &user{Username: "alice"}, "alice"},
		{"full name only", &user{FirstName: "Bob", LastName: "Smith"}, "Bob Smith"},
		{"first name only", &user{FirstName: "Charlie"}, "Charlie"},
		{"all empty", &user{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, telegramSenderName(tt.u))
		})
	}
}

func TestRequireMentionForGroup_Default(t *testing.T) {
	d := &Driver{cfg: Config{}}
	// Default: require mention in groups.
	assert.True(t, d.requireMentionForGroup(-100))
}

func TestRequireMentionForGroup_GlobalFalse(t *testing.T) {
	f := false
	d := &Driver{cfg: Config{RequireMention: &f}}
	assert.False(t, d.requireMentionForGroup(-100))
}

func TestRequireMentionForGroup_PerGroupOverride(t *testing.T) {
	globalTrue := true
	perGroupFalse := false
	d := &Driver{cfg: Config{
		RequireMention: &globalTrue,
		Groups: map[int64]GroupRule{
			-100: {RequireMention: &perGroupFalse},
		},
	}}
	assert.False(t, d.requireMentionForGroup(-100))
	// Other group still uses global.
	assert.True(t, d.requireMentionForGroup(-200))
}

func TestRequireMentionForGroup_WildcardFallback(t *testing.T) {
	wildcardFalse := false
	d := &Driver{cfg: Config{
		Groups: map[int64]GroupRule{
			-1: {RequireMention: &wildcardFalse},
		},
	}}
	// Any group should use the wildcard.
	assert.False(t, d.requireMentionForGroup(-500))
}

func TestIsBotMentioned_EntityMention(t *testing.T) {
	d := &Driver{botUsername: "mybot"}
	msg := &message{
		Text: "@mybot hello",
		Entities: []entity{
			{Type: "mention", Offset: 0, Length: 6},
		},
	}
	assert.True(t, d.isBotMentioned(msg))
}

func TestIsBotMentioned_NoMention(t *testing.T) {
	d := &Driver{botUsername: "mybot"}
	msg := &message{
		Text:     "hello world",
		Entities: nil,
	}
	assert.False(t, d.isBotMentioned(msg))
}

func TestIsBotMentioned_PrefixFallback(t *testing.T) {
	d := &Driver{botUsername: "mybot"}
	msg := &message{
		Text:     "@mybot hello",
		Entities: nil,
	}
	assert.True(t, d.isBotMentioned(msg))
}

func TestIsBotMentioned_WrongBot(t *testing.T) {
	d := &Driver{botUsername: "mybot"}
	msg := &message{
		Text: "@otherbot hello",
		Entities: []entity{
			{Type: "mention", Offset: 0, Length: 9},
		},
	}
	assert.False(t, d.isBotMentioned(msg))
}

func TestStripMention(t *testing.T) {
	d := &Driver{botUsername: "testbot"}
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"with mention", "@testbot hello", "hello"},
		{"case insensitive", "@TestBot world", "world"},
		{"no mention", "just text", "just text"},
		{"mention in middle", "hi @testbot there", "hi @testbot there"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, d.stripMention(tt.input))
		})
	}
}

func TestAddAllowedUser(t *testing.T) {
	d := &Driver{cfg: Config{AllowFrom: []int64{100}}}
	d.AddAllowedUser(200)
	assert.Contains(t, d.cfg.AllowFrom, int64(200))
	// Adding again should not duplicate.
	d.AddAllowedUser(200)
	count := 0
	for _, id := range d.cfg.AllowFrom {
		if id == 200 {
			count++
		}
	}
	assert.Equal(t, 1, count)
}

func TestSendNotification(t *testing.T) {
	var gotChatID, gotText string
	mux := http.NewServeMux()
	mux.HandleFunc("/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotChatID = r.FormValue("chat_id")
		gotText = r.FormValue("text")
		fmt.Fprint(w, `{"ok":true}`)
	})

	d, _ := newTestDriver(t, mux)
	err := d.SendNotification(context.Background(), 12345, "You are approved!")
	assert.NoError(t, err)
	assert.Equal(t, "12345", gotChatID)
	assert.Equal(t, "You are approved!", gotText)
}

func TestProcessUpdate_CallbackQuery(t *testing.T) {
	var handled bool
	var gotText string
	h := &testHandler{fn: func(_ context.Context, msg channel.Message, _ channel.Responder) {
		handled = true
		gotText = msg.Text
	}}

	mux := http.NewServeMux()
	mux.HandleFunc("/answerCallbackQuery", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	})

	d, _ := newTestDriver(t, mux)
	d.cfg.DMPolicy = "open"

	upd := update{
		UpdateID: 1,
		CallbackQuery: &callbackQuery{
			ID:   "cq-1",
			From: &user{ID: 42},
			Message: &message{
				MessageID: 10,
				Chat:      chat{ID: 100, Type: "private"},
			},
			Data: "/new",
		},
	}

	d.processUpdate(context.Background(), upd, h)
	assert.True(t, handled)
	assert.Equal(t, "/new", gotText)
}

func TestProcessUpdate_CallbackQuery_NilMessage(t *testing.T) {
	var handled bool
	h := &testHandler{fn: func(_ context.Context, _ channel.Message, _ channel.Responder) {
		handled = true
	}}

	d, _ := newTestDriver(t, http.NewServeMux())
	upd := update{
		CallbackQuery: &callbackQuery{
			ID:      "cq-2",
			Message: nil,
		},
	}

	d.processUpdate(context.Background(), upd, h)
	assert.False(t, handled)
}

func TestWithRequestTimeout_ZeroDuration(t *testing.T) {
	ctx := context.Background()
	newCtx, cancel := withRequestTimeout(ctx, 0)
	defer cancel()
	// Should be the same context (no timeout applied).
	assert.Equal(t, ctx, newCtx)
}

func TestWithRequestTimeout_PositiveDuration(t *testing.T) {
	ctx := context.Background()
	newCtx, cancel := withRequestTimeout(ctx, 5*time.Second)
	defer cancel()
	deadline, ok := newCtx.Deadline()
	assert.True(t, ok)
	assert.True(t, deadline.After(time.Now()))
}

func TestLongPollRequestTimeout_Zero(t *testing.T) {
	result := longPollRequestTimeout(0)
	assert.Equal(t, defaultHTTPTimeout, result)
}

func TestLongPollRequestTimeout_Positive(t *testing.T) {
	result := longPollRequestTimeout(30)
	expected := 30*time.Second + pollHTTPTimeoutBuffer
	assert.Equal(t, expected, result)
}

func TestShouldRespond_PrivateOpen(t *testing.T) {
	d := &Driver{cfg: Config{DMPolicy: "open"}}
	result := d.checkAccess("private", 100, 42)
	assert.Equal(t, accessAllowed, result)
}

func TestShouldRespond_PrivateAllowlist_Denied(t *testing.T) {
	d := &Driver{cfg: Config{DMPolicy: "allowlist", AllowFrom: []int64{100}}}
	result := d.checkAccess("private", 200, 42)
	assert.Equal(t, accessDenied, result)
}

func TestShouldRespond_PrivateAllowlist_Allowed(t *testing.T) {
	d := &Driver{cfg: Config{DMPolicy: "allowlist", AllowFrom: []int64{42}}}
	result := d.checkAccess("private", 200, 42)
	assert.Equal(t, accessAllowed, result)
}

func TestShouldRespond_PrivatePairing_NoMatch(t *testing.T) {
	d := &Driver{cfg: Config{DMPolicy: "pairing", AllowFrom: []int64{100}}}
	result := d.checkAccess("private", 200, 42)
	assert.Equal(t, accessPairing, result)
}

func TestShouldRespond_PrivatePairing_Match(t *testing.T) {
	d := &Driver{cfg: Config{DMPolicy: "pairing", AllowFrom: []int64{42}}}
	result := d.checkAccess("private", 200, 42)
	assert.Equal(t, accessAllowed, result)
}
