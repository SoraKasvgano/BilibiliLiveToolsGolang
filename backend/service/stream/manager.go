package stream

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"bilibililivetools/gover/backend/service/bilibili"
	ffsvc "bilibililivetools/gover/backend/service/ffmpeg"
	"bilibililivetools/gover/backend/store"
)

type Manager struct {
	store     *store.Store
	ffmpeg    *ffsvc.Service
	bilibili  bilibili.Service
	mediaDir  string
	status    store.PushStatus
	logBuffer int
	debugLogs bool

	mu            sync.RWMutex
	cancel        context.CancelFunc
	cmd           *exec.Cmd
	running       bool
	logs          []store.FFmpegLogItem
	hevcHintShown bool
}

func NewManager(storeDB *store.Store, ff *ffsvc.Service, bili bilibili.Service, mediaDir string, logBuffer int, debugLogs bool) *Manager {
	if logBuffer <= 0 {
		logBuffer = 300
	}
	return &Manager{
		store:     storeDB,
		ffmpeg:    ff,
		bilibili:  bili,
		mediaDir:  mediaDir,
		status:    store.PushStatusStopped,
		logBuffer: logBuffer,
		debugLogs: debugLogs,
		logs:      make([]store.FFmpegLogItem, 0, logBuffer),
	}
}

func (m *Manager) UpdateDebug(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.debugLogs = enabled
}

func (m *Manager) Start(ctx context.Context, startup bool) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return nil
	}
	pushSetting, err := m.store.GetPushSetting(ctx)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if startup && !pushSetting.IsAutoRetry {
		m.mu.Unlock()
		return errors.New("auto retry is disabled, startup push skipped")
	}
	if !pushSetting.IsUpdate {
		m.mu.Unlock()
		return errors.New("push setting is not configured yet")
	}

	runCtx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.running = true
	m.status = store.PushStatusStarting
	m.logs = m.logs[:0]
	m.hevcHintShown = false
	m.mu.Unlock()

	go m.runLoop(runCtx)
	return nil
}

func (m *Manager) runLoop(ctx context.Context) {
	defer func() {
		m.mu.Lock()
		m.running = false
		m.cmd = nil
		m.cancel = nil
		m.status = store.PushStatusStopped
		m.mu.Unlock()
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		err := m.runOnce(ctx)
		if err != nil {
			m.addLog("Error", err.Error())
		}

		setting, settingErr := m.store.GetPushSetting(context.Background())
		if settingErr != nil {
			m.addLog("Error", "load push setting failed: "+settingErr.Error())
			return
		}
		if !setting.IsAutoRetry {
			m.addLog("Info", "auto retry disabled, stream loop ended")
			return
		}

		m.setStatus(store.PushStatusWaiting)
		wait := time.Duration(setting.RetryInterval)
		if wait < 30 {
			wait = 30
		}
		timer := time.NewTimer(wait * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (m *Manager) runOnce(ctx context.Context) error {
	setting, err := m.store.GetPushSetting(ctx)
	if err != nil {
		return err
	}
	live, err := m.store.GetLiveSetting(ctx)
	if err != nil {
		return err
	}

	var videoMaterial *store.Material
	if setting.VideoMaterialID != nil && *setting.VideoMaterialID > 0 {
		videoMaterial, err = m.store.GetMaterialByID(ctx, *setting.VideoMaterialID)
		if err != nil {
			return fmt.Errorf("video material not found: %w", err)
		}
	}
	var audioMaterial *store.Material
	if setting.AudioMaterialID != nil && *setting.AudioMaterialID > 0 {
		audioMaterial, err = m.store.GetMaterialByID(ctx, *setting.AudioMaterialID)
		if err != nil {
			return fmt.Errorf("audio material not found: %w", err)
		}
	}

	streamURL, err := m.bilibili.GetStreamURL(ctx, live)
	if err != nil {
		return err
	}

	cmdPath, args, err := BuildCommand(BuildContext{
		Setting:       setting,
		Live:          live,
		StreamURL:     streamURL,
		MediaDir:      m.mediaDir,
		VideoMaterial: videoMaterial,
		AudioMaterial: audioMaterial,
		FFmpegPath:    m.ffmpeg.BinaryPath(),
	})
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, cmdPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.cmd = cmd
	m.mu.Unlock()

	m.addLog("Info", "======================= start ffmpeg ====================")
	m.addLog("Info", cmdPath+" "+joinArgs(args))
	if err := cmd.Start(); err != nil {
		return err
	}
	m.setStatus(store.PushStatusRunning)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		m.collectPipe("Info", stdout)
	}()
	go func() {
		defer wg.Done()
		m.collectPipe("Error", stderr)
	}()

	err = cmd.Wait()
	wg.Wait()

	m.mu.Lock()
	m.cmd = nil
	m.mu.Unlock()
	if err != nil {
		if summary := m.recentFailureSummary(); summary != "" {
			return fmt.Errorf("%w: %s", err, summary)
		}
		return err
	}
	return nil
}

func (m *Manager) collectPipe(level string, reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 128*1024), 4*1024*1024)
	scanner.Split(splitByCRLF)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		m.addLog(classifyFFmpegLogLevel(level, line), line)
		maybeHintHEVCSource(m, line)
	}
	if err := scanner.Err(); err != nil {
		m.addLog("Error", "ffmpeg log scanner error: "+err.Error())
	}
}

