package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const ffmpegReleaseBaseURL = "https://github.com/SoraKasvgano/BilibiliLiveToolsGolang/releases/download/ffmpeg/"

var autoCDNPrefixes = []string{"", "https://gh-proxy.org/", "https://hk.gh-proxy.org/", "https://gitproxy.click/"}

type InstallOptions struct {
	DataDir          string
	CDNPrefix        string
	ProgressCallback func(InstallStep)
}

type InstallStep struct {
	Stage    string `json:"stage"`
	Progress int    `json:"progress"`
	Message  string `json:"message"`
}

type InstallResult struct {
	OS          string        `json:"os"`
	Arch        string        `json:"arch"`
	AssetName   string        `json:"assetName"`
	SourceURL   string        `json:"sourceUrl"`
	DownloadURL string        `json:"downloadUrl"`
	ArchivePath string        `json:"archivePath"`
	InstallDir  string        `json:"installDir"`
	FFmpegPath  string        `json:"ffmpegPath"`
	FFprobePath string        `json:"ffprobePath,omitempty"`
	Bytes       int64         `json:"bytes"`
	Progress    []InstallStep `json:"progress"`
}

type releaseAsset struct {
	name string
}

func InstallRelease(ctx context.Context, opts InstallOptions) (*InstallResult, error) {
	asset, err := resolveReleaseAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return nil, err
	}

	dataDir := strings.TrimSpace(opts.DataDir)
	if dataDir == "" {
		dataDir = "data"
	}
	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}

	sourceURL := ffmpegReleaseBaseURL + asset.name
	archivePath := filepath.Join(absDataDir, "downloads", asset.name)
	installDir := absDataDir

	result := &InstallResult{
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		AssetName:   asset.name,
		SourceURL:   sourceURL,
		DownloadURL: sourceURL,
		ArchivePath: archivePath,
		InstallDir:  installDir,
		Progress:    make([]InstallStep, 0, 8),
	}

	emit := func(step InstallStep, keepHistory bool) {
		step.Progress = clampPercent(step.Progress)
		if keepHistory {
			result.Progress = append(result.Progress, step)
		}
		if opts.ProgressCallback != nil {
			opts.ProgressCallback(step)
		}
	}

	emit(InstallStep{Stage: "detect", Progress: 12, Message: fmt.Sprintf("已匹配安装包 %s", asset.name)}, true)
	downloadURL := resolveDownloadURL(ctx, sourceURL, opts.CDNPrefix, emit)
	result.DownloadURL = downloadURL

	cleanupOnCancel := func() {
		_ = os.Remove(archivePath)
		_ = os.Remove(archivePath + ".tmp")
	}
	defer func() {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			cleanupOnCancel()
		}
	}()

	downloader := NewDownloaderWithProgress(func(progress DownloadProgress) {
		emit(buildDownloadStep(progress), false)
	})
	downloaded, err := downloader.Download(ctx, downloadURL, archivePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = os.Remove(archivePath)
	}()
	result.Bytes = downloaded.Size
	emit(InstallStep{Stage: "download", Progress: 72, Message: fmt.Sprintf("下载完成（%d 字节）", downloaded.Size)}, true)

	extracted, err := ExtractFFmpegArchive(archivePath, installDir)
	if err != nil {
		return nil, err
	}
	result.FFmpegPath = extracted.FFmpegPath
	result.FFprobePath = extracted.FFprobePath
	emit(InstallStep{Stage: "extract", Progress: 85, Message: "解压完成"}, true)

	if _, err := os.Stat(result.FFmpegPath); err != nil {
		return nil, err
	}
	emit(InstallStep{Stage: "verify", Progress: 95, Message: "二进制校验通过"}, true)

	return result, nil
}

func resolveDownloadURL(ctx context.Context, sourceURL string, prefix string, emit func(InstallStep, bool)) string {
	p := strings.TrimSpace(prefix)
	if !isAutoCDNPrefix(p) {
		return applyCDNPrefix(sourceURL, p)
	}
	if emit != nil {
		emit(InstallStep{Stage: "detect", Progress: 16, Message: "正在测速 CDN 与直连 GitHub..."}, true)
	}
	urlValue, label, err := selectBestDownloadURL(ctx, sourceURL)
	if err != nil {
		if emit != nil {
			emit(InstallStep{Stage: "detect", Progress: 20, Message: "自动测速失败，回退为直连 GitHub"}, true)
		}
		return sourceURL
	}
	if emit != nil {
		emit(InstallStep{Stage: "detect", Progress: 20, Message: fmt.Sprintf("已选择下载线路：%s", label)}, true)
	}
	return urlValue
}

