package stream

import (
	"path/filepath"
	"runtime"
	"strings"
)

func isSDPSource(raw string) bool {
	value := strings.TrimSpace(raw)
	if value == "" {
		return false
	}
	lower := strings.ToLower(value)
	if strings.HasSuffix(lower, ".sdp") {
		return true
	}
	if strings.HasPrefix(lower, "file://") {
		return strings.HasSuffix(strings.SplitN(lower, "?", 2)[0], ".sdp")
	}
	return false
}

func normalizeGBPullURL(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	if !strings.HasPrefix(lower, "file://") {
		return value
	}
	pathValue := value[len("file://"):]
	if runtime.GOOS == "windows" {
		if len(pathValue) >= 3 && strings.HasPrefix(pathValue, "/") && pathValue[2] == ':' {
			pathValue = pathValue[1:]
		}
	}
	pathValue = filepath.FromSlash(pathValue)
	return strings.TrimSpace(pathValue)
}
