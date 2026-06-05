package app

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Rememorio/clawdex/internal/onboard"
	"github.com/Rememorio/clawdex/internal/secret"
)

// configEntry defines a typed getter/setter for a single config key.
type configEntry struct {
	get    func(*onboard.FileConfig) string
	set    func(*onboard.FileConfig, string) error
	secret bool // mask value in "config list"
}

// configKeys is the registry of non-channel dot-path config keys.
// Channel keys are resolved dynamically via channel name.
var configKeys = map[string]configEntry{
	// ── Codex ──
	"codex.workdir": {
		get: func(c *onboard.FileConfig) string { return c.Codex.WorkDir },
		set: func(c *onboard.FileConfig, v string) error { c.Codex.WorkDir = v; return nil },
	},
	"codex.timeout": {
		get: func(c *onboard.FileConfig) string { return c.Codex.Timeout },
		set: func(c *onboard.FileConfig, v string) error { c.Codex.Timeout = v; return nil },
	},
	"codex.sandbox": {
		get: func(c *onboard.FileConfig) string { return c.Codex.Sandbox },
		set: func(c *onboard.FileConfig, v string) error {
			if err := validateChoice(v, "read-only", "workspace-write", "danger-full-access"); err != nil {
				return err
			}
			c.Codex.Sandbox = v
			return nil
		},
	},
	"codex.group_sandbox": {
		get: func(c *onboard.FileConfig) string { return c.Codex.GroupSandbox },
		set: func(c *onboard.FileConfig, v string) error {
			if err := validateChoice(v, "read-only", "workspace-write", "danger-full-access"); err != nil {
				return err
			}
			c.Codex.GroupSandbox = v
			return nil
		},
	},

	// ── Gateway ──
	"gateway.address": {
		get: func(c *onboard.FileConfig) string { return c.Gateway.Address },
		set: func(c *onboard.FileConfig, v string) error { c.Gateway.Address = v; return nil },
	},

	// ── Logging ──
	"logging.level": {
		get: func(c *onboard.FileConfig) string { return c.Logging.Level },
		set: func(c *onboard.FileConfig, v string) error {
			if err := validateChoice(v, "debug", "info", "warn", "error"); err != nil {
				return err
			}
			c.Logging.Level = v
			return nil
		},
	},
	"logging.format": {
		get: func(c *onboard.FileConfig) string { return c.Logging.Format },
		set: func(c *onboard.FileConfig, v string) error {
			if err := validateChoice(v, "text", "json"); err != nil {
				return err
			}
			c.Logging.Format = v
			return nil
		},
	},
	"logging.codex_file": {
		get: func(c *onboard.FileConfig) string { return c.Logging.CodexFile },
		set: func(c *onboard.FileConfig, v string) error {
			c.Logging.CodexFile = v
			return nil
		},
	},

	// ── Meta (read-only) ──
	"meta.version": {
		get: func(c *onboard.FileConfig) string { return c.Meta.LastTouchedVersion },
		set: func(c *onboard.FileConfig, v string) error {
			return fmt.Errorf("meta.version is read-only")
		},
	},
	"meta.touched_at": {
		get: func(c *onboard.FileConfig) string { return c.Meta.LastTouchedAt },
		set: func(c *onboard.FileConfig, v string) error {
			return fmt.Errorf("meta.touched_at is read-only")
		},
	},
}

// telegramFieldGetter returns the value of a telegram channel field.
type telegramFieldGetter func(*onboard.TelegramChannelConfig) string

// telegramFieldSetter sets the value of a telegram channel field.
type telegramFieldSetter func(*onboard.TelegramChannelConfig, string) error

