package stream

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"bilibililivetools/gover/backend/store"
)

type BuildContext struct {
	Setting       *store.PushSetting
	Live          *store.LiveSetting
	StreamURL     string
	MediaDir      string
	VideoMaterial *store.Material
	AudioMaterial *store.Material
	FFmpegPath    string
}

func BuildCommand(ctx BuildContext) (string, []string, error) {
	if ctx.Setting == nil {
		return "", nil, errors.New("missing push setting")
	}
	if strings.TrimSpace(ctx.StreamURL) == "" {
		return "", nil, errors.New("missing stream url")
	}

	if ctx.Setting.Model == store.ConfigModelAdvance {
		return buildAdvanceCommand(ctx)
	}
	return buildNormalCommand(ctx)
}

func buildAdvanceCommand(ctx BuildContext) (string, []string, error) {
	cmdLine := strings.TrimSpace(ctx.Setting.FFmpegCommand)
	if cmdLine == "" {
		return "", nil, errors.New("ffmpeg command is empty")
	}
	cmdLine = strings.ReplaceAll(cmdLine, "{URL}", ctx.StreamURL)
	parts, err := splitCommandLine(cmdLine)
	if err != nil {
		return "", nil, err
	}
	if len(parts) == 0 {
		return "", nil, errors.New("invalid ffmpeg command")
	}
	bin := parts[0]
	if strings.EqualFold(bin, "ffmpeg") || strings.EqualFold(bin, "ffmpeg.exe") {
		bin = ctx.FFmpegPath
	}
	return bin, parts[1:], nil
}

