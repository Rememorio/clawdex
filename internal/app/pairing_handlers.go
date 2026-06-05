package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Rememorio/clawdex/internal/channel/telegram"
	"github.com/Rememorio/clawdex/internal/onboard"
	"github.com/Rememorio/clawdex/internal/pairing"
)

const notificationTimeout = 10 * time.Second

type pairingListEntry struct {
	Code      string    `json:"code"`
	Channel   string    `json:"channel"`
	SenderID  int64     `json:"sender_id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
}

type approveResponse struct {
	OK       bool   `json:"ok"`
	Channel  string `json:"channel,omitempty"`
	SenderID int64  `json:"sender_id,omitempty"`
	Username string `json:"username,omitempty"`
	Error    string `json:"error,omitempty"`
}

// channelApprover holds callbacks for updating a channel's runtime allowlist.
type channelApprover struct {
	addAllowedInt64  func(int64)  // for telegram (nil for wecom)
	addAllowedString func(string) // for wecom (nil for telegram)
}

func pairingListHandler(store *pairing.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		reqs := store.List()
		entries := make([]pairingListEntry, 0, len(reqs))
		for _, r := range reqs {
			entries = append(entries, pairingListEntry{
				Code:      r.Code,
				Channel:   r.Channel,
				SenderID:  r.SenderID,
				Username:  r.SenderUsername,
				CreatedAt: r.CreatedAt,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(entries)
	}
}

func pairingApproveHandler(store *pairing.Store, approvers map[string]*channelApprover, tgNotifiers map[string]*telegram.Driver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		code := strings.TrimSpace(r.URL.Query().Get("code"))
		if code == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(approveResponse{Error: "missing code parameter"})
			return
		}

		req, ok := store.Approve(code)
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(approveResponse{Error: "code not found or expired"})
			return
		}

		// Update runtime allowlist for the appropriate channel.
		if approver, ok := approvers[req.Channel]; ok {
			if approver.addAllowedInt64 != nil {
				approver.addAllowedInt64(req.SenderID)
			}
			if approver.addAllowedString != nil {
				approver.addAllowedString(req.SenderIDStr)
			}
		}

		// Persist to config file (best-effort).
		if err := persistAllowFrom(req.Channel, req.SenderID, req.SenderIDStr); err != nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(approveResponse{
				OK:       true,
				Channel:  req.Channel,
				SenderID: req.SenderID,
				Username: req.SenderUsername,
				Error:    "approved but failed to persist: " + err.Error(),
			})
		} else {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(approveResponse{
				OK:       true,
				Channel:  req.Channel,
				SenderID: req.SenderID,
				Username: req.SenderUsername,
			})
		}

		// Notify the user (best-effort). Only Telegram supports push notification.
		if drv, ok := tgNotifiers[req.Channel]; ok && drv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), notificationTimeout)
			defer cancel()
			_ = drv.SendNotification(ctx, req.SenderID, "You've been approved! Send me a message.")
		}
	}
}

func persistAllowFrom(channel string, senderID int64, senderIDStr string) error {
	cfg, err := onboard.LoadFileConfig()
	if err != nil {
		return err
	}
	if changed, err := updateAllowFrom(cfg, channel, senderID, senderIDStr); err != nil {
		return err
	} else if !changed {
		return nil
	}
	return onboard.SaveFileConfig(cfg)
}

// updateAllowFrom appends the sender to the channel's allow_from list
// inside cfg. It returns true if the config was modified, false if the
// sender was already present or the channel was not found.
func updateAllowFrom(
	cfg *onboard.FileConfig,
	channel string,
	senderID int64,
	senderIDStr string,
) (bool, error) {
	if cfg.Channels == nil {
		cfg.Channels = make(map[string]json.RawMessage)
	}
	raw, exists := cfg.Channels[channel]
	if !exists || len(raw) == 0 {
		return false, nil
	}
	chType, _ := onboard.ChannelType(raw)
	switch chType {
	case "telegram":
		var ch onboard.TelegramChannelConfig
		if err := json.Unmarshal(raw, &ch); err != nil {
			return false, fmt.Errorf(
				"parse telegram config: %w", err,
			)
		}
		if slices.Contains(ch.AllowFrom, senderID) {
			return false, nil
		}
		ch.AllowFrom = append(ch.AllowFrom, senderID)
		data, _ := json.Marshal(ch)
		cfg.Channels[channel] = data
		return true, nil
	case "wecom":
		var ch onboard.WeComChannelConfig
		if err := json.Unmarshal(raw, &ch); err != nil {
			return false, fmt.Errorf(
				"parse wecom config: %w", err,
			)
		}
		if slices.Contains(ch.AllowFrom, senderIDStr) {
			return false, nil
		}
		ch.AllowFrom = append(ch.AllowFrom, senderIDStr)
		data, _ := json.Marshal(ch)
		cfg.Channels[channel] = data
		return true, nil
	case "weixin":
		var ch onboard.WeixinChannelConfig
		if err := json.Unmarshal(raw, &ch); err != nil {
			return false, fmt.Errorf(
				"parse weixin config: %w", err,
			)
		}
		if slices.Contains(ch.AllowFrom, senderIDStr) {
			return false, nil
		}
		ch.AllowFrom = append(ch.AllowFrom, senderIDStr)
		data, _ := json.Marshal(ch)
		cfg.Channels[channel] = data
		return true, nil
	}
	return false, nil
}