// telegramFields defines field accessors for Telegram channels.
var telegramFields = map[string]struct {
	get    telegramFieldGetter
	set    telegramFieldSetter
	secret bool
}{
	"type": {
		get: func(ch *onboard.TelegramChannelConfig) string { return ch.Type },
		set: func(ch *onboard.TelegramChannelConfig, v string) error {
			if err := validateChoice(v, "telegram"); err != nil {
				return err
			}
			ch.Type = v
			return nil
		},
	},
	"enabled": {
		get: func(ch *onboard.TelegramChannelConfig) string { return fmtBoolPtr(ch.Enabled) },
		set: func(ch *onboard.TelegramChannelConfig, v string) error {
			b, err := parseBool(v)
			if err != nil {
				return err
			}
			ch.Enabled = &b
			return nil
		},
	},
	"bot_token": {
		get:    func(ch *onboard.TelegramChannelConfig) string { return ch.BotToken },
		set:    func(ch *onboard.TelegramChannelConfig, v string) error { ch.BotToken = v; return nil },
		secret: true,
	},
	"dm_policy": {
		get: func(ch *onboard.TelegramChannelConfig) string { return ch.DMPolicy },
		set: func(ch *onboard.TelegramChannelConfig, v string) error {
			if err := validateChoice(v, "open", "pairing", "allowlist"); err != nil {
				return err
			}
			ch.DMPolicy = v
			return nil
		},
	},
	"chunk_mode": {
		get: func(ch *onboard.TelegramChannelConfig) string { return ch.ChunkMode },
		set: func(ch *onboard.TelegramChannelConfig, v string) error {
			if err := validateChoice(v, "length", "newline"); err != nil {
				return err
			}
			ch.ChunkMode = v
			return nil
		},
	},
	"text_chunk_limit": {
		get: func(ch *onboard.TelegramChannelConfig) string { return fmtInt(ch.TextChunkLimit) },
		set: func(ch *onboard.TelegramChannelConfig, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("invalid integer: %s", v)
			}
			ch.TextChunkLimit = n
			return nil
		},
	},
	"streaming": {
		get: func(ch *onboard.TelegramChannelConfig) string { return ch.Streaming },
		set: func(ch *onboard.TelegramChannelConfig, v string) error {
			if err := validateChoice(v, "off", "partial", "progress"); err != nil {
				return err
			}
			ch.Streaming = v
			return nil
		},
	},
	"allow_from": {
		get: func(ch *onboard.TelegramChannelConfig) string { return fmtInt64Slice(ch.AllowFrom) },
		set: func(ch *onboard.TelegramChannelConfig, v string) error {
			ids, err := parseCommaSepInt64(v)
			if err != nil {
				return err
			}
			ch.AllowFrom = ids
			return nil
		},
	},
	"group_policy": {
		get: func(ch *onboard.TelegramChannelConfig) string { return ch.GroupPolicy },
		set: func(ch *onboard.TelegramChannelConfig, v string) error {
			if err := validateChoice(v, "disabled", "allowlist", "open"); err != nil {
				return err
			}
			ch.GroupPolicy = v
			return nil
		},
	},
	"group_allow_from": {
		get: func(ch *onboard.TelegramChannelConfig) string { return fmtInt64Slice(ch.GroupAllowFrom) },
		set: func(ch *onboard.TelegramChannelConfig, v string) error {
			ids, err := parseCommaSepInt64(v)
			if err != nil {
				return err
			}
			ch.GroupAllowFrom = ids
			return nil
		},
	},
	"require_mention": {
		get: func(ch *onboard.TelegramChannelConfig) string { return fmtBoolPtr(ch.RequireMention) },
		set: func(ch *onboard.TelegramChannelConfig, v string) error {
			b, err := parseBool(v)
			if err != nil {
				return err
			}
			ch.RequireMention = &b
			return nil
		},
	},
}

// wecomFieldGetter returns the value of a wecom channel field.
type wecomFieldGetter func(*onboard.WeComChannelConfig) string

// wecomFieldSetter sets the value of a wecom channel field.
type wecomFieldSetter func(*onboard.WeComChannelConfig, string) error

