package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"bilibililivetools/gover/backend/config"
	"bilibililivetools/gover/backend/httpapi"
	"bilibililivetools/gover/backend/router"
)

type runtimeConfigModule struct {
	deps *router.Dependencies
}

func init() {
	router.Register(func(deps *router.Dependencies) router.Module {
		return &runtimeConfigModule{deps: deps}
	})
}

func (m *runtimeConfigModule) Prefix() string {
	return m.deps.Config.APIBase
}

func (m *runtimeConfigModule) Routes() []router.Route {
	return []router.Route{
		{Method: http.MethodGet, Pattern: "/config", Summary: "Get runtime config", Handler: m.getConfig},
		{Method: http.MethodPost, Pattern: "/config", Summary: "Save runtime config and hot reload", Handler: m.saveConfig},
		{Method: http.MethodPost, Pattern: "/config/reload", Summary: "Reload config from file", Handler: m.reloadConfig},
	}
}

func (m *runtimeConfigModule) getConfig(w http.ResponseWriter, r *http.Request) {
	if m.deps.ConfigMgr == nil {
		httpapi.Error(w, -1, "config manager not available", http.StatusOK)
		return
	}
	cfg := m.deps.ConfigMgr.Current()
	httpapi.OK(w, map[string]any{
		"config":         cfg,
		"configFile":     cfg.ConfigFile,
		"hotReloadNotes": runtimeHotReloadNotes(),
	})
}

func (m *runtimeConfigModule) saveConfig(w http.ResponseWriter, r *http.Request) {
	if m.deps.ConfigMgr == nil {
		httpapi.Error(w, -1, "config manager not available", http.StatusOK)
		return
	}
	oldCfg := m.deps.ConfigMgr.Current()
	nextCfg, err := decodeConfigPatch(r, oldCfg)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	saved, err := m.deps.ConfigMgr.Save(nextCfg)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	restartFields := restartRequiredChangedFields(oldCfg, saved)
	httpapi.OK(w, map[string]any{
		"config":          saved,
		"configFile":      saved.ConfigFile,
		"requiresRestart": len(restartFields) > 0,
		"restartFields":   restartFields,
		"hotReloadNotes":  runtimeHotReloadNotes(),
	})
}

func (m *runtimeConfigModule) reloadConfig(w http.ResponseWriter, r *http.Request) {
	if m.deps.ConfigMgr == nil {
		httpapi.Error(w, -1, "config manager not available", http.StatusOK)
		return
	}
	oldCfg := m.deps.ConfigMgr.Current()
	cfg, err := m.deps.ConfigMgr.ReloadFromDisk()
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	restartFields := restartRequiredChangedFields(oldCfg, cfg)
	httpapi.OK(w, map[string]any{
		"config":          cfg,
		"configFile":      cfg.ConfigFile,
		"requiresRestart": len(restartFields) > 0,
		"restartFields":   restartFields,
		"hotReloadNotes":  runtimeHotReloadNotes(),
	})
}

func decodeConfigPatch(r *http.Request, base config.Config) (config.Config, error) {
	var req map[string]any
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		return base, err
	}

	if value, ok := getString(req, "listenAddr"); ok {
		base.ListenAddr = value
	}
	if value, ok := getString(req, "dataDir"); ok {
		base.DataDir = value
	}
	if value, ok := getString(req, "dbPath"); ok {
		base.DBPath = value
	}
	if value, ok := getString(req, "mediaDir"); ok {
		base.MediaDir = value
	}
	if value, ok := getString(req, "ffmpegPath"); ok {
		base.FFmpegPath = value
	}
	if value, ok := getString(req, "ffprobePath"); ok {
		base.FFprobePath = value
	}
	if value, ok := getInt(req, "logBufferSize"); ok {
		base.LogBufferSize = value
	}
	if value, ok := getString(req, "apiBase"); ok {
		base.APIBase = value
	}
	if value, ok := getString(req, "allowOrigin"); ok {
		base.AllowOrigin = value
	}
	if value, ok := getBool(req, "enableDebugLogs"); ok {
		base.EnableDebugLogs = value
	}
	if value, ok := getBool(req, "autoStartPush"); ok {
		base.AutoStartPush = value
	}
	if value, ok := getBool(req, "debugMode"); ok {
		base.DebugMode = value
		base.EnableDebugLogs = value
	}
	if value, ok := getString(req, "biliAppKey"); ok {
		base.BiliAppKey = value
	}
	if value, ok := getString(req, "biliAppSecret"); ok {
		base.BiliAppSecret = value
	}
	if value, ok := getString(req, "biliPlatform"); ok {
		base.BiliPlatform = value
	}
	if value, ok := getString(req, "biliVersion"); ok {
		base.BiliVersion = value
	}
	if value, ok := getString(req, "biliBuild"); ok {
		base.BiliBuild = value
	}
	if value, ok := getBool(req, "gb28181Enabled"); ok {
		base.GB28181Enabled = value
	}
	if value, ok := getString(req, "gb28181ListenIp"); ok {
		base.GB28181ListenIP = value
	}
	if value, ok := getInt(req, "gb28181ListenPort"); ok {
		base.GB28181ListenPort = value
	}
	if value, ok := getString(req, "gb28181Transport"); ok {
		base.GB28181Transport = value
	}
	if value, ok := getString(req, "gb28181ServerId"); ok {
		base.GB28181ServerID = value
	}
	if value, ok := getString(req, "gb28181Realm"); ok {
		base.GB28181Realm = value
	}
	if value, ok := getString(req, "gb28181Password"); ok {
		base.GB28181Password = value
	}
	if value, ok := getInt(req, "gb28181RegisterExpires"); ok {
		base.GB28181RegisterExpires = value
	}
	if value, ok := getInt(req, "gb28181HeartbeatInterval"); ok {
		base.GB28181HeartbeatInterval = value
	}
	if value, ok := getString(req, "gb28181MediaIp"); ok {
		base.GB28181MediaIP = value
	}
	if value, ok := getInt(req, "gb28181MediaPort"); ok {
		base.GB28181MediaPort = value
	}
	if value, ok := getInt(req, "gb28181MediaPortStart"); ok {
		base.GB28181MediaPortStart = value
	}
	if value, ok := getInt(req, "gb28181MediaPortEnd"); ok {
		base.GB28181MediaPortEnd = value
	}
	if value, ok := getInt(req, "gb28181AckTimeoutSec"); ok {
		base.GB28181AckTimeoutSec = value
	}
	return base, nil
}