func buildNormalCommand(ctx BuildContext) (string, []string, error) {
	args := make([]string, 0, 64)
	forceVideoTranscode := false
	addOutput := func(hasAudio bool) {
		codec := strings.TrimSpace(ctx.Setting.CustomVideoCodec)
		if codec == "" {
			codec = "libx264"
		}
		useCopy := ctx.Setting.OutputQuality == store.OutputQualityOriginal && !forceVideoTranscode
		if useCopy {
			args = append(args, "-c:v", "copy")
		} else {
			targetQuality := ctx.Setting.OutputQuality
			if targetQuality == store.OutputQualityOriginal {
				// MJPEG/USB/desktop/mosaic pipelines cannot stream-copy to FLV reliably.
				targetQuality = store.OutputQualityMedium
			}
			quality := qualityPreset(targetQuality)
			bitrate := quality.Bitrate
			bufSize := quality.BufSize
			if customBitrate := normalizeBitrateKbps(ctx.Setting.OutputBitrateKbps); customBitrate > 0 {
				bitrate = fmt.Sprintf("%dk", customBitrate)
				bufSize = fmt.Sprintf("%dk", customBitrate*2)
			}
			args = append(args,
				"-vcodec", codec,
				"-pix_fmt", "yuv420p",
				"-r", "30",
				"-g", "30",
				"-b:v", bitrate,
				"-maxrate", bitrate,
				"-bufsize", bufSize,
				"-preset", quality.Preset,
				"-crf", quality.CRF,
				"-tune", "zerolatency",
			)
		}
		if !useCopy && strings.TrimSpace(ctx.Setting.OutputResolution) != "" {
			args = append(args, "-s", ctx.Setting.OutputResolution)
		}
		if strings.TrimSpace(ctx.Setting.CustomOutputParams) != "" {
			parts, _ := splitCommandLine(ctx.Setting.CustomOutputParams)
			args = append(args, parts...)
		}
		if hasAudio {
			args = append(args, "-acodec", "aac", "-ac", "2", "-ar", "44100", "-b:a", "128k")
		} else {
			args = append(args, "-an")
		}
		args = append(args, "-f", "flv", ctx.StreamURL)
	}

	hasAudio := false
	if ctx.Setting.MultiInputEnabled && (len(ctx.Setting.MultiInputURLs) >= 2 || len(ctx.Setting.MultiInputMeta) >= 2) {
		forceVideoTranscode = true
		if err := appendMosaicInputArgs(ctx, &args, &hasAudio); err != nil {
			return "", nil, err
		}
		if ctx.Setting.IsMute && ctx.AudioMaterial == nil {
			hasAudio = false
		}
		addOutput(hasAudio)
		return ctx.FFmpegPath, args, nil
	}

	switch ctx.Setting.InputType {
	case store.InputTypeVideo:
		if ctx.VideoMaterial == nil {
			return "", nil, errors.New("video material is required")
		}
		videoPath := filepath.Join(ctx.MediaDir, filepath.FromSlash(ctx.VideoMaterial.Path))
		args = append(args, "-re", "-stream_loop", "-1", "-i", videoPath)
		if ctx.AudioMaterial != nil {
			audioPath := filepath.Join(ctx.MediaDir, filepath.FromSlash(ctx.AudioMaterial.Path))
			args = append(args, "-stream_loop", "-1", "-i", audioPath)
			args = append(args, "-map", "0:v:0", "-map", "1:a:0")
			hasAudio = true
		} else if !ctx.Setting.IsMute {
			hasAudio = true
		}

	case store.InputTypeUSBCamera, store.InputTypeCameraPlus:
		forceVideoTranscode = true
		deviceName := strings.TrimSpace(ctx.Setting.InputDeviceName)
		if deviceName == "" {
			return "", nil, errors.New("camera device is required")
		}
		res := strings.TrimSpace(ctx.Setting.InputDeviceResolution)
		if res == "" {
			res = "1280x720"
		}
		fps := "30"
		if ctx.Setting.InputDeviceFramerate > 0 {
			fps = fmt.Sprintf("%d", ctx.Setting.InputDeviceFramerate)
		}
		if runtime.GOOS == "windows" {
			args = append(args, "-f", "dshow", "-video_size", res, "-framerate", fps, "-i", fmt.Sprintf("video=%q", deviceName))
		} else {
			args = append(args, "-f", "v4l2", "-video_size", res, "-framerate", fps, "-i", deviceName)
		}
		if ctx.Setting.InputAudioSource == store.InputAudioSourceDevice && strings.TrimSpace(ctx.Setting.InputAudioDeviceName) != "" {
			if runtime.GOOS == "windows" {
				args = append(args, "-f", "dshow", "-i", fmt.Sprintf("audio=%q", ctx.Setting.InputAudioDeviceName))
			} else {
				args = append(args, "-f", "alsa", "-i", ctx.Setting.InputAudioDeviceName)
			}
			args = append(args, "-map", "0:v:0", "-map", "1:a:0")
			hasAudio = true
		} else if ctx.AudioMaterial != nil {
			audioPath := filepath.Join(ctx.MediaDir, filepath.FromSlash(ctx.AudioMaterial.Path))
			args = append(args, "-stream_loop", "-1", "-i", audioPath, "-map", "0:v:0", "-map", "1:a:0")
			hasAudio = true
		}

	case store.InputTypeDesktop:
		forceVideoTranscode = true
		if runtime.GOOS == "windows" {
			args = append(args, "-f", "gdigrab", "-framerate", "30", "-i", "desktop")
		} else {
			args = append(args, "-f", "x11grab", "-framerate", "30", "-i", ":0.0")
		}
		if ctx.Setting.InputAudioSource == store.InputAudioSourceDevice && strings.TrimSpace(ctx.Setting.InputAudioDeviceName) != "" {
			if runtime.GOOS == "windows" {
				args = append(args, "-f", "dshow", "-i", fmt.Sprintf("audio=%q", ctx.Setting.InputAudioDeviceName))
			} else {
				args = append(args, "-f", "alsa", "-i", ctx.Setting.InputAudioDeviceName)
			}
			args = append(args, "-map", "0:v:0", "-map", "1:a:0")
			hasAudio = true
		} else if ctx.AudioMaterial != nil {
			audioPath := filepath.Join(ctx.MediaDir, filepath.FromSlash(ctx.AudioMaterial.Path))
			args = append(args, "-stream_loop", "-1", "-i", audioPath, "-map", "0:v:0", "-map", "1:a:0")
			hasAudio = true
		}

	case store.InputTypeRTSP:
		if strings.TrimSpace(ctx.Setting.RTSPURL) == "" {
			return "", nil, errors.New("rtsp url is required")
		}
		// RTSP cameras vary a lot; force transcode + conservative probe settings for stability.
		forceVideoTranscode = true
		args = appendRTSPInputArgs(args, strings.TrimSpace(ctx.Setting.RTSPURL))
		if ctx.AudioMaterial != nil {
			audioPath := filepath.Join(ctx.MediaDir, filepath.FromSlash(ctx.AudioMaterial.Path))
			args = append(args, "-stream_loop", "-1", "-i", audioPath)
			args = append(args, "-map", "0:v:0", "-map", "1:a:0")
			hasAudio = true
		} else {
			args = append(args, "-map", "0:v:0")
			if !ctx.Setting.IsMute {
				args = append(args, "-map", "0:a:0?")
				hasAudio = true
			}
		}

	case store.InputTypeMJPEG:
		forceVideoTranscode = true
		if strings.TrimSpace(ctx.Setting.MJPEGURL) == "" {
			return "", nil, errors.New("mjpeg url is required")
		}
		args = append(args, "-f", "mjpeg", "-i", strings.TrimSpace(ctx.Setting.MJPEGURL))
		if ctx.AudioMaterial != nil {
			audioPath := filepath.Join(ctx.MediaDir, filepath.FromSlash(ctx.AudioMaterial.Path))
			args = append(args, "-stream_loop", "-1", "-i", audioPath, "-map", "0:v:0", "-map", "1:a:0")
			hasAudio = true
		}

	case store.InputTypeRTMP:
		if strings.TrimSpace(ctx.Setting.RTMPURL) == "" {
			return "", nil, errors.New("rtmp url is required")
		}
		args = append(args, "-i", strings.TrimSpace(ctx.Setting.RTMPURL))
		if ctx.AudioMaterial != nil {
			audioPath := filepath.Join(ctx.MediaDir, filepath.FromSlash(ctx.AudioMaterial.Path))
			args = append(args, "-stream_loop", "-1", "-i", audioPath, "-map", "0:v:0", "-map", "1:a:0")
			hasAudio = true
		} else if !ctx.Setting.IsMute {
			hasAudio = true
		}

	case store.InputTypeGB28181:
		gbURL := normalizeGBPullURL(strings.TrimSpace(ctx.Setting.GBPullURL))
		if gbURL == "" {
			return "", nil, errors.New("gb28181 pull url is required")
		}
		if isSDPSource(gbURL) {
			forceVideoTranscode = true
			args = append(args, "-protocol_whitelist", "file,udp,rtp,tcp", "-fflags", "+genpts", "-i", gbURL)
		} else if isRTSPSource(gbURL) {
			args = appendRTSPInputArgs(args, gbURL)
		} else if looksLikeMJPEG(gbURL) {
			forceVideoTranscode = true
			args = append(args, "-f", "mjpeg", "-i", gbURL)
		} else {
			args = append(args, "-i", gbURL)
		}
		if ctx.AudioMaterial != nil {
			audioPath := filepath.Join(ctx.MediaDir, filepath.FromSlash(ctx.AudioMaterial.Path))
			args = append(args, "-stream_loop", "-1", "-i", audioPath, "-map", "0:v:0", "-map", "1:a:0")
			hasAudio = true
		} else if !ctx.Setting.IsMute {
			hasAudio = true
		}

	case store.InputTypeONVIF:
		if strings.TrimSpace(ctx.Setting.RTSPURL) == "" {
			return "", nil, errors.New("onvif input currently expects a resolved rtsp url")
		}
		// ONVIF preview/push source is RTSP under the hood; apply same robust RTSP defaults.
		forceVideoTranscode = true
		args = appendRTSPInputArgs(args, strings.TrimSpace(ctx.Setting.RTSPURL))
		args = append(args, "-map", "0:v:0")
		if !ctx.Setting.IsMute {
			args = append(args, "-map", "0:a:0?")
			hasAudio = true
		}

	default:
		return "", nil, fmt.Errorf("unsupported input type: %s", ctx.Setting.InputType)
	}

	if ctx.Setting.IsMute && ctx.AudioMaterial == nil && ctx.Setting.InputAudioSource != store.InputAudioSourceDevice {
		hasAudio = false
	}
	addOutput(hasAudio)
	return ctx.FFmpegPath, args, nil
}