// wecomFields defines field accessors for WeCom channels.
var wecomFields = map[string]struct {
	get    wecomFieldGetter
	set    wecomFieldSetter
	secret bool
}{
	"type": {
		get: func(ch *onboard.WeComChannelConfig) string { return ch.Type },
		set: func(ch *onboard.WeComChannelConfig, v string) error {
			if err := validateChoice(v, "wecom"); err != nil {
				return err
			}
			ch.Type = v
			return nil
		},
	},
	"enabled": {
		get: func(ch *onboard.WeComChannelConfig) string { return fmtBoolPtr(ch.Enabled) },
		set: func(ch *onboard.WeComChannelConfig, v string) error {
			b, err := parseBool(v)
			if err != nil {
				return err
			}
			ch.Enabled = &b
			return nil
		},
	},
	"token": {
		get:    func(ch *onboard.WeComChannelConfig) string { return ch.Token },
		set:    func(ch *onboard.WeComChannelConfig, v string) error { ch.Token = v; return nil },
		secret: true,
	},
	"encoding_aes_key": {
		get:    func(ch *onboard.WeComChannelConfig) string { return ch.EncodingAESKey },
		set:    func(ch *onboard.WeComChannelConfig, v string) error { ch.EncodingAESKey = v; return nil },
		secret: true,
	},
	"webhook_path": {
		get: func(ch *onboard.WeComChannelConfig) string { return ch.WebhookPath },
		set: func(ch *onboard.WeComChannelConfig, v string) error { ch.WebhookPath = v; return nil },
	},
	"dm_policy": {
		get: func(ch *onboard.WeComChannelConfig) string { return ch.DMPolicy },
		set: func(ch *onboard.WeComChannelConfig, v string) error {
			if err := validateChoice(v, "open", "pairing", "allowlist"); err != nil {
				return err
			}
			ch.DMPolicy = v
			return nil
		},
	},
	"allow_from": {
		get: func(ch *onboard.WeComChannelConfig) string { return fmtStringSlice(ch.AllowFrom) },
		set: func(ch *onboard.WeComChannelConfig, v string) error {
			ch.AllowFrom = parseCommaSepStrings(v)
			return nil
		},
	},
	"group_policy": {
		get: func(ch *onboard.WeComChannelConfig) string { return ch.GroupPolicy },
		set: func(ch *onboard.WeComChannelConfig, v string) error {
			if err := validateChoice(v, "open", "allowlist", "disabled"); err != nil {
				return err
			}
			ch.GroupPolicy = v
			return nil
		},
	},
	"group_allow_from": {
		get: func(ch *onboard.WeComChannelConfig) string { return fmtStringSlice(ch.GroupAllowFrom) },
		set: func(ch *onboard.WeComChannelConfig, v string) error {
			ch.GroupAllowFrom = parseCommaSepStrings(v)
			return nil
		},
	},
	"groups": {
		get: func(ch *onboard.WeComChannelConfig) string { return fmtJSON(ch.Groups) },
		set: func(ch *onboard.WeComChannelConfig, v string) error {
			var groups map[string]onboard.WeComGroupRuleFile
			if err := json.Unmarshal([]byte(v), &groups); err != nil {
				return fmt.Errorf("invalid JSON: %w", err)
			}
			ch.Groups = groups
			return nil
		},
	},
	"connection_mode": {
		get: func(ch *onboard.WeComChannelConfig) string { return ch.ConnectionMode },
		set: func(ch *onboard.WeComChannelConfig, v string) error {
			if err := validateChoice(v, "webhook", "websocket"); err != nil {
				return err
			}
			ch.ConnectionMode = v
			return nil
		},
	},
	"botid": {
		get: func(ch *onboard.WeComChannelConfig) string { return ch.BotID },
		set: func(ch *onboard.WeComChannelConfig, v string) error { ch.BotID = v; return nil },
	},
	"secret": {
		get:    func(ch *onboard.WeComChannelConfig) string { return ch.Secret },
		set:    func(ch *onboard.WeComChannelConfig, v string) error { ch.Secret = v; return nil },
		secret: true,
	},
	"text_chunk_limit": {
		get: func(ch *onboard.WeComChannelConfig) string { return fmtInt(ch.TextChunkLimit) },
		set: func(ch *onboard.WeComChannelConfig, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("invalid integer: %s", v)
			}
			ch.TextChunkLimit = n
			return nil
		},
	},
}

// parseChannelKey parses a key like "channels.telegram.bot_token" or
// "channels.my-wecom.token" into (channelName, fieldName, ok).
// channelName may be "*" for wildcard operations.
func parseChannelKey(key string) (channelName, fieldName string, ok bool) {
	rest, found := strings.CutPrefix(key, "channels.")
	if !found || rest == "" {
		return "", "", false
	}

	dot := strings.IndexByte(rest, '.')
	if dot < 0 {
		return "", "", false
	}

	channelName = rest[:dot]
	fieldName = rest[dot+1:]
	if channelName == "" || fieldName == "" {
		return "", "", false
	}

	return channelName, fieldName, true
}