func maybeHintHEVCSource(manager *Manager, line string) {
	lower := strings.ToLower(strings.TrimSpace(line))
	if !strings.Contains(lower, "video: hevc") {
		return
	}
	manager.mu.Lock()
	already := manager.hevcHintShown
	if !already {
		manager.hevcHintShown = true
	}
	manager.mu.Unlock()
	if already {
		return
	}
	manager.addLog("Warn", "detected HEVC input stream; for best RTSP stability set camera encode to H264 and keyframe interval to 1s")
}

func splitByCRLF(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if len(data) == 0 {
		if atEOF {
			return 0, nil, nil
		}
		return 0, nil, nil
	}
	if idx := bytes.IndexAny(data, "\r\n"); idx >= 0 {
		return idx + 1, bytes.TrimSpace(data[:idx]), nil
	}
	if atEOF {
		return len(data), bytes.TrimSpace(data), nil
	}
	return 0, nil, nil
}

func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	cancel := m.cancel
	cmd := m.cmd
	m.cancel = nil
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	// Wait until runLoop goroutine actually exits.
	deadline := time.After(5 * time.Second)
	for {
		m.mu.RLock()
		still := m.running
		m.mu.RUnlock()
		if !still {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return nil
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (m *Manager) Restart(ctx context.Context) error {
	if err := m.Stop(ctx); err != nil {
		return err
	}
	return m.Start(ctx, false)
}

func (m *Manager) Status() store.PushStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

func (m *Manager) Logs() []store.FFmpegLogItem {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]store.FFmpegLogItem, len(m.logs))
	// Return in reverse order (newest first) since we append to end.
	for i, j := 0, len(m.logs)-1; j >= 0; i, j = i+1, j-1 {
		result[i] = m.logs[j]
	}
	return result
}

func (m *Manager) setStatus(status store.PushStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = status
}

func (m *Manager) addLog(logType string, message string) {
	if strings.TrimSpace(message) == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := store.FFmpegLogItem{LogType: logType, Time: time.Now(), Message: message}
	m.logs = append(m.logs, entry)
	if len(m.logs) > m.logBuffer {
		m.logs = m.logs[len(m.logs)-m.logBuffer:]
	}
	if m.debugLogs {
		log.Printf("[ffmpeg][%s] %s", strings.ToLower(strings.TrimSpace(logType)), message)
	}
}

func joinArgs(args []string) string {
	if len(args) == 0 {
		return ""
	}
	result := make([]string, 0, len(args))
	for _, arg := range args {
		if strings.ContainsAny(arg, " \t") {
			result = append(result, fmt.Sprintf("%q", arg))
		} else {
			result = append(result, arg)
		}
	}
	return strings.Join(result, " ")
}

