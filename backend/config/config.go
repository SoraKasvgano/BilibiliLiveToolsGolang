package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Config holds runtime options for the Go rewrite service.
type Config struct {
	ListenAddr               string `json:"listenAddr"`
	DataDir                  string `json:"dataDir"`
	DBPath                   string `json:"dbPath"`
	MediaDir                 string `json:"mediaDir"`
	FFmpegPath               string `json:"ffmpegPath"`
	FFprobePath              string `json:"ffprobePath"`
	LogBufferSize            int    `json:"logBufferSize"`
	APIBase                  string `json:"apiBase"`
	AllowOrigin              string `json:"allowOrigin"`
	DebugMode                bool   `json:"debugMode"`
	EnableDebugLogs          bool   `json:"enableDebugLogs"`
	AutoStartPush            bool   `json:"autoStartPush"`
	BiliAppKey               string `json:"biliAppKey"`
	BiliAppSecret            string `json:"biliAppSecret"`
	BiliPlatform             string `json:"biliPlatform"`
	BiliVersion              string `json:"biliVersion"`
	BiliBuild                string `json:"biliBuild"`
	GB28181Enabled           bool   `json:"gb28181Enabled"`
	GB28181ListenIP          string `json:"gb28181ListenIp"`
	GB28181ListenPort        int    `json:"gb28181ListenPort"`
	GB28181Transport         string `json:"gb28181Transport"`
	GB28181ServerID          string `json:"gb28181ServerId"`
	GB28181Realm             string `json:"gb28181Realm"`
	GB28181Password          string `json:"gb28181Password"`
	GB28181RegisterExpires   int    `json:"gb28181RegisterExpires"`
	GB28181HeartbeatInterval int    `json:"gb28181HeartbeatInterval"`
	GB28181MediaIP           string `json:"gb28181MediaIp"`
	GB28181MediaPort         int    `json:"gb28181MediaPort"`
	GB28181MediaPortStart    int    `json:"gb28181MediaPortStart"`
	GB28181MediaPortEnd      int    `json:"gb28181MediaPortEnd"`
	GB28181AckTimeoutSec     int    `json:"gb28181AckTimeoutSec"`
	ConfigFile               string `json:"configFile"`
}

func resolveConfigFilePath() (string, error) {
	path := strings.TrimSpace(os.Getenv("GOVER_CONFIG_FILE"))
	if path == "" {
		path = defaultConfigFilePath()
	}
	return filepath.Abs(path)
}

func defaultConfigFilePath() string {
	defaultPath := filepath.FromSlash("./data/config.json")
	repoPath := filepath.FromSlash("./gover/data/config.json")
	for _, candidate := range []string{defaultPath, repoPath} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	if info, err := os.Stat(filepath.FromSlash("./gover")); err == nil && info.IsDir() {
		return repoPath
	}
	return defaultPath
}

func defaultFFmpegPathByOS() string {
	if found := firstExistingBinary(ffmpegCandidatesByOS()); found != "" {
		return found
	}
	switch runtime.GOOS {
	case "windows":
		return "ffmpeg.exe"
	default:
		return "ffmpeg"
	}
}

func defaultFFprobePathByOS() string {
	if found := firstExistingBinary(ffprobeCandidatesByOS()); found != "" {
		return found
	}
	switch runtime.GOOS {
	case "windows":
		return "ffprobe.exe"
	default:
		return "ffprobe"
	}
}

