// Package app contains top-level application workflows.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Rememorio/clawdex/internal/channel"
	"github.com/Rememorio/clawdex/internal/channel/qqbot"
	"github.com/Rememorio/clawdex/internal/channel/telegram"
	"github.com/Rememorio/clawdex/internal/channel/wecom"
	"github.com/Rememorio/clawdex/internal/channel/weixin"
	"github.com/Rememorio/clawdex/internal/codex"
	"github.com/Rememorio/clawdex/internal/config"
	"github.com/Rememorio/clawdex/internal/daemon"
	"github.com/Rememorio/clawdex/internal/gateway"
	"github.com/Rememorio/clawdex/internal/logger"
	"github.com/Rememorio/clawdex/internal/pairing"
	"github.com/Rememorio/clawdex/internal/server"
)

const (
	defaultCodexTraceLogName = "codex.log"
	codexTraceDisabledOff    = "off"
	codexTraceDisabledNone   = "none"
)

func resolveCodexTracePath(rawPath, dataDir string) (string, bool, error) {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return filepath.Join(dataDir, defaultCodexTraceLogName), true, nil
	}

	switch strings.ToLower(trimmed) {
	case codexTraceDisabledOff, codexTraceDisabledNone:
		return "", false, nil
	}

	if filepath.IsAbs(trimmed) {
		return trimmed, true, nil
	}
	if dataDir == "" {
		return "", false, fmt.Errorf("resolve codex trace path: missing data directory")
	}
	return filepath.Join(dataDir, trimmed), true, nil
}

func openCodexTraceLogger(rawPath, dataDir string) (
	*codex.TraceLogger,
	*os.File,
	string,
	error,
) {
	path, enabled, err := resolveCodexTracePath(rawPath, dataDir)
	if err != nil || !enabled {
		return nil, nil, path, err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, path, fmt.Errorf("create codex log directory: %w", err)
	}

	file, err := os.OpenFile(
		path,
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		0o644,
	)
	if err != nil {
		return nil, nil, path, fmt.Errorf("open codex log file: %w", err)
	}
	return codex.NewTraceLogger(file), file, path, nil
}

