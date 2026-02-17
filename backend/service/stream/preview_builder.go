package stream

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"bilibililivetools/gover/backend/store"
)

type PreviewOptions struct {
	FPS   int
	Width int
}

func BuildPreviewCommand(ctx BuildContext, options PreviewOptions) (string, []string, error) {
	if ctx.Setting == nil {
		return "", nil, errors.New("missing push setting")
	}
	fps, width := normalizePreviewOptions(options)
	args := make([]string, 0, 64)
	args = append(args, "-hide_banner", "-loglevel", "warning")

	mappedVideo := false
	if ctx.Setting.MultiInputEnabled && (len(ctx.Setting.MultiInputURLs) >= 2 || len(ctx.Setting.MultiInputMeta) >= 2) {
		if err := appendMosaicPreviewInputArgs(ctx, &args); err != nil {
			return "", nil, err
		}
		mappedVideo = true
	} else {
		switch ctx.Setting.InputType {
		case store.InputTypeVideo:
			if ctx.VideoMaterial == nil {
				return "", nil, errors.New("video material is required")
			}
			videoPath := filepath.Join(ctx.MediaDir, filepath.FromSlash(ctx.VideoMaterial.Path))
			args = append(args, "-re", "-stream_loop", "-1", "-i", videoPath)
		case store.InputTypeUSBCamera, store.InputTypeCameraPlus:
			deviceName := strings.TrimSpace(ctx.Setting.InputDeviceName)
			if deviceName == "" {
				return "", nil, errors.New("camera device is required")
			}
			resolution := strings.TrimSpace(ctx.Setting.InputDeviceResolution)
			if resolution == "" {
				resolution = "1280x720"
			}
			fpsText := "30"
			if ctx.Setting.InputDeviceFramerate > 0 {
				fpsText = fmt.Sprintf("%d", ctx.Setting.InputDeviceFramerate)
			}
			if runtime.GOOS == "windows" {
				args = append(args, "-f", "dshow", "-video_size", resolution, "-framerate", fpsText, "-i", fmt.Sprintf("video=%q", deviceName))
			} else {
				args = append(args, "-f", "v4l2", "-video_size", resolution, "-framerate", fpsText, "-i", deviceName)
			}
		case store.InputTypeDesktop:
			if runtime.GOOS == "windows" {
				args = append(args, "-f", "gdigrab", "-framerate", "30", "-i", "desktop")
			} else {
				args = append(args, "-f", "x11grab", "-framerate", "30", "-i", ":0.0")
			}
		case store.InputTypeRTSP:
			streamURL := strings.TrimSpace(ctx.Setting.RTSPURL)
			if streamURL == "" {
				return "", nil, errors.New("rtsp url is required")
			}
			args = appendRTSPInputArgs(args, streamURL)
		case store.InputTypeMJPEG:
			streamURL := strings.TrimSpace(ctx.Setting.MJPEGURL)
			if streamURL == "" {
				return "", nil, errors.New("mjpeg url is required")
			}
			args = append(args, "-f", "mjpeg", "-i", streamURL)
		case store.InputTypeRTMP:
			streamURL := strings.TrimSpace(ctx.Setting.RTMPURL)
			if streamURL == "" {
				return "", nil, errors.New("rtmp url is required")
			}
			args = append(args, "-i", streamURL)
		case store.InputTypeGB28181:
			streamURL := normalizeGBPullURL(strings.TrimSpace(ctx.Setting.GBPullURL))
			if streamURL == "" {
				return "", nil, errors.New("gb28181 pull url is required")
			}
			if isSDPSource(streamURL) {
				args = append(args, "-protocol_whitelist", "file,udp,rtp,tcp", "-fflags", "+genpts", "-i", streamURL)
			} else if isRTSPSource(streamURL) {
				args = appendRTSPInputArgs(args, streamURL)
			} else if looksLikeMJPEG(streamURL) {
				args = append(args, "-f", "mjpeg", "-i", streamURL)
			} else {
				args = append(args, "-i", streamURL)
			}
		case store.InputTypeONVIF:
			streamURL := strings.TrimSpace(ctx.Setting.RTSPURL)
			if streamURL == "" {
				return "", nil, errors.New("onvif preview currently expects a resolved rtsp url")
			}
			args = appendRTSPInputArgs(args, streamURL)
		default:
			return "", nil, fmt.Errorf("unsupported input type: %s", ctx.Setting.InputType)
		}
	}

	if !mappedVideo {
		args = append(args, "-map", "0:v:0")
	}
	vf := fmt.Sprintf("fps=%d,scale=%d:-2:flags=lanczos", fps, width)
	args = append(args,
		"-an",
		"-vf", vf,
		"-c:v", "mjpeg",
		"-q:v", "6",
		"-f", "mpjpeg",
		"-",
	)
	return ctx.FFmpegPath, args, nil
}

func appendMosaicPreviewInputArgs(ctx BuildContext, args *[]string) error {
	sources := normalizeMultiSources(ctx.Setting.MultiInputMeta, ctx.Setting.MultiInputURLs, 9)
	if len(sources) < 2 {
		return errors.New("multi input preview requires at least 2 sources")
	}
	prioritizePrimarySource(sources)
	for idx := range sources {
		sources[idx].URL = resolveMosaicSourceURL(sources[idx].URL, ctx.MediaDir)
		if strings.TrimSpace(sources[idx].Title) == "" {
			sources[idx].Title = fmt.Sprintf("Source-%d", idx+1)
		}
	}
	for _, source := range sources {
		if isRTSPSource(source.URL) {
			*args = appendRTSPInputArgs(*args, source.URL)
			continue
		}
		if looksLikeMJPEG(source.URL) {
			*args = append(*args, "-f", "mjpeg", "-i", source.URL)
			continue
		}
		if isLikelyLocalMosaicFile(source.URL) {
			*args = append(*args, "-re", "-stream_loop", "-1", "-i", source.URL)
			continue
		}
		*args = append(*args, "-i", source.URL)
	}

	outW, outH := parseOutputResolution(ctx.Setting.OutputResolution)
	mode := strings.ToLower(strings.TrimSpace(ctx.Setting.MultiInputLayout))
	filterComplex := ""
	var err error
	if mode == "canvas" || mode == "free" || mode == "custom" {
		filterComplex, err = buildCanvasMosaicFilter(sources, outW, outH)
	} else if mode == "focus" || strings.HasPrefix(mode, "focus-") {
		filterComplex, err = buildFocusMosaicFilter(sources, outW, outH)
	} else {
		cols, rows := parseMosaicLayout(mode, len(sources))
		filterComplex, err = buildGridMosaicFilter(sources, cols, rows, outW, outH)
	}
	if err != nil {
		return err
	}
	*args = append(*args, "-filter_complex", filterComplex, "-map", "[vout]")
	return nil
}

func normalizePreviewOptions(options PreviewOptions) (int, int) {
	fps := options.FPS
	if fps <= 0 {
		fps = 8
	}
	if fps > 20 {
		fps = 20
	}
	width := options.Width
	if width <= 0 {
		width = 960
	}
	if width < 240 {
		width = 240
	}
	if width > 1920 {
		width = 1920
	}
	return fps, width
}

func isLikelyLocalMosaicFile(raw string) bool {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return false
	}
	if strings.HasPrefix(value, "rtsp://") ||
		strings.HasPrefix(value, "http://") ||
		strings.HasPrefix(value, "https://") ||
		strings.HasPrefix(value, "rtmp://") ||
		strings.HasPrefix(value, "udp://") ||
		strings.HasPrefix(value, "tcp://") {
		return false
	}
	return !strings.Contains(value, "://")
}
