// Package app contains top-level application workflows.
package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Rememorio/clawdex/internal/channel"
	"github.com/Rememorio/clawdex/internal/channel/feishu"
	"github.com/Rememorio/clawdex/internal/channel/qqbot"
	"github.com/Rememorio/clawdex/internal/channel/telegram"
	"github.com/Rememorio/clawdex/internal/channel/wecom"
	"github.com/Rememorio/clawdex/internal/channel/weixin"
	"github.com/Rememorio/clawdex/internal/codex"
	"github.com/Rememorio/clawdex/internal/config"
	cronjob "github.com/Rememorio/clawdex/internal/cron"
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

func addSoulSource(c *codex.Client, channelName, content, path string, override bool, appendText string) {
	if override && content != "" {
		if c.SoulOverrides == nil {
			c.SoulOverrides = make(map[string]string)
		}
		c.SoulOverrides[channelName] = content
	}
	if path != "" {
		if c.SoulOverridePaths == nil {
			c.SoulOverridePaths = make(map[string]string)
		}
		c.SoulOverridePaths[channelName] = path
	}
	if appendText != "" {
		if c.SoulAppends == nil {
			c.SoulAppends = make(map[string]string)
		}
		c.SoulAppends[channelName] = appendText
	}
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
		WorkDir:        cfg.Codex.WorkDir,
		Timeout:        cfg.Codex.CommandTimeout,
		Sandbox:        cfg.Codex.Sandbox,
		GroupSandbox:   cfg.Codex.GroupSandbox,
		SoulContent:    cfg.Codex.SoulContent,
		SoulPath:       cfg.Codex.SoulPath,
		Store:          codex.NewSessionStore(filepath.Join(dataDir, "sessions.json")),
		Trace:          codexTraceLogger,
		CronMCPEnabled: cfg.Cron.Enabled && cfg.Cron.MCPEnabled,
		GatewayURL:     gatewayLoopbackURL(cfg.Server.Address),
	}

	// Populate per-instance SOUL overrides from Telegram configs.
	for _, tgCfg := range cfg.Telegram {
		if tgCfg.Enabled {
			addSoulSource(codexClient, tgCfg.Name, tgCfg.SoulContent, tgCfg.SoulPath, tgCfg.SoulOverride, "")
		}
	}

	// Populate per-instance SOUL overrides from WeCom configs.
	for _, wcCfg := range cfg.WeCom {
		if wcCfg.Enabled {
			addSoulSource(codexClient, wcCfg.Name, wcCfg.SoulContent, wcCfg.SoulPath, wcCfg.SoulOverride, wcCfg.SoulAppend)
		}
	}

	// Populate per-instance SOUL overrides from Weixin configs.
	for _, wxCfg := range cfg.Weixin {
		if wxCfg.Enabled {
			addSoulSource(codexClient, wxCfg.Name, wxCfg.SoulContent, wxCfg.SoulPath, wxCfg.SoulOverride, "")
		}
	}

	// Populate per-instance SOUL overrides from QQ Bot configs.
	for _, qqCfg := range cfg.QQBot {
		if qqCfg.Enabled {
			addSoulSource(codexClient, qqCfg.Name, qqCfg.SoulContent, qqCfg.SoulPath, qqCfg.SoulOverride, "")
		}
	}

	// Populate per-instance SOUL overrides from Feishu configs.
	for _, fsCfg := range cfg.Feishu {
		if fsCfg.Enabled {
			addSoulSource(codexClient, fsCfg.Name, fsCfg.SoulContent, fsCfg.SoulPath, fsCfg.SoulOverride, "")
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
	if cfg.Cron.Enabled {
		cronSvc := cronjob.New(cronjob.Options{
			StorePath: cfg.Cron.StorePath,
			Enabled:   cfg.Cron.Enabled,
			Deliver:   gw.DeliverCron,
			RunAgent:  gw.RunCronAgent,
		})
		gw.SetCron(cronSvc)
	}

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

	for _, fsCfg := range cfg.Feishu {
		if !fsCfg.Enabled {
			logger.Info("feishu channel is disabled, skipping", "name", fsCfg.Name)
			continue
		}
		if fsCfg.AppID == "" || fsCfg.AppSecret == "" {
			logger.Warn("feishu channel missing app_id or app_secret, skipping", "name", fsCfg.Name)
			continue
		}

		var groups map[string]feishu.GroupRule
		if fsCfg.Groups != nil {
			groups = make(map[string]feishu.GroupRule, len(fsCfg.Groups))
			for k, v := range fsCfg.Groups {
				groups[k] = feishu.GroupRule{
					Enabled:        v.Enabled,
					AllowFrom:      v.AllowFrom,
					RequireMention: v.RequireMention,
				}
			}
		}

		fsDriver := feishu.New(feishu.Config{
			Name:           fsCfg.Name,
			AppID:          fsCfg.AppID,
			AppSecret:      fsCfg.AppSecret,
			BaseURL:        fsCfg.BaseURL,
			TextChunkLimit: fsCfg.TextChunkLimit,
			DMPolicy:       fsCfg.DMPolicy,
			AllowFrom:      fsCfg.AllowFrom,
			GroupPolicy:    fsCfg.GroupPolicy,
			GroupAllowFrom: fsCfg.GroupAllowFrom,
			Groups:         groups,
			RequireMention: fsCfg.RequireMention,
		}, pairingStore)

		if fsCfg.DMPolicy == "pairing" {
			approvers[fsCfg.Name] = &channelApprover{
				addAllowedString: fsDriver.AddAllowedUser,
			}
		}
		drivers = append(drivers, fsDriver)
		logger.Info("feishu channel enabled", "name", fsCfg.Name)
	}

	if len(approvers) > 0 {
		routes = append(routes,
			server.RouteHandler{Pattern: "/pairing/list", Handler: pairingListHandler(pairingStore)},
			server.RouteHandler{Pattern: "/pairing/approve", Handler: pairingApproveHandler(pairingStore, approvers, tgNotifiers)},
		)
	}
	if cfg.Cron.Enabled {
		routes = append(routes, gw.CronRoutes()...)
	}

	if len(drivers) == 0 {
		return fmt.Errorf("no channel drivers enabled; enable telegram, wecom, weixin, qqbot, or feishu")
	}
	for _, d := range drivers {
		if sender, ok := d.(channel.ProactiveSender); ok {
			gw.RegisterSender(sender)
		}
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

func gatewayLoopbackURL(address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return "http://127.0.0.1:8080"
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		if strings.HasPrefix(address, ":") {
			return "http://127.0.0.1" + address
		}
		if parsed, parseErr := url.Parse(address); parseErr == nil && parsed.Scheme != "" {
			return strings.TrimRight(address, "/")
		}
		return "http://" + address
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return "http://" + net.JoinHostPort(host, port)
}
