package app

import (
	"testing"
	"time"

	"github.com/Rememorio/clawdex/internal/channel"
	"github.com/Rememorio/clawdex/internal/channel/telegram"
	"github.com/Rememorio/clawdex/internal/channel/wecom"
	"github.com/Rememorio/clawdex/internal/config"
	"github.com/Rememorio/clawdex/internal/pairing"
	"github.com/Rememorio/clawdex/internal/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildGatewayComponents reproduces the channel assembly logic from
// RunGateway so we can test the wiring in isolation without starting
// real servers or requiring a config file on disk.
func buildGatewayComponents(
	cfg *config.Config,
) (
	drivers []channel.Driver,
	routes []server.RouteHandler,
	approvers map[string]*channelApprover,
	tgNotifiers map[string]*telegram.Driver,
) {
	pairingStore := pairing.NewStore(30 * time.Minute)
	approvers = map[string]*channelApprover{}
	tgNotifiers = map[string]*telegram.Driver{}

	for _, tgCfg := range cfg.Telegram {
		if !tgCfg.Enabled {
			continue
		}

		var groups map[int64]telegram.GroupRule
		if tgCfg.Groups != nil {
			groups = make(
				map[int64]telegram.GroupRule, len(tgCfg.Groups),
			)
			for k, v := range tgCfg.Groups {
				groups[k] = telegram.GroupRule{
					Enabled:        v.Enabled,
					AllowFrom:      v.AllowFrom,
					RequireMention: v.RequireMention,
				}
			}
		}

		driver := telegram.New(telegram.Config{
			Name:                tgCfg.Name,
			BotToken:            tgCfg.BotToken,
			PollTimeout:         tgCfg.PollTimeout,
			StartupProbeTimeout: tgCfg.StartupProbeTimeout,
			ChunkMode:           tgCfg.ChunkMode,
			TextChunkLimit:      tgCfg.TextChunkLimit,
			DMPolicy:            tgCfg.DMPolicy,
			AllowFrom:           tgCfg.AllowFrom,
			GroupPolicy:         tgCfg.GroupPolicy,
			GroupAllowFrom:      tgCfg.GroupAllowFrom,
			Groups:              groups,
			RequireMention:      tgCfg.RequireMention,
		}, pairingStore)

		if tgCfg.DMPolicy == "pairing" {
			approvers[tgCfg.Name] = &channelApprover{
				addAllowedInt64: driver.AddAllowedUser,
			}
		}
		drivers = append(drivers, driver)
		tgNotifiers[tgCfg.Name] = driver
	}

	for _, wcCfg := range cfg.WeCom {
		if !wcCfg.Enabled {
			continue
		}

		var groups map[string]wecom.GroupRule
		if wcCfg.Groups != nil {
			groups = make(
				map[string]wecom.GroupRule, len(wcCfg.Groups),
			)
			for k, v := range wcCfg.Groups {
				groups[k] = wecom.GroupRule{
					Enabled:   v.Enabled,
					AllowFrom: v.AllowFrom,
				}
			}
		}

		wecomDriver := wecom.New(wecom.Config{
			Name:              wcCfg.Name,
			Token:             wcCfg.Token,
			EncodingAESKey:    wcCfg.EncodingAESKey,
			WebhookPath:       wcCfg.WebhookPath,
			TextChunkLimit:    wcCfg.TextChunkLimit,
			DMPolicy:          wcCfg.DMPolicy,
			AllowFrom:         wcCfg.AllowFrom,
			GroupPolicy:       wcCfg.GroupPolicy,
			GroupAllowFrom:    wcCfg.GroupAllowFrom,
			Groups:            groups,
			ConnectionMode:    wcCfg.ConnectionMode,
			BotID:             wcCfg.BotID,
			Secret:            wcCfg.Secret,
			WSURL:             wcCfg.WSURL,
			HeartbeatInterval: wcCfg.HeartbeatInterval,
		}, pairingStore)

		if wcCfg.DMPolicy == "pairing" {
			approvers[wcCfg.Name] = &channelApprover{
				addAllowedString: wecomDriver.AddAllowedUser,
			}
		}

		if wcCfg.ConnectionMode != "websocket" {
			routes = append(routes, server.RouteHandler{
				Pattern: wcCfg.WebhookPath,
				Handler: wecomDriver.Handler(),
			})
		}
		drivers = append(drivers, wecomDriver)
	}

	if len(approvers) > 0 {
		routes = append(routes,
			server.RouteHandler{
				Pattern: "/pairing/list",
				Handler: pairingListHandler(pairingStore),
			},
			server.RouteHandler{
				Pattern: "/pairing/approve",
				Handler: pairingApproveHandler(
					pairingStore, approvers, tgNotifiers,
				),
			},
		)
	}

	return drivers, routes, approvers, tgNotifiers
}

// ── Tests ──

func TestResolveCodexTracePath_Default(t *testing.T) {
	path, enabled, err := resolveCodexTracePath("", "/tmp/clawdex")
	require.NoError(t, err)
	assert.True(t, enabled)
	assert.Equal(t, "/tmp/clawdex/codex.log", path)
}

func TestResolveCodexTracePath_Disabled(t *testing.T) {
	path, enabled, err := resolveCodexTracePath("off", "/tmp/clawdex")
	require.NoError(t, err)
	assert.False(t, enabled)
	assert.Equal(t, "", path)
}

func TestResolveCodexTracePath_Relative(t *testing.T) {
	path, enabled, err := resolveCodexTracePath("trace/custom.log", "/tmp/clawdex")
	require.NoError(t, err)
	assert.True(t, enabled)
	assert.Equal(t, "/tmp/clawdex/trace/custom.log", path)
}

func TestBuildGateway_SingleTelegram(t *testing.T) {
	cfg := &config.Config{
		Telegram: []config.TelegramConfig{
			{
				Name:     "tg-main",
				BotToken: "fake-token",
				Enabled:  true,
				DMPolicy: "pairing",
			},
		},
	}

	drivers, routes, approvers, notifiers := buildGatewayComponents(cfg)

	assert.Len(t, drivers, 1)
	assert.Equal(t, "tg-main", drivers[0].Name())

	// Approver should be registered for pairing policy.
	require.Contains(t, approvers, "tg-main")
	assert.NotNil(t, approvers["tg-main"].addAllowedInt64)
	assert.Nil(t, approvers["tg-main"].addAllowedString)

	// Notifier should be registered.
	assert.Contains(t, notifiers, "tg-main")

	// Pairing routes should be registered.
	hasListRoute := false
	hasApproveRoute := false
	for _, r := range routes {
		if r.Pattern == "/pairing/list" {
			hasListRoute = true
		}
		if r.Pattern == "/pairing/approve" {
			hasApproveRoute = true
		}
	}
	assert.True(t, hasListRoute, "expected /pairing/list route")
	assert.True(t, hasApproveRoute, "expected /pairing/approve route")
}

func TestBuildGateway_TelegramOpenPolicy(t *testing.T) {
	cfg := &config.Config{
		Telegram: []config.TelegramConfig{
			{
				Name:     "tg-open",
				BotToken: "fake-token",
				Enabled:  true,
				DMPolicy: "open",
			},
		},
	}

	drivers, routes, approvers, notifiers := buildGatewayComponents(cfg)

	assert.Len(t, drivers, 1)

	// Open policy should not register an approver.
	assert.NotContains(t, approvers, "tg-open")

	// Notifier is still registered regardless of policy.
	assert.Contains(t, notifiers, "tg-open")

	// No pairing routes when no approvers.
	for _, r := range routes {
		assert.NotEqual(t, "/pairing/list", r.Pattern)
		assert.NotEqual(t, "/pairing/approve", r.Pattern)
	}
}

func TestBuildGateway_DisabledTelegramSkipped(t *testing.T) {
	cfg := &config.Config{
		Telegram: []config.TelegramConfig{
			{
				Name:     "disabled-tg",
				BotToken: "tok",
				Enabled:  false,
				DMPolicy: "pairing",
			},
		},
	}

	drivers, _, approvers, notifiers := buildGatewayComponents(cfg)

	assert.Empty(t, drivers)
	assert.Empty(t, approvers)
	assert.Empty(t, notifiers)
}

func TestBuildGateway_SingleWeComWebhook(t *testing.T) {
	cfg := &config.Config{
		WeCom: []config.WeComConfig{
			{
				Name:           "wc-1",
				Token:          "tok",
				EncodingAESKey: "key",
				WebhookPath:    "/wecom/cb",
				Enabled:        true,
				DMPolicy:       "pairing",
				ConnectionMode: "webhook",
			},
		},
	}

	drivers, routes, approvers, _ := buildGatewayComponents(cfg)

	assert.Len(t, drivers, 1)
	assert.Equal(t, "wc-1", drivers[0].Name())

	// Approver should be registered.
	require.Contains(t, approvers, "wc-1")
	assert.Nil(t, approvers["wc-1"].addAllowedInt64)
	assert.NotNil(t, approvers["wc-1"].addAllowedString)

	// Webhook route should be registered.
	hasWebhook := false
	for _, r := range routes {
		if r.Pattern == "/wecom/cb" {
			hasWebhook = true
		}
	}
	assert.True(t, hasWebhook, "expected webhook route /wecom/cb")
}

func TestBuildGateway_WeComWebsocketNoRoute(t *testing.T) {
	cfg := &config.Config{
		WeCom: []config.WeComConfig{
			{
				Name:           "wc-ws",
				BotID:          "bot123",
				Secret:         "secret",
				Enabled:        true,
				DMPolicy:       "pairing",
				ConnectionMode: "websocket",
			},
		},
	}

	drivers, routes, approvers, _ := buildGatewayComponents(cfg)

	assert.Len(t, drivers, 1)
	require.Contains(t, approvers, "wc-ws")

	// No webhook route for websocket mode.
	for _, r := range routes {
		assert.NotEqual(t, "/wecom/cb", r.Pattern,
			"websocket mode should not register a webhook route")
	}

	// But pairing routes should still exist.
	hasPairing := false
	for _, r := range routes {
		if r.Pattern == "/pairing/list" {
			hasPairing = true
		}
	}
	assert.True(t, hasPairing)
}

func TestBuildGateway_MultiInstance(t *testing.T) {
	trueVal := true
	cfg := &config.Config{
		Telegram: []config.TelegramConfig{
			{
				Name:     "tg-1",
				BotToken: "tok1",
				Enabled:  true,
				DMPolicy: "pairing",
			},
			{
				Name:     "tg-2",
				BotToken: "tok2",
				Enabled:  true,
				DMPolicy: "allowlist",
			},
		},
		WeCom: []config.WeComConfig{
			{
				Name:           "wc-1",
				Token:          "tok",
				EncodingAESKey: "key",
				WebhookPath:    "/wc/1",
				Enabled:        true,
				DMPolicy:       "pairing",
				ConnectionMode: "webhook",
			},
		},
	}
	// Suppress RequireMention nil deref by setting it.
	cfg.Telegram[0].RequireMention = &trueVal

	drivers, routes, approvers, notifiers := buildGatewayComponents(cfg)

	// 2 telegram + 1 wecom = 3 drivers.
	assert.Len(t, drivers, 3)

	// Only pairing-policy channels get approvers.
	assert.Contains(t, approvers, "tg-1")
	assert.NotContains(t, approvers, "tg-2")
	assert.Contains(t, approvers, "wc-1")

	// Both telegram drivers are notifiers.
	assert.Contains(t, notifiers, "tg-1")
	assert.Contains(t, notifiers, "tg-2")

	// Webhook route for wc-1.
	hasWC := false
	for _, r := range routes {
		if r.Pattern == "/wc/1" {
			hasWC = true
		}
	}
	assert.True(t, hasWC)
}

func TestBuildGateway_NoDriversEmpty(t *testing.T) {
	cfg := &config.Config{}

	drivers, routes, approvers, notifiers := buildGatewayComponents(cfg)

	assert.Empty(t, drivers)
	assert.Empty(t, routes)
	assert.Empty(t, approvers)
	assert.Empty(t, notifiers)
}

func TestBuildGateway_TelegramGroupRulesPropagated(t *testing.T) {
	trueVal := true
	falseVal := false
	cfg := &config.Config{
		Telegram: []config.TelegramConfig{
			{
				Name:     "tg-groups",
				BotToken: "tok",
				Enabled:  true,
				DMPolicy: "open",
				Groups: map[int64]config.TelegramGroupRule{
					-100: {
						Enabled:        &trueVal,
						AllowFrom:      []int64{1, 2},
						RequireMention: &falseVal,
					},
				},
			},
		},
	}

	drivers, _, _, _ := buildGatewayComponents(cfg)
	require.Len(t, drivers, 1)
	assert.Equal(t, "tg-groups", drivers[0].Name())
}

func TestBuildGateway_WeComGroupRulesPropagated(t *testing.T) {
	trueVal := true
	cfg := &config.Config{
		WeCom: []config.WeComConfig{
			{
				Name:           "wc-grp",
				Token:          "tok",
				EncodingAESKey: "key",
				WebhookPath:    "/wc",
				Enabled:        true,
				DMPolicy:       "open",
				ConnectionMode: "webhook",
				Groups: map[string]config.WeComGroupRule{
					"room1": {
						Enabled:   &trueVal,
						AllowFrom: []string{"alice"},
					},
				},
			},
		},
	}

	drivers, _, _, _ := buildGatewayComponents(cfg)
	require.Len(t, drivers, 1)
	assert.Equal(t, "wc-grp", drivers[0].Name())
}

func TestBuildGateway_AllDisabledNoDrivers(t *testing.T) {
	cfg := &config.Config{
		Telegram: []config.TelegramConfig{
			{Name: "d1", Enabled: false},
		},
		WeCom: []config.WeComConfig{
			{Name: "d2", Enabled: false},
		},
	}

	drivers, routes, approvers, notifiers := buildGatewayComponents(cfg)

	assert.Empty(t, drivers)
	assert.Empty(t, routes)
	assert.Empty(t, approvers)
	assert.Empty(t, notifiers)
}