func isAutoCDNPrefix(prefix string) bool {
	p := strings.TrimSpace(prefix)
	return p == "__auto__" || strings.EqualFold(p, "auto")
}

func selectBestDownloadURL(ctx context.Context, sourceURL string) (string, string, error) {
	type candidate struct {
		label string
		url   string
	}
	candidates := make([]candidate, 0, len(autoCDNPrefixes))
	for _, prefix := range autoCDNPrefixes {
		label := strings.TrimSpace(prefix)
		if label == "" {
			label = "direct-github"
		}
		candidates = append(candidates, candidate{label: label, url: applyCDNPrefix(sourceURL, prefix)})
	}
	client := &http.Client{Timeout: 10 * time.Second}

	bestURL := ""
	bestLabel := ""
	bestSpeed := 0.0
	bestLatency := time.Duration(1<<63 - 1)

	for _, c := range candidates {
		speed, latency, err := probeDownloadURL(ctx, client, c.url)
		if err != nil {
			continue
		}
		if speed <= 0 {
			continue
		}
		if bestURL == "" || speed > bestSpeed || (speed >= bestSpeed*0.95 && latency < bestLatency) {
			bestURL = c.url
			bestLabel = c.label
			bestSpeed = speed
			bestLatency = latency
		}
	}
	if bestURL == "" {
		return "", "", fmt.Errorf("no reachable candidate")
	}
	return bestURL, bestLabel, nil
}

func probeDownloadURL(ctx context.Context, client *http.Client, urlValue string) (float64, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlValue, nil)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Range", "bytes=0-262143")
	req.Header.Set("User-Agent", "gover-ffmpeg-cdn-probe/1.0")

	startAt := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return 0, 0, fmt.Errorf("status %d", resp.StatusCode)
	}

	readBytes, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 262144))
	if err != nil {
		return 0, 0, err
	}
	if readBytes <= 0 {
		return 0, 0, fmt.Errorf("no bytes read")
	}
	latency := time.Since(startAt)
	if latency <= 0 {
		latency = time.Millisecond
	}
	speed := float64(readBytes) / latency.Seconds()
	return speed, latency, nil
}

func buildDownloadStep(progress DownloadProgress) InstallStep {
	downloaded := progress.DownloadedBytes
	if downloaded < 0 {
		downloaded = 0
	}
	total := progress.TotalBytes
	overall := 30
	message := fmt.Sprintf("下载中（已下载 %s）", formatBytes(downloaded))
	if total > 0 {
		percent := clampPercent(progress.Percent)
		overall = 20 + (percent * 50 / 100)
		message = fmt.Sprintf("下载中 %d%%（%s / %s）", percent, formatBytes(downloaded), formatBytes(total))
	}
	overall = clampPercent(overall)
	if overall < 20 {
		overall = 20
	}
	if overall > 70 {
		overall = 70
	}
	return InstallStep{Stage: "download", Progress: overall, Message: message}
}

func formatBytes(value int64) string {
	if value < 0 {
		value = 0
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v := float64(value)
	idx := 0
	for v >= 1024 && idx < len(units)-1 {
		v /= 1024
		idx++
	}
	if idx == 0 {
		return fmt.Sprintf("%d%s", int64(v), units[idx])
	}
	return fmt.Sprintf("%.1f%s", v, units[idx])
}

func clampPercent(value int) int {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func resolveReleaseAsset(goos string, goarch string) (*releaseAsset, error) {
	switch goos {
	case "windows":
		if goarch == "amd64" {
			return &releaseAsset{name: "ffmpeg-win-x64.zip"}, nil
		}
		return nil, fmt.Errorf("unsupported windows arch: %s", goarch)
	case "linux":
		switch goarch {
		case "amd64":
			return &releaseAsset{name: "ffmpeg-linux-x64.zip"}, nil
		case "arm64":
			return &releaseAsset{name: "ffmpeg-linux-arm64.zip"}, nil
		case "arm":
			return &releaseAsset{name: "ffmpeg-linux-arm.zip"}, nil
		default:
			return nil, fmt.Errorf("unsupported linux arch: %s", goarch)
		}
	default:
		return nil, fmt.Errorf("unsupported os: %s", goos)
	}
}

func applyCDNPrefix(sourceURL string, prefix string) string {
	p := strings.TrimSpace(prefix)
	if p == "" {
		return sourceURL
	}
	if strings.Contains(p, "{url}") {
		return strings.ReplaceAll(p, "{url}", sourceURL)
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p + sourceURL
}