// RunGateway starts the HTTP server and channel gateway in the foreground.
// It handles signal-based shutdown (SIGINT, SIGTERM), writes a PID file,
// and cleans it up on exit.
func RunGateway() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Initialize logger from config.
	logger.Configure(logger.Config{
		Level:  cfg.Logging.Level,
		Format: cfg.Logging.Format,
	})

	// Write PID file so `gateway stop` can find this process.
	if err := daemon.WritePID(); err != nil {
		return fmt.Errorf("write PID file: %w", err)
	}
	defer daemon.RemovePID()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	dataDir, err := daemon.DataDir()
	if err != nil {
		return fmt.Errorf("resolve data directory: %w", err)
	}

	codexTraceLogger, codexTraceFile, codexTracePath, err := openCodexTraceLogger(
		cfg.Logging.CodexFile,
		dataDir,
	)
	if err != nil {
		return err
	}
	if codexTraceFile != nil {
		defer codexTraceFile.Close()
		logger.Info("codex trace logging enabled", "path", codexTracePath)
	}

	codexClient := &codex.Client{
		WorkDir:      cfg.Codex.WorkDir,
		Timeout:      cfg.Codex.CommandTimeout,
		Sandbox:      cfg.Codex.Sandbox,
		GroupSandbox: cfg.Codex.GroupSandbox,
		SoulContent:  cfg.Codex.SoulContent,
		Store:        codex.NewSessionStore(filepath.Join(dataDir, "sessions.json")),
		Trace:        codexTraceLogger,
	}

	// Populate per-instance SOUL overrides from Telegram configs.
	for _, tgCfg := range cfg.Telegram {
		if tgCfg.Enabled && tgCfg.SoulContent != "" {
			if codexClient.SoulOverrides == nil {
				codexClient.SoulOverrides = make(map[string]string)
			}
			codexClient.SoulOverrides[tgCfg.Name] = tgCfg.SoulContent
		}
	}

	// Populate per-instance SOUL overrides from WeCom configs.
	for _, wcCfg := range cfg.WeCom {
		if wcCfg.Enabled && wcCfg.SoulContent != "" {
			if codexClient.SoulOverrides == nil {
				codexClient.SoulOverrides = make(map[string]string)
			}
			codexClient.SoulOverrides[wcCfg.Name] = wcCfg.SoulContent
		}
	}

	// Populate per-instance SOUL overrides from Weixin configs.
	for _, wxCfg := range cfg.Weixin {
		if wxCfg.Enabled && wxCfg.SoulContent != "" {
			if codexClient.SoulOverrides == nil {
				codexClient.SoulOverrides = make(map[string]string)
			}
			codexClient.SoulOverrides[wxCfg.Name] = wxCfg.SoulContent
		}
	}

	// Determine streaming mode (use first enabled telegram channel's setting).
	streamingMode := "partial"
	for _, tgCfg := range cfg.Telegram {
		if tgCfg.Enabled {
			streamingMode = tgCfg.Streaming
			break
		}
	}

	gw := gateway.New(codexClient, 4, streamingMode)

	pairingStore := pairing.NewStore(30 * time.Minute)
	var routes []server.RouteHandler
	var drivers []channel.Driver
	approvers := map[string]*channelApprover{}
	tgNotifiers := map[string]*telegram.Driver{}

	for _, tgCfg := range cfg.Telegram {
		if !tgCfg.Enabled {
			logger.Info("telegram channel is disabled, skipping", "name", tgCfg.Name)
			continue
		}

		var groups map[int64]telegram.GroupRule
		if tgCfg.Groups != nil {
			groups = make(map[int64]telegram.GroupRule, len(tgCfg.Groups))
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
		logger.Info("telegram channel enabled", "name", tgCfg.Name)
	}

	for _, wcCfg := range cfg.WeCom {
		if !wcCfg.Enabled {
			continue
		}

		var groups map[string]wecom.GroupRule
		if wcCfg.Groups != nil {
			groups = make(map[string]wecom.GroupRule, len(wcCfg.Groups))
			for k, v := range wcCfg.Groups {
				groups[k] = wecom.GroupRule{Enabled: v.Enabled, AllowFrom: v.AllowFrom}
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

		// Only register HTTP route in webhook mode.
		if wcCfg.ConnectionMode != "websocket" {
			routes = append(routes, server.RouteHandler{
				Pattern: wcCfg.WebhookPath,
				Handler: wecomDriver.Handler(),
			})
		}
		// Register the gateway as card event handler so button clicks
		// are processed synchronously (required by WeCom's timeout).
		wecomDriver.SetCardEventHandler(gw)
		drivers = append(drivers, wecomDriver)
		if wcCfg.ConnectionMode == "websocket" {
			logger.Info("wecom channel enabled in websocket mode", "name", wcCfg.Name, "bot", wcCfg.BotID)
		} else {
			logger.Info("wecom channel enabled on webhook path", "name", wcCfg.Name, "path", wcCfg.WebhookPath)
		}
	}

	for _, wxCfg := range cfg.Weixin {
		if !wxCfg.Enabled {
			logger.Info("weixin channel is disabled, skipping", "name", wxCfg.Name)
			continue
		}
		if wxCfg.Token == "" {
			logger.Warn("weixin channel has no token, skipping (run 'clawdex weixin login' to authenticate)", "name", wxCfg.Name)
			continue
		}

		wxDriver := weixin.New(weixin.Config{
			Name:           wxCfg.Name,
			BaseURL:        wxCfg.BaseURL,
			Token:          wxCfg.Token,
			TextChunkLimit: wxCfg.TextChunkLimit,
			DMPolicy:       wxCfg.DMPolicy,
			AllowFrom:      wxCfg.AllowFrom,
		}, pairingStore)

		if wxCfg.DMPolicy == "pairing" {
			approvers[wxCfg.Name] = &channelApprover{
				addAllowedString: wxDriver.AddAllowedUser,
			}
		}
		drivers = append(drivers, wxDriver)
		logger.Info("weixin channel enabled", "name", wxCfg.Name)
	}

	for _, qqCfg := range cfg.QQBot {
		if !qqCfg.Enabled {
			logger.Info("qqbot channel is disabled, skipping", "name", qqCfg.Name)
			continue
		}
		if qqCfg.AppID == "" || qqCfg.ClientSecret == "" {
			logger.Warn("qqbot channel missing app_id or client_secret, skipping", "name", qqCfg.Name)
			continue
		}

		qqDriver := qqbot.New(qqbot.Config{
			Name:           qqCfg.Name,
			AppID:          qqCfg.AppID,
			ClientSecret:   qqCfg.ClientSecret,
			DMPolicy:       qqCfg.DMPolicy,
			AllowFrom:      qqCfg.AllowFrom,
			GroupPolicy:    qqCfg.GroupPolicy,
			GroupAllowFrom: qqCfg.GroupAllowFrom,
			TextChunkLimit: qqCfg.TextChunkLimit,
		}, pairingStore)

		if qqCfg.DMPolicy == "pairing" {
			approvers[qqCfg.Name] = &channelApprover{
				addAllowedString: qqDriver.AddAllowedUser,
			}
		}
		drivers = append(drivers, qqDriver)
		logger.Info("qqbot channel enabled", "name", qqCfg.Name)
	}

	// Populate per-instance SOUL overrides from QQ Bot configs.
	for _, qqCfg := range cfg.QQBot {
		if qqCfg.Enabled && qqCfg.SoulContent != "" {
			if codexClient.SoulOverrides == nil {
				codexClient.SoulOverrides = make(map[string]string)
			}
			codexClient.SoulOverrides[qqCfg.Name] = qqCfg.SoulContent
		}
	}

	if len(approvers) > 0 {
		routes = append(routes,
			server.RouteHandler{Pattern: "/pairing/list", Handler: pairingListHandler(pairingStore)},
			server.RouteHandler{Pattern: "/pairing/approve", Handler: pairingApproveHandler(pairingStore, approvers, tgNotifiers)},
		)
	}

	if len(drivers) == 0 {
		return fmt.Errorf("no channel drivers enabled; enable telegram, wecom, weixin, or qqbot")
	}

	httpServer := server.New(cfg.Server.Address, routes...)
	httpErrCh := make(chan error, 1)
	go func() {
		if err := httpServer.Start(); err != nil {
			httpErrCh <- err
			cancel()
		}
	}()

	logger.Info("gateway server listening", "address", cfg.Server.Address)

	gatewayErr := gw.Run(ctx, drivers...)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("http shutdown failed", "error", err)
	}

	select {
	case err := <-httpErrCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("http server error: %w", err)
		}
	default:
	}

	if gatewayErr != nil && !errors.Is(gatewayErr, context.Canceled) {
		return gatewayErr
	}
	return nil
}
