package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarshalTextContent(t *testing.T) {
	got, err := marshalTextContent(`hello "feishu"`)
	require.NoError(t, err)

	var parsed textContent
	require.NoError(t, json.Unmarshal([]byte(got), &parsed))
	assert.Equal(t, `hello "feishu"`, parsed.Text)
}

func TestLarkMessageAPI_CreateReaction(t *testing.T) {
	var gotMethod, gotPath, gotEmojiType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			fmt.Fprint(w, `{"code":0,"msg":"ok","tenant_access_token":"tenant-token","expire":7200}`)
		case "/open-apis/im/v1/messages/om_msg/reactions":
			gotMethod = r.Method
			gotPath = r.URL.Path
			var body struct {
				ReactionType struct {
					EmojiType string `json:"emoji_type"`
				} `json:"reaction_type"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			gotEmojiType = body.ReactionType.EmojiType
			fmt.Fprint(w, `{"code":0,"msg":"ok","data":{"reaction_id":"reaction_1"}}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	api := newMessageAPI("cli_test", "secret_test", server.URL, "fs")
	reactionID, err := api.CreateReaction(context.Background(), "om_msg", "Typing")
	require.NoError(t, err)

	assert.Equal(t, "reaction_1", reactionID)
	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/open-apis/im/v1/messages/om_msg/reactions", gotPath)
	assert.Equal(t, "Typing", gotEmojiType)
}

func TestLarkMessageAPI_DeleteReaction(t *testing.T) {
	var gotMethod, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			fmt.Fprint(w, `{"code":0,"msg":"ok","tenant_access_token":"tenant-token","expire":7200}`)
		case "/open-apis/im/v1/messages/om_msg/reactions/reaction_1":
			gotMethod = r.Method
			gotPath = r.URL.Path
			fmt.Fprint(w, `{"code":0,"msg":"ok","data":{"reaction_id":"reaction_1"}}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	api := newMessageAPI("cli_test", "secret_test", server.URL, "fs")
	err := api.DeleteReaction(context.Background(), "om_msg", "reaction_1")
	require.NoError(t, err)

	assert.Equal(t, http.MethodDelete, gotMethod)
	assert.Equal(t, "/open-apis/im/v1/messages/om_msg/reactions/reaction_1", gotPath)
}