func (m *Manager) recentFailureSummary() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.logs) == 0 {
		return ""
	}
	keywords := []string{
		"error", "failed", "fail", "timeout", "timed out",
		"invalid", "unauthorized", "forbidden", "refused",
		"broken pipe", "could not", "not found",
		"av_interleaved_write_frame", "server returned",
		"connection", "i/o error", "end of file", "unsupported",
		"unrecognized option", "option not found", "no such file",
	}
	lines := make([]string, 0, 4)
	for index := len(m.logs) - 1; index >= 0 && len(lines) < 4; index-- {
		item := m.logs[index]
		msg := strings.TrimSpace(item.Message)
		if msg == "" {
			continue
		}
		lower := strings.ToLower(msg)
		if isFFmpegBannerLine(lower) {
			continue
		}
		if looksLikeFFmpegCommandLine(lower) {
			continue
		}
		if strings.Contains(lower, "could not find ref with poc") {
			// This HEVC warning can be transient (usually first GOP) and doesn't necessarily break streaming.
			continue
		}
		matched := false
		for _, keyword := range keywords {
			if strings.Contains(lower, keyword) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if len(msg) > 260 {
			msg = msg[:260] + "..."
		}
		lines = append([]string{msg}, lines...)
	}
	return strings.Join(lines, " | ")
}

func isFFmpegBannerLine(lowerText string) bool {
	lowerText = strings.TrimSpace(lowerText)
	if lowerText == "" {
		return true
	}
	if strings.HasPrefix(lowerText, "ffmpeg version ") {
		return true
	}
	if strings.HasPrefix(lowerText, "built with ") {
		return true
	}
	if strings.HasPrefix(lowerText, "configuration: ") {
		return true
	}
	if strings.HasPrefix(lowerText, "libavutil") ||
		strings.HasPrefix(lowerText, "libavcodec") ||
		strings.HasPrefix(lowerText, "libavformat") ||
		strings.HasPrefix(lowerText, "libavdevice") ||
		strings.HasPrefix(lowerText, "libavfilter") ||
		strings.HasPrefix(lowerText, "libswscale") ||
		strings.HasPrefix(lowerText, "libswresample") ||
		strings.HasPrefix(lowerText, "libpostproc") {
		return true
	}
	return false
}

func looksLikeFFmpegCommandLine(lowerText string) bool {
	lowerText = strings.TrimSpace(lowerText)
	if strings.HasPrefix(lowerText, "exit status ") {
		return true
	}
	if strings.Contains(lowerText, ".exe -") || strings.Contains(lowerText, "ffmpeg -") {
		return true
	}
	return false
}

func classifyFFmpegLogLevel(defaultLevel string, message string) string {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return defaultLevel
	}
	if strings.EqualFold(defaultLevel, "Info") {
		return "Info"
	}

	// Stderr includes both progress/info and actual errors.
	infoPrefixes := []string{
		"input #", "output #", "metadata:", "duration:", "stream #",
		"stream mapping:", "press [q] to stop", "side data:",
	}
	for _, prefix := range infoPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return "Info"
		}
	}
	if strings.HasPrefix(lower, "frame=") {
		return "Info"
	}
	if isFFmpegBannerLine(lower) {
		return "Info"
	}

	// Common noisy HEVC decode warning; keep visible but not as hard error.
	if strings.Contains(lower, "could not find ref with poc") {
		return "Warn"
	}

	if strings.Contains(lower, "warning") ||
		strings.Contains(lower, "deprecated") ||
		strings.Contains(lower, "non-monotonous") ||
		strings.Contains(lower, "past duration too large") {
		return "Warn"
	}

	errorKeywords := []string{
		"error", "failed", "invalid", "not found",
		"permission denied", "connection refused", "no such file",
		"option ", "unable to", "broken pipe", "i/o error",
	}
	for _, keyword := range errorKeywords {
		if strings.Contains(lower, keyword) {
			return "Error"
		}
	}
	return defaultLevel
}