func restartRequiredChangedFields(oldCfg config.Config, newCfg config.Config) []string {
	result := make([]string, 0, 10)
	appendIfChanged := func(name string, oldValue string, newValue string) {
		if strings.TrimSpace(oldValue) != strings.TrimSpace(newValue) {
			result = append(result, name)
		}
	}
	appendIfChanged("listenAddr", oldCfg.ListenAddr, newCfg.ListenAddr)
	appendIfChanged("apiBase", oldCfg.APIBase, newCfg.APIBase)
	appendIfChanged("dataDir", oldCfg.DataDir, newCfg.DataDir)
	appendIfChanged("dbPath", oldCfg.DBPath, newCfg.DBPath)
	appendIfChanged("mediaDir", oldCfg.MediaDir, newCfg.MediaDir)
	appendIfChanged("allowOrigin", oldCfg.AllowOrigin, newCfg.AllowOrigin)
	appendIfChanged("gb28181ListenIp", oldCfg.GB28181ListenIP, newCfg.GB28181ListenIP)
	appendIfChanged("gb28181Transport", oldCfg.GB28181Transport, newCfg.GB28181Transport)
	appendIfChanged("gb28181ServerId", oldCfg.GB28181ServerID, newCfg.GB28181ServerID)
	appendIfChanged("gb28181Realm", oldCfg.GB28181Realm, newCfg.GB28181Realm)
	appendIfChanged("gb28181Password", oldCfg.GB28181Password, newCfg.GB28181Password)
	appendIfChanged("gb28181MediaIp", oldCfg.GB28181MediaIP, newCfg.GB28181MediaIP)
	if oldCfg.LogBufferSize != newCfg.LogBufferSize {
		result = append(result, "logBufferSize")
	}
	if oldCfg.AutoStartPush != newCfg.AutoStartPush {
		result = append(result, "autoStartPush")
	}
	if oldCfg.GB28181Enabled != newCfg.GB28181Enabled {
		result = append(result, "gb28181Enabled")
	}
	if oldCfg.GB28181ListenPort != newCfg.GB28181ListenPort {
		result = append(result, "gb28181ListenPort")
	}
	if oldCfg.GB28181RegisterExpires != newCfg.GB28181RegisterExpires {
		result = append(result, "gb28181RegisterExpires")
	}
	if oldCfg.GB28181HeartbeatInterval != newCfg.GB28181HeartbeatInterval {
		result = append(result, "gb28181HeartbeatInterval")
	}
	if oldCfg.GB28181MediaPort != newCfg.GB28181MediaPort {
		result = append(result, "gb28181MediaPort")
	}
	if oldCfg.GB28181MediaPortStart != newCfg.GB28181MediaPortStart {
		result = append(result, "gb28181MediaPortStart")
	}
	if oldCfg.GB28181MediaPortEnd != newCfg.GB28181MediaPortEnd {
		result = append(result, "gb28181MediaPortEnd")
	}
	if oldCfg.GB28181AckTimeoutSec != newCfg.GB28181AckTimeoutSec {
		result = append(result, "gb28181AckTimeoutSec")
	}
	return result
}

func runtimeHotReloadNotes() []string {
	return []string{
		"Dynamic hot reload now applies to ffmpegPath/ffprobePath and Bilibili API credentials/metadata.",
		"debugMode/enableDebugLogs now hot apply to data/log file logging and ffmpeg verbose mirror.",
		"autoStartPush controls whether startup will auto-call push start; change takes effect on next process start.",
		"GB28181 signaling and media port pool settings can be edited here; runtime service will apply on next start/restart.",
		"All other fields are persisted, but restart is required before they fully apply.",
	}
}

func getString(payload map[string]any, key string) (string, bool) {
	value, ok := payload[key]
	if !ok {
		return "", false
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v), true
	default:
		return fmt.Sprintf("%v", v), true
	}
}

func getInt(payload map[string]any, key string) (int, bool) {
	value, ok := payload[key]
	if !ok {
		return 0, false
	}
	switch v := value.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func getBool(payload map[string]any, key string) (bool, bool) {
	value, ok := payload[key]
	if !ok {
		return false, false
	}
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		lower := strings.ToLower(strings.TrimSpace(v))
		if lower == "true" || lower == "1" || lower == "yes" {
			return true, true
		}
		if lower == "false" || lower == "0" || lower == "no" {
			return false, true
		}
	}
	return false, false
}