// ConfigSet sets a config key to the given value and saves.
func ConfigSet(key, value string) error {
	// Try static keys first.
	if entry, ok := configKeys[key]; ok {
		cfg, err := onboard.LoadFileConfig()
		if err != nil {
			return err
		}
		if err := entry.set(cfg, value); err != nil {
			return fmt.Errorf("invalid value for %s: %w", key, err)
		}
		if err := onboard.SaveFileConfig(cfg); err != nil {
			return err
		}
		fmt.Printf("%s = %s\n", key, value)
		return nil
	}

	// Try channel keys.
	if strings.HasPrefix(key, "channels.") {
		return configSetChannel(key, value)
	}

	return fmt.Errorf("unknown config key: %s\n\nRun 'clawdex config list' to see all keys", key)
}

func configSetChannel(key, value string) error {
	cfg, err := onboard.LoadFileConfig()
	if err != nil {
		return err
	}

	channelName, fieldName, ok := parseChannelKey(key)
	if !ok {
		return fmt.Errorf("invalid channel key format: %s", key)
	}

	// Handle wildcard: set field for all channels.
	if channelName == "*" {
		return configSetChannelWildcard(cfg, fieldName, value)
	}

	// Ensure channels map exists.
	if cfg.Channels == nil {
		cfg.Channels = make(map[string]json.RawMessage)
	}

	raw, exists := cfg.Channels[channelName]
	chType, _ := onboard.ChannelType(raw)

	// Determine channel type from existing config or field name.
	if chType == "" {
		// Infer type from field name.
		if _, isTelegram := telegramFields[fieldName]; isTelegram {
			if _, isWecom := wecomFields[fieldName]; isWecom {
				// Field exists in both - this shouldn't happen with our design.
				return fmt.Errorf("ambiguous field %q exists in multiple channel types", fieldName)
			}
			chType = "telegram"
		} else if _, isWecom := wecomFields[fieldName]; isWecom {
			chType = "wecom"
		} else {
			return fmt.Errorf("unknown channel field: %s", fieldName)
		}
	}

	switch chType {
	case "telegram":
		var ch onboard.TelegramChannelConfig
		if exists && len(raw) > 0 {
			if err := json.Unmarshal(raw, &ch); err != nil {
				return fmt.Errorf("parse telegram config: %w", err)
			}
		} else {
			ch.Type = "telegram"
		}
		entry, ok := telegramFields[fieldName]
		if !ok {
			return fmt.Errorf("unknown telegram field: %s", fieldName)
		}
		if err := entry.set(&ch, value); err != nil {
			return fmt.Errorf("invalid value for %s: %w", key, err)
		}
		data, err := json.Marshal(ch)
		if err != nil {
			return fmt.Errorf("marshal telegram config: %w", err)
		}
		cfg.Channels[channelName] = data

	case "wecom":
		var ch onboard.WeComChannelConfig
		if exists && len(raw) > 0 {
			if err := json.Unmarshal(raw, &ch); err != nil {
				return fmt.Errorf("parse wecom config: %w", err)
			}
		} else {
			ch.Type = "wecom"
		}
		entry, ok := wecomFields[fieldName]
		if !ok {
			return fmt.Errorf("unknown wecom field: %s", fieldName)
		}
		if err := entry.set(&ch, value); err != nil {
			return fmt.Errorf("invalid value for %s: %w", key, err)
		}
		data, err := json.Marshal(ch)
		if err != nil {
			return fmt.Errorf("marshal wecom config: %w", err)
		}
		cfg.Channels[channelName] = data

	default:
		return fmt.Errorf("unknown channel type: %s", chType)
	}

	if err := onboard.SaveFileConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("%s = %s\n", key, value)
	return nil
}