func ffmpegCandidatesByOS() []string {
	switch runtime.GOOS {
	case "windows":
		return expandBinaryCandidates(
			"./ffmpeg/win-x64/ffmpeg.exe",
			"./gover/ffmpeg/win-x64/ffmpeg.exe",
			"./ffmpeg/ffmpeg.exe",
		)
	case "linux":
		archPath := "./ffmpeg/linux-x64/ffmpeg"
		repoArchPath := "./gover/ffmpeg/linux-x64/ffmpeg"
		switch runtime.GOARCH {
		case "arm64":
			archPath = "./ffmpeg/linux-arm64/ffmpeg"
			repoArchPath = "./gover/ffmpeg/linux-arm64/ffmpeg"
		case "arm":
			archPath = "./ffmpeg/linux-arm/ffmpeg"
			repoArchPath = "./gover/ffmpeg/linux-arm/ffmpeg"
		}
		return expandBinaryCandidates(
			archPath,
			repoArchPath,
			"./ffmpeg/linux-x64/ffmpeg",
			"./gover/ffmpeg/linux-x64/ffmpeg",
			"./ffmpeg/ffmpeg",
		)
	default:
		return nil
	}
}

func ffprobeCandidatesByOS() []string {
	switch runtime.GOOS {
	case "windows":
		return expandBinaryCandidates(
			"./ffmpeg/win-x64/ffprobe.exe",
			"./gover/ffmpeg/win-x64/ffprobe.exe",
			"./ffmpeg/ffprobe.exe",
		)
	case "linux":
		archPath := "./ffmpeg/linux-x64/ffprobe"
		repoArchPath := "./gover/ffmpeg/linux-x64/ffprobe"
		switch runtime.GOARCH {
		case "arm64":
			archPath = "./ffmpeg/linux-arm64/ffprobe"
			repoArchPath = "./gover/ffmpeg/linux-arm64/ffprobe"
		case "arm":
			archPath = "./ffmpeg/linux-arm/ffprobe"
			repoArchPath = "./gover/ffmpeg/linux-arm/ffprobe"
		}
		return expandBinaryCandidates(
			archPath,
			repoArchPath,
			"./ffmpeg/ffprobe",
		)
	default:
		return nil
	}
}

func expandBinaryCandidates(paths ...string) []string {
	exeDir := ""
	if exePath, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exePath)
	}
	candidates := make([]string, 0, len(paths)*2)
	for _, path := range paths {
		cleanPath := filepath.Clean(filepath.FromSlash(strings.TrimSpace(path)))
		if cleanPath == "." || cleanPath == "" {
			continue
		}
		candidates = append(candidates, cleanPath)
		if exeDir != "" && !filepath.IsAbs(cleanPath) {
			candidates = append(candidates, filepath.Join(exeDir, cleanPath))
		}
	}
	return candidates
}

func firstExistingBinary(candidates []string) string {
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		abs, err := filepath.Abs(candidate)
		if err != nil {
			return candidate
		}
		return abs
	}
	return ""
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envIntOrDefault(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	if parsed <= 0 {
		return fallback
	}
	return parsed
}