type quality struct {
	Bitrate string
	BufSize string
	Preset  string
	CRF     string
}

func qualityPreset(level store.OutputQuality) quality {
	switch level {
	case store.OutputQualityHigh:
		return quality{Bitrate: "8000k", BufSize: "16000k", Preset: "fast", CRF: "23"}
	case store.OutputQualityLow:
		return quality{Bitrate: "2000k", BufSize: "4000k", Preset: "ultrafast", CRF: "33"}
	default:
		return quality{Bitrate: "4000k", BufSize: "8000k", Preset: "veryfast", CRF: "28"}
	}
}

func normalizeBitrateKbps(value int) int {
	if value <= 0 {
		return 0
	}
	if value > 120000 {
		return 120000
	}
	return value
}

func splitCommandLine(command string) ([]string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, errors.New("empty command")
	}
	parts := make([]string, 0)
	var current strings.Builder
	inQuote := false
	quoteChar := rune(0)
	escaped := false
	for _, r := range command {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case inQuote && r == quoteChar:
			inQuote = false
		case !inQuote && (r == '"' || r == '\''):
			inQuote = true
			quoteChar = r
		case !inQuote && (r == ' ' || r == '\t' || r == '\n' || r == '\r'):
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if escaped || inQuote {
		return nil, errors.New("invalid command quoting")
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts, nil
}

func appendMosaicInputArgs(ctx BuildContext, args *[]string, hasAudio *bool) error {
	sources := normalizeMultiSources(ctx.Setting.MultiInputMeta, ctx.Setting.MultiInputURLs, 9)
	if len(sources) < 2 {
		return errors.New("multi input requires at least 2 sources")
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

	if ctx.AudioMaterial != nil {
		audioPath := filepath.Join(ctx.MediaDir, filepath.FromSlash(ctx.AudioMaterial.Path))
		audioInputIndex := len(sources)
		*args = append(*args, "-stream_loop", "-1", "-i", audioPath)
		*args = append(*args, "-map", fmt.Sprintf("%d:a:0", audioInputIndex))
		*hasAudio = true
		return nil
	}

	if !ctx.Setting.IsMute {
		*args = append(*args, "-map", "0:a:0?")
		*hasAudio = true
	}
	return nil
}

func buildGridMosaicFilter(sources []store.MultiInputSource, cols int, rows int, outW int, outH int) (string, error) {
	tileW := outW / cols
	tileH := outH / rows
	if tileW <= 0 || tileH <= 0 {
		return "", errors.New("invalid grid mosaic tile resolution")
	}
	filters := make([]string, 0, len(sources)+1)
	layoutItems := make([]string, 0, len(sources))
	stackInput := make([]string, 0, len(sources))
	for idx, source := range sources {
		label := fmt.Sprintf("vs%d", idx)
		x := (idx % cols) * tileW
		y := (idx / cols) * tileH
		layoutItems = append(layoutItems, fmt.Sprintf("%d_%d", x, y))
		stackInput = append(stackInput, fmt.Sprintf("[%s]", label))
		filters = append(filters, buildMosaicInputFilter(idx, label, tileW, tileH, source.Title))
	}
	filters = append(filters, fmt.Sprintf("%sxstack=inputs=%d:layout=%s[vout]", strings.Join(stackInput, ""), len(sources), strings.Join(layoutItems, "|")))
	return strings.Join(filters, ";"), nil
}

func buildFocusMosaicFilter(sources []store.MultiInputSource, outW int, outH int) (string, error) {
	if len(sources) < 2 {
		return "", errors.New("focus mosaic requires at least 2 sources")
	}
	mainW := (outW * 2) / 3
	if mainW <= 0 || mainW >= outW {
		mainW = outW / 2
	}
	mainH := outH
	sideW := outW - mainW
	if sideW <= 0 || mainH <= 0 {
		return "", errors.New("invalid focus mosaic resolution")
	}

	filters := make([]string, 0, len(sources)+6)
	filters = append(filters, buildMosaicInputFilter(0, "vmain", mainW, mainH, sources[0].Title))
	secondaryCount := len(sources) - 1

	secCols, secRows := parseMosaicLayout("2x2", secondaryCount)
	if secCols*secRows < secondaryCount {
		secRows = int(math.Ceil(float64(secondaryCount) / float64(secCols)))
	}
	secW := sideW / secCols
	secH := mainH / secRows
	if secW <= 0 || secH <= 0 {
		return "", errors.New("invalid focus mosaic secondary resolution")
	}
	secLayout := make([]string, 0, secondaryCount)
	secInput := make([]string, 0, secondaryCount)
	for idx := 1; idx < len(sources); idx++ {
		secIndex := idx - 1
		label := fmt.Sprintf("vsec%d", secIndex)
		x := (secIndex % secCols) * secW
		y := (secIndex / secCols) * secH
		secLayout = append(secLayout, fmt.Sprintf("%d_%d", x, y))
		secInput = append(secInput, fmt.Sprintf("[%s]", label))
		filters = append(filters, buildMosaicInputFilter(idx, label, secW, secH, sources[idx].Title))
	}
	filters = append(filters, fmt.Sprintf("%sxstack=inputs=%d:layout=%s[vright]", strings.Join(secInput, ""), secondaryCount, strings.Join(secLayout, "|")))
	filters = append(filters, fmt.Sprintf("color=c=black:s=%dx%d[base]", outW, outH))
	filters = append(filters, "[base][vmain]overlay=0:0[stage1]")
	filters = append(filters, fmt.Sprintf("[stage1][vright]overlay=%d:0[vout]", mainW))
	return strings.Join(filters, ";"), nil
}

type canvasMosaicNode struct {
	InputIndex int
	Label      string
	X          int
	Y          int
	W          int
	H          int
	Z          int
	Title      string
}

func buildCanvasMosaicFilter(sources []store.MultiInputSource, outW int, outH int) (string, error) {
	if len(sources) < 2 {
		return "", errors.New("canvas mosaic requires at least 2 sources")
	}
	defaultRects := defaultCanvasRects(len(sources))
	nodes := make([]canvasMosaicNode, 0, len(sources))
	for idx, source := range sources {
		rect := defaultRects[idx]
		hasCustomRect := source.W > 0 || source.H > 0
		xNorm := rect.X
		yNorm := rect.Y
		wNorm := rect.W
		hNorm := rect.H
		if hasCustomRect {
			xNorm = clampCanvasNumber(source.X, rect.X, 0, 1)
			yNorm = clampCanvasNumber(source.Y, rect.Y, 0, 1)
			wNorm = clampCanvasNumber(source.W, rect.W, 0.08, 1)
			hNorm = clampCanvasNumber(source.H, rect.H, 0.08, 1)
		}
		if xNorm+wNorm > 1 {
			xNorm = 1 - wNorm
		}
		if yNorm+hNorm > 1 {
			yNorm = 1 - hNorm
		}
		if xNorm < 0 {
			xNorm = 0
		}
		if yNorm < 0 {
			yNorm = 0
		}

		node := canvasMosaicNode{
			InputIndex: idx,
			Label:      fmt.Sprintf("vc%d", idx),
			X:          int(math.Round(float64(outW) * xNorm)),
			Y:          int(math.Round(float64(outH) * yNorm)),
			W:          int(math.Round(float64(outW) * wNorm)),
			H:          int(math.Round(float64(outH) * hNorm)),
			Z:          source.Z,
			Title:      source.Title,
		}
		if node.W < 64 {
			node.W = 64
		}
		if node.H < 36 {
			node.H = 36
		}
		if node.X+node.W > outW {
			node.X = outW - node.W
		}
		if node.Y+node.H > outH {
			node.Y = outH - node.H
		}
		if node.X < 0 {
			node.X = 0
		}
		if node.Y < 0 {
			node.Y = 0
		}
		nodes = append(nodes, node)
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Z == nodes[j].Z {
			return nodes[i].InputIndex < nodes[j].InputIndex
		}
		return nodes[i].Z < nodes[j].Z
	})

	filters := make([]string, 0, len(nodes)*2+3)
	for _, node := range nodes {
		filters = append(filters, buildMosaicInputFilter(node.InputIndex, node.Label, node.W, node.H, node.Title))
	}
	filters = append(filters, fmt.Sprintf("color=c=black:s=%dx%d[canvas0]", outW, outH))
	previous := "canvas0"
	for idx, node := range nodes {
		current := fmt.Sprintf("canvas%d", idx+1)
		filters = append(filters, fmt.Sprintf("[%s][%s]overlay=%d:%d[%s]", previous, node.Label, node.X, node.Y, current))
		previous = current
	}
	filters = append(filters, fmt.Sprintf("[%s]copy[vout]", previous))
	return strings.Join(filters, ";"), nil
}

type canvasRect struct {
	X float64
	Y float64
	W float64
	H float64
}

func defaultCanvasRects(sourceCount int) []canvasRect {
	if sourceCount <= 0 {
		return []canvasRect{}
	}
	cols, rows := parseMosaicLayout("", sourceCount)
	if cols <= 0 {
		cols = 1
	}
	if rows <= 0 {
		rows = 1
	}
	cellW := 1.0 / float64(cols)
	cellH := 1.0 / float64(rows)
	marginX := math.Min(0.01, cellW*0.08)
	marginY := math.Min(0.01, cellH*0.08)
	items := make([]canvasRect, 0, sourceCount)
	for idx := 0; idx < sourceCount; idx++ {
		col := idx % cols
		row := idx / cols
		x := float64(col)*cellW + marginX
		y := float64(row)*cellH + marginY
		w := cellW - marginX*2
		h := cellH - marginY*2
		if w < 0.08 {
			w = 0.08
		}
		if h < 0.08 {
			h = 0.08
		}
		if x+w > 1 {
			x = 1 - w
		}
		if y+h > 1 {
			y = 1 - h
		}
		items = append(items, canvasRect{X: x, Y: y, W: w, H: h})
	}
	return items
}

func clampCanvasNumber(value float64, fallback float64, min float64, max float64) float64 {
	if !isFiniteNumber(value) {
		value = fallback
	}
	if value < min {
		value = min
	}
	if value > max {
		value = max
	}
	return value
}

func isFiniteNumber(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func buildMosaicInputFilter(index int, label string, width int, height int, title string) string {
	base := fmt.Sprintf("[%d:v]fps=30,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:black", index, width, height, width, height)
	title = strings.TrimSpace(title)
	if title == "" {
		return base + fmt.Sprintf("[%s]", label)
	}
	return base + fmt.Sprintf(",drawtext=text='%s':x=12:y=h-th-12:fontsize=20:fontcolor=white:box=1:boxcolor=black@0.45:boxborderw=6[%s]", escapeDrawtextText(title), label)
}

func escapeDrawtextText(text string) string {
	text = strings.TrimSpace(text)
	if len(text) > 80 {
		text = text[:80]
	}
	replacer := strings.NewReplacer(
		`\\`, `\\\\`,
		`:`, `\:`,
		`'`, `\'`,
		`%`, `\%`,
		`,`, `\,`,
		`[`, `\[`,
		`]`, `\]`,
	)
	return replacer.Replace(text)
}

func normalizeMultiSources(meta []store.MultiInputSource, urls []string, max int) []store.MultiInputSource {
	if max <= 0 {
		max = 9
	}
	seen := map[string]struct{}{}
	result := make([]store.MultiInputSource, 0, max)
	appendSource := func(item store.MultiInputSource) {
		value := strings.TrimSpace(item.URL)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		item.URL = value
		item.Title = strings.TrimSpace(item.Title)
		item.SourceType = strings.TrimSpace(item.SourceType)
		if item.MaterialID < 0 {
			item.MaterialID = 0
		}
		result = append(result, item)
	}
	for _, item := range meta {
		appendSource(item)
		if len(result) >= max {
			break
		}
	}
	for _, value := range urls {
		appendSource(store.MultiInputSource{URL: value})
		if len(result) >= max {
			break
		}
	}
	return result
}

func prioritizePrimarySource(items []store.MultiInputSource) {
	if len(items) == 0 {
		return
	}
	primary := 0
	for idx := range items {
		if items[idx].Primary {
			primary = idx
			break
		}
	}
	if primary > 0 {
		items[0], items[primary] = items[primary], items[0]
	}
	for idx := range items {
		items[idx].Primary = idx == 0
	}
}

func resolveMosaicSourceURL(raw string, mediaDir string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "file://") {
		return strings.TrimSpace(raw[len("file://"):])
	}
	if strings.HasPrefix(lower, "media://") {
		relative := strings.TrimPrefix(raw, "media://")
		if mediaDir == "" {
			return relative
		}
		return filepath.Join(mediaDir, filepath.FromSlash(relative))
	}
	if strings.Contains(raw, "://") {
		return raw
	}
	if filepath.IsAbs(raw) {
		return raw
	}
	if mediaDir != "" {
		return filepath.Join(mediaDir, filepath.FromSlash(raw))
	}
	return raw
}

func isRTSPSource(sourceURL string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(sourceURL)), "rtsp://")
}

