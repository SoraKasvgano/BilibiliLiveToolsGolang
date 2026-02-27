package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"bilibililivetools/gover/backend/store"
)

type Service struct {
	ffmpegPath  string
	ffprobePath string
	mu          sync.RWMutex
}

func New(ffmpegPath string, ffprobePath string) *Service {
	return &Service{ffmpegPath: ffmpegPath, ffprobePath: ffprobePath}
}

func (s *Service) BinaryPath() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return normalizeBinaryPath(s.ffmpegPath, "ffmpeg")
}

func (s *Service) FFprobePath() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return normalizeBinaryPath(s.ffprobePath, "ffprobe")
}

func (s *Service) UpdatePaths(ffmpegPath string, ffprobePath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(ffmpegPath) != "" {
		s.ffmpegPath = strings.TrimSpace(ffmpegPath)
	}
	if strings.TrimSpace(ffprobePath) != "" {
		s.ffprobePath = strings.TrimSpace(ffprobePath)
	}
}

func (s *Service) Version(ctx context.Context) (string, error) {
	output, err := runCombined(ctx, s.BinaryPath(), "-version")
	if err != nil {
		return "", err
	}
	line := strings.Split(strings.TrimSpace(output), "\n")
	if len(line) == 0 {
		return "", errors.New("empty ffmpeg version output")
	}
	return strings.TrimSpace(line[0]), nil
}

func (s *Service) ListVideoCodecs(ctx context.Context) ([]string, error) {
	output, err := runCombined(ctx, s.BinaryPath(), "-hide_banner", "-codecs")
	if err != nil {
		return nil, err
	}
	codecs := make([]string, 0)
	seen := map[string]struct{}{}
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if len(line) < 8 {
			continue
		}
		if strings.HasPrefix(line, "D") || strings.HasPrefix(line, ".") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			name := fields[1]
			if strings.Contains(strings.ToLower(name), "264") || strings.Contains(strings.ToLower(name), "265") || strings.Contains(strings.ToLower(name), "hevc") {
				if _, ok := seen[name]; !ok {
					seen[name] = struct{}{}
					codecs = append(codecs, name)
				}
			}
		}
	}
	sort.Strings(codecs)
	return codecs, nil
}

func (s *Service) ListDevices(ctx context.Context) ([]store.DeviceInfo, []store.DeviceInfo, error) {
	switch runtime.GOOS {
	case "windows":
		return s.listWindowsDevices(ctx)
	default:
		return s.listUnixDevices(ctx)
	}
}

func (s *Service) listWindowsDevices(ctx context.Context) ([]store.DeviceInfo, []store.DeviceInfo, error) {
	output, err := runCombined(ctx, s.BinaryPath(), "-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy")
	if err != nil {
		// ffmpeg returns non-zero for this command on many machines, so only fail on empty output.
		if strings.TrimSpace(output) == "" {
			return nil, nil, err
		}
	}
	videos := make([]store.DeviceInfo, 0)
	audios := make([]store.DeviceInfo, 0)
	mode := ""
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.Contains(line, "DirectShow video devices") {
			mode = "video"
			continue
		}
		if strings.Contains(line, "DirectShow audio devices") {
			mode = "audio"
			continue
		}
		if !strings.Contains(line, "\"") {
			continue
		}
		start := strings.Index(line, "\"")
		end := strings.LastIndex(line, "\"")
		if start < 0 || end <= start {
			continue
		}
		name := strings.TrimSpace(line[start+1 : end])
		if name == "" {
			continue
		}
		item := store.DeviceInfo{Name: name, Type: mode}
		switch mode {
		case "video":
			videos = append(videos, item)
		case "audio":
			audios = append(audios, item)
		}
	}
	return uniqueDevices(videos), uniqueDevices(audios), nil
}

func (s *Service) listUnixDevices(_ context.Context) ([]store.DeviceInfo, []store.DeviceInfo, error) {
	videos := make([]store.DeviceInfo, 0)
	matches, _ := filepath.Glob("/dev/video*")
	for _, match := range matches {
		videos = append(videos, store.DeviceInfo{Name: match, Type: "video"})
	}
	if len(videos) == 0 {
		videos = append(videos, store.DeviceInfo{Name: "No /dev/video* found", Type: "video"})
	}
	audios := make([]store.DeviceInfo, 0)
	if data, err := os.ReadFile("/proc/asound/cards"); err == nil {
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.Contains(line, "[") && strings.Contains(line, "]") {
				audios = append(audios, store.DeviceInfo{Name: line, Type: "audio"})
			}
		}
	}
	if len(audios) == 0 {
		audios = append(audios, store.DeviceInfo{Name: "No ALSA card found", Type: "audio"})
	}
	return uniqueDevices(videos), uniqueDevices(audios), nil
}

func uniqueDevices(items []store.DeviceInfo) []store.DeviceInfo {
	seen := map[string]struct{}{}
	result := make([]store.DeviceInfo, 0, len(items))
	for _, item := range items {
		if _, ok := seen[item.Name+"|"+item.Type]; ok {
			continue
		}
		seen[item.Name+"|"+item.Type] = struct{}{}
		result = append(result, item)
	}
	return result
}

type ffprobeOutput struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
	Streams []struct {
		CodecType string `json:"codec_type"`
	} `json:"streams"`
}

func (s *Service) AnalyzeMedia(ctx context.Context, filePath string) (string, error) {
	output, err := runCombined(ctx, s.FFprobePath(),
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		filePath,
	)
	if err != nil {
		return "", err
	}
	var parsed ffprobeOutput
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		return "", err
	}
	normalized, err := json.Marshal(parsed)
	if err != nil {
		return "", err
	}
	return string(normalized), nil
}

func runCombined(ctx context.Context, bin string, args ...string) (string, error) {
	if ctx == nil {
		tmpCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		ctx = tmpCtx
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("%w: %s", err, out.String())
	}
	return out.String(), nil
}

func normalizeBinaryPath(path string, tool string) string {
	value := strings.TrimSpace(strings.Trim(path, "\""))
	if value == "" {
		return defaultToolBinaryName(tool)
	}
	info, err := os.Stat(value)
	if err == nil && info.IsDir() {
		candidate := filepath.Join(value, defaultToolBinaryName(tool))
		if candidateInfo, candidateErr := os.Stat(candidate); candidateErr == nil && !candidateInfo.IsDir() {
			return candidate
		}
		// Keep the joined candidate even if it does not exist yet; this is
		// still more useful than trying to exec a directory path.
		return candidate
	}
	if runtime.GOOS == "windows" && filepath.Ext(value) == "" {
		candidate := value + ".exe"
		if candidateInfo, candidateErr := os.Stat(candidate); candidateErr == nil && !candidateInfo.IsDir() {
			return candidate
		}
	}
	return value
}

func defaultToolBinaryName(tool string) string {
	name := strings.TrimSpace(tool)
	if name == "" {
		name = "ffmpeg"
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(name), ".exe") {
		return name + ".exe"
	}
	return name
}