func defaultConfig(configFile string) Config {
	baseDir := filepath.Dir(configFile)
	cfg := Config{
		ListenAddr:               envOrDefault("GOVER_LISTEN", ":18686"),
		DataDir:                  envOrDefault("GOVER_DATA_DIR", baseDir),
		FFmpegPath:               envOrDefault("GOVER_FFMPEG_PATH", defaultFFmpegPathByOS()),
		FFprobePath:              envOrDefault("GOVER_FFPROBE_PATH", defaultFFprobePathByOS()),
		LogBufferSize:            envIntOrDefault("GOVER_LOG_BUFFER_SIZE", 300),
		APIBase:                  envOrDefault("GOVER_API_BASE", "/api/v1"),
		AllowOrigin:              envOrDefault("GOVER_ALLOW_ORIGIN", "*"),
		DebugMode:                strings.EqualFold(envOrDefault("GOVER_DEBUG", "false"), "true"),
		EnableDebugLogs:          strings.EqualFold(envOrDefault("GOVER_DEBUG", "false"), "true"),
		AutoStartPush:            strings.EqualFold(envOrDefault("GOVER_AUTO_START_PUSH", "false"), "true"),
		BiliAppKey:               envOrDefault("GOVER_BILI_APP_KEY", "aae92bc66f3edfab"),
		BiliAppSecret:            envOrDefault("GOVER_BILI_APP_SECRET", "af125a0d5279fd576c1b4418a3e8276d"),
		BiliPlatform:             envOrDefault("GOVER_BILI_PLATFORM", "pc_link"),
		BiliVersion:              envOrDefault("GOVER_BILI_VERSION", "7.20.0.9482"),
		BiliBuild:                envOrDefault("GOVER_BILI_BUILD", "9482"),
		GB28181Enabled:           strings.EqualFold(envOrDefault("GOVER_GB28181_ENABLED", "false"), "true"),
		GB28181ListenIP:          envOrDefault("GOVER_GB28181_LISTEN_IP", "0.0.0.0"),
		GB28181ListenPort:        envIntOrDefault("GOVER_GB28181_LISTEN_PORT", 5060),
		GB28181Transport:         envOrDefault("GOVER_GB28181_TRANSPORT", "udp"),
		GB28181ServerID:          envOrDefault("GOVER_GB28181_SERVER_ID", "34020000002000000001"),
		GB28181Realm:             envOrDefault("GOVER_GB28181_REALM", "3402000000"),
		GB28181Password:          envOrDefault("GOVER_GB28181_PASSWORD", "123456"),
		GB28181RegisterExpires:   envIntOrDefault("GOVER_GB28181_REGISTER_EXPIRES", 3600),
		GB28181HeartbeatInterval: envIntOrDefault("GOVER_GB28181_HEARTBEAT_INTERVAL", 60),
		GB28181MediaIP:           envOrDefault("GOVER_GB28181_MEDIA_IP", ""),
		GB28181MediaPort:         envIntOrDefault("GOVER_GB28181_MEDIA_PORT", 30000),
		GB28181MediaPortStart:    envIntOrDefault("GOVER_GB28181_MEDIA_PORT_START", 30000),
		GB28181MediaPortEnd:      envIntOrDefault("GOVER_GB28181_MEDIA_PORT_END", 30100),
		GB28181AckTimeoutSec:     envIntOrDefault("GOVER_GB28181_ACK_TIMEOUT_SEC", 10),
		ConfigFile:               configFile,
	}
	cfg = normalizeConfig(cfg, configFile)
	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(cfg.DataDir, "db", "app.db")
	}
	if cfg.MediaDir == "" {
		cfg.MediaDir = filepath.Join(cfg.DataDir, "media")
	}
	cfg.ConfigFile = configFile
	return cfg
}