func parseMosaicLayout(layout string, sourceCount int) (int, int) {
	layout = strings.TrimSpace(strings.ToLower(layout))
	if strings.Contains(layout, "x") {
		parts := strings.Split(layout, "x")
		if len(parts) == 2 {
			colValue, colErr := strconv.Atoi(strings.TrimSpace(parts[0]))
			rowValue, rowErr := strconv.Atoi(strings.TrimSpace(parts[1]))
			if colErr == nil && rowErr == nil && colValue > 0 && rowValue > 0 {
				if colValue*rowValue < sourceCount {
					needRows := int(math.Ceil(float64(sourceCount) / float64(colValue)))
					return colValue, needRows
				}
				return colValue, rowValue
			}
		}
	}
	cols := int(math.Ceil(math.Sqrt(float64(sourceCount))))
	if cols <= 0 {
		cols = 1
	}
	rows := int(math.Ceil(float64(sourceCount) / float64(cols)))
	if rows <= 0 {
		rows = 1
	}
	return cols, rows
}

func parseOutputResolution(raw string) (int, int) {
	parts := strings.Split(strings.TrimSpace(raw), "x")
	if len(parts) != 2 {
		return 1280, 720
	}
	width, widthErr := strconv.Atoi(strings.TrimSpace(parts[0]))
	height, heightErr := strconv.Atoi(strings.TrimSpace(parts[1]))
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return 1280, 720
	}
	return width, height
}

func looksLikeMJPEG(sourceURL string) bool {
	lower := strings.ToLower(strings.TrimSpace(sourceURL))
	if strings.Contains(lower, "mjpeg") || strings.Contains(lower, "mjpg") {
		return true
	}
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return strings.Contains(lower, "mjpeg") || strings.Contains(lower, "mjpg")
	}
	return false
}

func appendRTSPInputArgs(args []string, sourceURL string) []string {
	sourceURL = strings.TrimSpace(sourceURL)
	if sourceURL == "" {
		return args
	}
	// Keep RTSP pull conservative while allowing non-standard devices to negotiate UDP fallback.
	args = append(args,
		"-rtsp_flags", "prefer_tcp",
		"-rw_timeout", "60000000",
		"-thread_queue_size", "1024",
		"-analyzeduration", "10000000",
		"-probesize", "5000000",
		"-fflags", "+genpts+discardcorrupt",
		"-use_wallclock_as_timestamps", "1",
		"-i", sourceURL,
	)
	return args
}