// configSetChannelWildcard sets a field for all existing channels.
// It only sets the field on channels where the field is valid and the value is compatible.
// Channels with incompatible values are skipped silently.
func configSetChannelWildcard(cfg *onboard.FileConfig, fieldName, value string) error {
	if len(cfg.Channels) == 0 {
		return fmt.Errorf("no channels configured")
	}

	telegramEntry, isTelegram := telegramFields[fieldName]
	wecomEntry, isWecom := wecomFields[fieldName]

	if !isTelegram && !isWecom {
		return fmt.Errorf("unknown channel field: %s", fieldName)
	}

	var updated []string
	var skipped []string

	for name, raw := range cfg.Channels {
		chType, _ := onboard.ChannelType(raw)

		switch chType {
		case "telegram":
			if !isTelegram {
				continue
			}
			var ch onboard.TelegramChannelConfig
			if err := json.Unmarshal(raw, &ch); err != nil {
				continue
			}
			if err := telegramEntry.set(&ch, value); err != nil {
				// Skip channels with incompatible values (e.g., string for int64 field).
				skipped = append(skipped, name)
				continue
			}
			data, err := json.Marshal(ch)
			if err != nil {
				return fmt.Errorf("marshal telegram config: %w", err)
			}
			cfg.Channels[name] = data
			updated = append(updated, name)

		case "wecom":
			if !isWecom {
				continue
			}
			var ch onboard.WeComChannelConfig
			if err := json.Unmarshal(raw, &ch); err != nil {
				continue
			}
			if err := wecomEntry.set(&ch, value); err != nil {
				skipped = append(skipped, name)
				continue
			}
			data, err := json.Marshal(ch)
			if err != nil {
				return fmt.Errorf("marshal wecom config: %w", err)
			}
			cfg.Channels[name] = data
			updated = append(updated, name)
		}
	}

	if len(updated) == 0 {
		if len(skipped) > 0 {
			return fmt.Errorf("value %q is incompatible with field %q for all channels", value, fieldName)
		}
		return fmt.Errorf("no channels have field %q", fieldName)
	}

	if err := onboard.SaveFileConfig(cfg); err != nil {
		return err
	}

	sort.Strings(updated)
	msg := fmt.Sprintf("channels.*.%s = %s (updated: %s)", fieldName, value, strings.Join(updated, ", "))
	if len(skipped) > 0 {
		sort.Strings(skipped)
		msg += fmt.Sprintf(" (skipped: %s)", strings.Join(skipped, ", "))
	}
	fmt.Println(msg)
	return nil
}

// ConfigGet prints the current value of a config key.
func ConfigGet(key string) error {
	// Try static keys first.
	if entry, ok := configKeys[key]; ok {
		cfg, err := onboard.LoadFileConfig()
		if err != nil {
			return err
		}
		fmt.Println(entry.get(cfg))
		return nil
	}

	// Try channel keys.
	if strings.HasPrefix(key, "channels.") {
		return configGetChannel(key)
	}

	return fmt.Errorf("unknown config key: %s\n\nRun 'clawdex config list' to see all keys", key)
}

func configGetChannel(key string) error {
	cfg, err := onboard.LoadFileConfig()
	if err != nil {
		return err
	}

	channelName, fieldName, ok := parseChannelKey(key)
	if !ok {
		return fmt.Errorf("invalid channel key format: %s", key)
	}

	// Handle wildcard: show field for all channels.
	if channelName == "*" {
		return configGetChannelWildcard(cfg, fieldName)
	}

	raw, exists := cfg.Channels[channelName]
	if !exists || len(raw) == 0 {
		fmt.Println("(not set)")
		return nil
	}

	chType, err := onboard.ChannelType(raw)
	if err != nil {
		return err
	}

	switch chType {
	case "telegram":
		var ch onboard.TelegramChannelConfig
		if err := json.Unmarshal(raw, &ch); err != nil {
			return fmt.Errorf("parse telegram config: %w", err)
		}
		entry, ok := telegramFields[fieldName]
		if !ok {
			return fmt.Errorf("unknown telegram field: %s", fieldName)
		}
		fmt.Println(entry.get(&ch))

	case "wecom":
		var ch onboard.WeComChannelConfig
		if err := json.Unmarshal(raw, &ch); err != nil {
			return fmt.Errorf("parse wecom config: %w", err)
		}
		entry, ok := wecomFields[fieldName]
		if !ok {
			return fmt.Errorf("unknown wecom field: %s", fieldName)
		}
		fmt.Println(entry.get(&ch))

	default:
		return fmt.Errorf("unknown channel type: %s", chType)
	}

	return nil
}