func normalizeConfig(cfg Config, configFile string) Config {
	configDir := filepath.Dir(configFile)
	cfg.ConfigFile = configFile

	if strings.TrimSpace(cfg.ListenAddr) == "" {
		cfg.ListenAddr = ":18686"
	}
	if strings.TrimSpace(cfg.APIBase) == "" {
		cfg.APIBase = "/api/v1"
	}
	if !strings.HasPrefix(cfg.APIBase, "/") {
		cfg.APIBase = "/" + cfg.APIBase
	}
	cfg.APIBase = strings.TrimSuffix(cfg.APIBase, "/")
	if cfg.APIBase == "" {
		cfg.APIBase = "/api/v1"
	}
	if cfg.LogBufferSize <= 0 {
		cfg.LogBufferSize = 300
	}
	if strings.TrimSpace(cfg.AllowOrigin) == "" {
		cfg.AllowOrigin = "*"
	}
	if cfg.DebugMode {
		cfg.EnableDebugLogs = true
	}
	cfg.DebugMode = cfg.EnableDebugLogs
	if strings.TrimSpace(cfg.BiliPlatform) == "" {
		cfg.BiliPlatform = "pc_link"
	}
	if strings.TrimSpace(cfg.BiliVersion) == "" {
		cfg.BiliVersion = "7.20.0.9482"
	}
	if strings.TrimSpace(cfg.BiliBuild) == "" {
		cfg.BiliBuild = "9482"
	}
	if strings.TrimSpace(cfg.GB28181ListenIP) == "" {
		cfg.GB28181ListenIP = "0.0.0.0"
	}
	if cfg.GB28181ListenPort <= 0 {
		cfg.GB28181ListenPort = 5060
	}
	transport := strings.ToLower(strings.TrimSpace(cfg.GB28181Transport))
	switch transport {
	case "udp", "tcp", "both":
		cfg.GB28181Transport = transport
	default:
		cfg.GB28181Transport = "udp"
	}
	if strings.TrimSpace(cfg.GB28181ServerID) == "" {
		cfg.GB28181ServerID = "34020000002000000001"
	}
	if strings.TrimSpace(cfg.GB28181Realm) == "" {
		cfg.GB28181Realm = "3402000000"
	}
	if cfg.GB28181RegisterExpires <= 0 {
		cfg.GB28181RegisterExpires = 3600
	}
	if cfg.GB28181HeartbeatInterval <= 0 {
		cfg.GB28181HeartbeatInterval = 60
	}
	if cfg.GB28181MediaPort <= 0 {
		cfg.GB28181MediaPort = 30000
	}
	if cfg.GB28181MediaPortStart <= 0 {
		cfg.GB28181MediaPortStart = cfg.GB28181MediaPort
	}
	if cfg.GB28181MediaPortEnd <= 0 {
		cfg.GB28181MediaPortEnd = cfg.GB28181MediaPortStart + 100
	}
	if cfg.GB28181MediaPortEnd < cfg.GB28181MediaPortStart {
		cfg.GB28181MediaPortStart, cfg.GB28181MediaPortEnd = cfg.GB28181MediaPortEnd, cfg.GB28181MediaPortStart
	}
	if cfg.GB28181MediaPort < cfg.GB28181MediaPortStart || cfg.GB28181MediaPort > cfg.GB28181MediaPortEnd {
		cfg.GB28181MediaPort = cfg.GB28181MediaPortStart
	}
	if cfg.GB28181AckTimeoutSec <= 0 {
		cfg.GB28181AckTimeoutSec = 10
	}
	if strings.TrimSpace(cfg.FFmpegPath) == "" {
		cfg.FFmpegPath = defaultFFmpegPathByOS()
	}
	if strings.TrimSpace(cfg.FFprobePath) == "" {
		cfg.FFprobePath = defaultFFprobePathByOS()
	}

	cfg.DataDir = absPathWithBase(cfg.DataDir, configDir)
	if strings.TrimSpace(cfg.DataDir) == "" {
		cfg.DataDir = configDir
	}

	cfg.DBPath = absPathWithBase(cfg.DBPath, configDir)
	if strings.TrimSpace(cfg.DBPath) == "" {
		cfg.DBPath = filepath.Join(cfg.DataDir, "db", "app.db")
	}

	cfg.MediaDir = absPathWithBase(cfg.MediaDir, configDir)
	if strings.TrimSpace(cfg.MediaDir) == "" {
		cfg.MediaDir = filepath.Join(cfg.DataDir, "media")
	}

	cfg.FFmpegPath = absPathWithBase(cfg.FFmpegPath, configDir)
	cfg.FFprobePath = absPathWithBase(cfg.FFprobePath, configDir)
	return cfg
}

func absPathWithBase(target string, base string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	if filepath.IsAbs(target) {
		return target
	}
	if base == "" {
		if abs, err := filepath.Abs(target); err == nil {
			return abs
		}
		return target
	}
	if abs, err := filepath.Abs(filepath.Join(base, target)); err == nil {
		return abs
	}
	return filepath.Join(base, target)
}

// Load keeps backward compatibility by returning the current config snapshot.
func Load() (Config, error) {
	manager, err := NewManager()
	if err != nil {
		return Config{}, err
	}
	cfg := manager.Current()
	manager.StopWatching()
	return cfg, nil
}