// configGetChannelWildcard shows a field value for all channels.
func configGetChannelWildcard(cfg *onboard.FileConfig, fieldName string) error {
	if len(cfg.Channels) == 0 {
		fmt.Println("(no channels configured)")
		return nil
	}

	telegramEntry, isTelegram := telegramFields[fieldName]
	wecomEntry, isWecom := wecomFields[fieldName]

	if !isTelegram && !isWecom {
		return fmt.Errorf("unknown channel field: %s", fieldName)
	}

	// Collect results for sorted output.
	type result struct {
		name  string
		value string
	}
	var results []result

	for name, raw := range cfg.Channels {
		chType, _ := onboard.ChannelType(raw)

		switch chType {
		case "telegram":
			if !isTelegram {
				continue
			}
			var ch onboard.TelegramChannelConfig
			if err := json.Unmarshal(raw, &ch); err != nil {
				continue
			}
			results = append(results, result{name: name, value: telegramEntry.get(&ch)})

		case "wecom":
			if !isWecom {
				continue
			}
			var ch onboard.WeComChannelConfig
			if err := json.Unmarshal(raw, &ch); err != nil {
				continue
			}
			results = append(results, result{name: name, value: wecomEntry.get(&ch)})
		}
	}

	if len(results) == 0 {
		fmt.Printf("(no channels have field %q)\n", fieldName)
		return nil
	}

	sort.Slice(results, func(i, j int) bool { return results[i].name < results[j].name })
	for _, r := range results {
		if r.value == "" {
			r.value = "(not set)"
		}
		fmt.Printf("channels.%s.%s = %s\n", r.name, fieldName, r.value)
	}

	return nil
}

// ConfigList prints all config keys and their current values.
func ConfigList() error {
	cfg, err := onboard.LoadFileConfig()
	if err != nil {
		return err
	}

	// Static keys.
	keys := make([]string, 0, len(configKeys))
	for k := range configKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		entry := configKeys[k]
		val := entry.get(cfg)
		if entry.secret && val != "" {
			val = secret.Describe(val)
		}
		if val == "" {
			val = "(not set)"
		}
		fmt.Printf("%-40s %s\n", k, val)
	}

	// Channels.
	if len(cfg.Channels) > 0 {
		channelNames := make([]string, 0, len(cfg.Channels))
		for name := range cfg.Channels {
			channelNames = append(channelNames, name)
		}
		sort.Strings(channelNames)

		for _, name := range channelNames {
			raw := cfg.Channels[name]
			chType, _ := onboard.ChannelType(raw)

			prefix := fmt.Sprintf("channels.%s", name)

			switch chType {
			case "telegram":
				var ch onboard.TelegramChannelConfig
				if err := json.Unmarshal(raw, &ch); err != nil {
					continue
				}
				fieldNames := make([]string, 0, len(telegramFields))
				for f := range telegramFields {
					fieldNames = append(fieldNames, f)
				}
				sort.Strings(fieldNames)
				for _, f := range fieldNames {
					entry := telegramFields[f]
					val := entry.get(&ch)
					if entry.secret && val != "" {
						val = secret.Describe(val)
					}
					if val == "" {
						val = "(not set)"
					}
					fmt.Printf("%-40s %s\n", prefix+"."+f, val)
				}

			case "wecom":
				var ch onboard.WeComChannelConfig
				if err := json.Unmarshal(raw, &ch); err != nil {
					continue
				}
				fieldNames := make([]string, 0, len(wecomFields))
				for f := range wecomFields {
					fieldNames = append(fieldNames, f)
				}
				sort.Strings(fieldNames)
				for _, f := range fieldNames {
					entry := wecomFields[f]
					val := entry.get(&ch)
					if entry.secret && val != "" {
						val = secret.Describe(val)
					}
					if val == "" {
						val = "(not set)"
					}
					fmt.Printf("%-40s %s\n", prefix+"."+f, val)
				}
			}
		}
	}

	return nil
}

// ConfigFile prints the path to the config file.
func ConfigFile() error {
	path, err := onboard.ConfigPath()
	if err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

// ── Helpers ──

func parseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "true", "1":
		return true, nil
	case "false", "0":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean: %s (must be true or false)", s)
	}
}

func validateChoice(v string, choices ...string) error {
	for _, c := range choices {
		if v == c {
			return nil
		}
	}
	return fmt.Errorf("invalid value %q: must be one of %s", v, strings.Join(choices, ", "))
}

func fmtBoolPtr(b *bool) string {
	if b == nil {
		return ""
	}
	if *b {
		return "true"
	}
	return "false"
}

func fmtInt(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func fmtInt64Slice(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

func fmtStringSlice(ss []string) string {
	return strings.Join(ss, ",")
}

func parseCommaSepInt64(s string) ([]int64, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	var ids []int64
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid int64 %q: %w", p, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func parseCommaSepStrings(s string) []string {
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// fmtJSON marshals v to compact JSON. Returns "" if v is nil or empty.
func fmtJSON(v any) string {
	if v == nil {
		return ""
	}
	data, err := json.Marshal(v)
	if err != nil || string(data) == "null" || string(data) == "{}" {
		return ""
	}
	return string(data)
}
