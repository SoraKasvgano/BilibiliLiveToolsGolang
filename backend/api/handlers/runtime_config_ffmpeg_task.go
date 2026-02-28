package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"bilibililivetools/gover/backend/config"
	"bilibililivetools/gover/backend/httpapi"
	ffsvc "bilibililivetools/gover/backend/service/ffmpeg"
)

const (
	ffmpegInstallTaskPending   = "pending"
	ffmpegInstallTaskRunning   = "running"
	ffmpegInstallTaskSucceeded = "succeeded"
	ffmpegInstallTaskFailed    = "failed"
	ffmpegInstallTaskCancelled = "cancelled"
)

var defaultFFmpegInstallTaskManager = newFFmpegInstallTaskManager(48)

type ffmpegInstallTask struct {
	ID          string               `json:"taskId"`
	Status      string               `json:"status"`
	Stage       string               `json:"stage"`
	Percent     int                  `json:"percent"`
	Message     string               `json:"message"`
	Error       string               `json:"error,omitempty"`
	CDNPrefix   string               `json:"cdnPrefix,omitempty"`
	CreatedAt   time.Time            `json:"createdAt"`
	UpdatedAt   time.Time            `json:"updatedAt"`
	StartedAt   *time.Time           `json:"startedAt,omitempty"`
	FinishedAt  *time.Time           `json:"finishedAt,omitempty"`
	Progress    []ffsvc.InstallStep  `json:"progress"`
	Install     *ffsvc.InstallResult `json:"install,omitempty"`
	FFmpegPath  string               `json:"ffmpegPath,omitempty"`
	FFprobePath string               `json:"ffprobePath,omitempty"`

	Version int64 `json:"-"`
}

type ffmpegInstallTaskManager struct {
	mu       sync.RWMutex
	tasks    map[string]*ffmpegInstallTask
	cancels  map[string]context.CancelFunc
	maxTasks int
}

func newFFmpegInstallTaskManager(maxTasks int) *ffmpegInstallTaskManager {
	if maxTasks <= 0 {
		maxTasks = 32
	}
	return &ffmpegInstallTaskManager{
		tasks:    make(map[string]*ffmpegInstallTask, maxTasks),
		cancels:  make(map[string]context.CancelFunc, maxTasks),
		maxTasks: maxTasks,
	}
}

func (m *ffmpegInstallTaskManager) Create(cdnPrefix string) (*ffmpegInstallTask, error) {
	taskID, err := newFFmpegInstallTaskID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	step := ffsvc.InstallStep{Stage: "detect", Progress: 3, Message: "Task created"}
	task := &ffmpegInstallTask{
		ID:        taskID,
		Status:    ffmpegInstallTaskPending,
		Stage:     step.Stage,
		Percent:   step.Progress,
		Message:   step.Message,
		CDNPrefix: strings.TrimSpace(cdnPrefix),
		CreatedAt: now,
		UpdatedAt: now,
		Progress:  []ffsvc.InstallStep{step},
		Version:   1,
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasks[task.ID] = task
	m.pruneLocked()
	return cloneFFmpegInstallTask(task), nil
}

func (m *ffmpegInstallTaskManager) SetRunning(taskID string, step ffsvc.InstallStep) {
	m.applyStep(taskID, ffmpegInstallTaskRunning, step, true)
}

func (m *ffmpegInstallTaskManager) Update(taskID string, step ffsvc.InstallStep) {
	m.applyStep(taskID, "", step, false)
}

func (m *ffmpegInstallTaskManager) AppendStep(taskID string, step ffsvc.InstallStep) {
	m.applyStep(taskID, "", step, true)
}

func (m *ffmpegInstallTaskManager) SetCancel(taskID string, cancel context.CancelFunc) {
	if cancel == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tasks[taskID]; !ok {
		return
	}
	m.cancels[taskID] = cancel
}

func (m *ffmpegInstallTaskManager) ClearCancel(taskID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cancels, taskID)
}

func (m *ffmpegInstallTaskManager) Cancel(taskID string, message string) (*ffmpegInstallTask, error) {
	id := strings.TrimSpace(taskID)
	if id == "" {
		return nil, fmt.Errorf("missing task id")
	}
	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = "Install cancelled by user"
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[id]
	if !ok {
		return nil, fmt.Errorf("install task not found")
	}
	if isFFmpegInstallTaskDone(task.Status) {
		return cloneFFmpegInstallTask(task), nil
	}
	if cancel, ok := m.cancels[id]; ok && cancel != nil {
		cancel()
		delete(m.cancels, id)
	}
	m.setCancelledLocked(task, msg)
	return cloneFFmpegInstallTask(task), nil
}

func (m *ffmpegInstallTaskManager) SetFailed(taskID string, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[taskID]
	if !ok {
		return
	}
	delete(m.cancels, taskID)
	now := time.Now()
	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = "Install failed"
	}
	task.Status = ffmpegInstallTaskFailed
	task.Error = msg
	task.Stage = "save"
	task.Message = msg
	task.UpdatedAt = now
	task.FinishedAt = &now
	if task.Percent < 1 {
		task.Percent = 1
	}
	m.appendStepLocked(task, ffsvc.InstallStep{Stage: "save", Progress: task.Percent, Message: msg})
	task.Version++
}

func (m *ffmpegInstallTaskManager) SetSucceeded(taskID string, install *ffsvc.InstallResult, ffmpegPath string, ffprobePath string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[taskID]
	if !ok {
		return
	}
	delete(m.cancels, taskID)
	now := time.Now()
	task.Status = ffmpegInstallTaskSucceeded
	task.Stage = "save"
	task.Percent = 100
	task.Message = "Install completed"
	task.Error = ""
	task.UpdatedAt = now
	task.FinishedAt = &now
	task.FFmpegPath = strings.TrimSpace(ffmpegPath)
	task.FFprobePath = strings.TrimSpace(ffprobePath)
	task.Install = cloneInstallResult(install)
	task.Version++
}

func (m *ffmpegInstallTaskManager) SetCancelled(taskID string, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[taskID]
	if !ok {
		return
	}
	delete(m.cancels, taskID)
	m.setCancelledLocked(task, message)
}

func (m *ffmpegInstallTaskManager) setCancelledLocked(task *ffmpegInstallTask, message string) {
	if task == nil {
		return
	}
	if isFFmpegInstallTaskDone(task.Status) {
		return
	}
	now := time.Now()
	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = "Install cancelled by user"
	}
	task.Status = ffmpegInstallTaskCancelled
	task.Error = msg
	task.Stage = "save"
	task.Message = msg
	task.UpdatedAt = now
	task.FinishedAt = &now
	if task.Percent < 1 {
		task.Percent = 1
	}
	m.appendStepLocked(task, ffsvc.InstallStep{Stage: "save", Progress: task.Percent, Message: msg})
	task.Version++
}

func (m *ffmpegInstallTaskManager) Get(taskID string) (*ffmpegInstallTask, bool) {
	task, _, ok := m.GetWithVersion(taskID)
	return task, ok
}

func (m *ffmpegInstallTaskManager) GetWithVersion(taskID string) (*ffmpegInstallTask, int64, bool) {
	id := strings.TrimSpace(taskID)
	if id == "" {
		return nil, 0, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	task, ok := m.tasks[id]
	if !ok {
		return nil, 0, false
	}
	return cloneFFmpegInstallTask(task), task.Version, true
}

func (m *ffmpegInstallTaskManager) applyStep(taskID string, status string, step ffsvc.InstallStep, appendHistory bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[taskID]
	if !ok {
		return
	}
	if isFFmpegInstallTaskDone(task.Status) {
		return
	}
	now := time.Now()
	if task.StartedAt == nil {
		task.StartedAt = &now
	}
	if strings.TrimSpace(status) != "" {
		task.Status = strings.TrimSpace(status)
	}
	if task.Status == ffmpegInstallTaskPending {
		task.Status = ffmpegInstallTaskRunning
	}

	stage := strings.TrimSpace(step.Stage)
	if stage != "" {
		task.Stage = stage
	}
	if step.Progress < 0 {
		step.Progress = 0
	}
	if step.Progress > 100 {
		step.Progress = 100
	}
	if step.Progress > 0 {
		task.Percent = step.Progress
	}
	message := strings.TrimSpace(step.Message)
	if message != "" {
		task.Message = message
	}
	task.UpdatedAt = now
	if appendHistory {
		m.appendStepLocked(task, step)
	}
	task.Version++
}

func (m *ffmpegInstallTaskManager) appendStepLocked(task *ffmpegInstallTask, step ffsvc.InstallStep) {
	if task == nil {
		return
	}
	if !isDuplicateInstallStep(task.Progress, step) {
		task.Progress = append(task.Progress, step)
	}
}

func (m *ffmpegInstallTaskManager) pruneLocked() {
	if len(m.tasks) <= m.maxTasks {
		return
	}
	type item struct {
		id      string
		updated time.Time
		done    bool
	}
	items := make([]item, 0, len(m.tasks))
	for id, task := range m.tasks {
		items = append(items, item{
			id:      id,
			updated: task.UpdatedAt,
			done:    isFFmpegInstallTaskDone(task.Status),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].updated.Before(items[j].updated)
	})
	for _, it := range items {
		if len(m.tasks) <= m.maxTasks {
			return
		}
		if it.done {
			delete(m.tasks, it.id)
			delete(m.cancels, it.id)
		}
	}
	for _, it := range items {
		if len(m.tasks) <= m.maxTasks {
			return
		}
		delete(m.tasks, it.id)
		delete(m.cancels, it.id)
	}
}

func cloneFFmpegInstallTask(task *ffmpegInstallTask) *ffmpegInstallTask {
	if task == nil {
		return nil
	}
	clone := *task
	clone.Progress = append(make([]ffsvc.InstallStep, 0, len(task.Progress)), task.Progress...)
	clone.Install = cloneInstallResult(task.Install)
	return &clone
}

func cloneInstallResult(src *ffsvc.InstallResult) *ffsvc.InstallResult {
	if src == nil {
		return nil
	}
	copyValue := *src
	copyValue.Progress = append(make([]ffsvc.InstallStep, 0, len(src.Progress)), src.Progress...)
	return &copyValue
}

func isDuplicateInstallStep(list []ffsvc.InstallStep, step ffsvc.InstallStep) bool {
	if len(list) == 0 {
		return false
	}
	last := list[len(list)-1]
	return last.Stage == step.Stage && last.Progress == step.Progress && last.Message == step.Message
}

func isFFmpegInstallTaskDone(status string) bool {
	s := strings.TrimSpace(status)
	return s == ffmpegInstallTaskSucceeded || s == ffmpegInstallTaskFailed || s == ffmpegInstallTaskCancelled
}

func newFFmpegInstallTaskID() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func resolveReusableFFmpegPaths(cfg config.Config) (string, string) {
	pathValue := strings.TrimSpace(cfg.FFmpegPath)
	probeValue := strings.TrimSpace(cfg.FFprobePath)
	if isExistingFile(pathValue) {
		return pathValue, probeValue
	}
	autoFFmpeg, autoFFprobe := config.AutoDetectFFmpegAndFFprobePaths()
	autoFFmpeg = strings.TrimSpace(autoFFmpeg)
	autoFFprobe = strings.TrimSpace(autoFFprobe)
	if isExistingFile(autoFFmpeg) {
		return autoFFmpeg, autoFFprobe
	}
	return "", ""
}

func isExistingFile(pathValue string) bool {
	pathValue = strings.TrimSpace(pathValue)
	if pathValue == "" {
		return false
	}
	fi, err := os.Stat(pathValue)
	if err != nil {
		return false
	}
	return !fi.IsDir()
}

func cleanupFFmpegDownloadTempFiles(dataDir string) {
	base := strings.TrimSpace(dataDir)
	if base == "" {
		base = "data"
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return
	}
	downloadDir := filepath.Join(absBase, "downloads")
	entries, err := os.ReadDir(downloadDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(entry.Name()))
		if !strings.HasPrefix(name, "ffmpeg-") {
			continue
		}
		if !strings.HasSuffix(name, ".zip") && !strings.HasSuffix(name, ".zip.tmp") {
			continue
		}
		_ = os.Remove(filepath.Join(downloadDir, entry.Name()))
	}
}

func (m *runtimeConfigModule) runFFmpegInstallTask(taskID string, dataDir string, cdnPrefix string) {
	if m.installTasks == nil {
		return
	}
	m.installTasks.SetRunning(taskID, ffsvc.InstallStep{Stage: "detect", Progress: 10, Message: "Resolving install package..."})

	if m.deps.ConfigMgr != nil {
		cfg := m.deps.ConfigMgr.Current()
		existingFFmpegPath, existingFFprobePath := resolveReusableFFmpegPaths(cfg)
		if existingFFmpegPath != "" {
			m.installTasks.AppendStep(taskID, ffsvc.InstallStep{Stage: "detect", Progress: 35, Message: "Local ffmpeg found, skip download"})
			nextCfg := cfg
			nextCfg.FFmpegPath = existingFFmpegPath
			if strings.TrimSpace(existingFFprobePath) != "" {
				nextCfg.FFprobePath = existingFFprobePath
			}
			saved, err := m.deps.ConfigMgr.Save(nextCfg)
			if err != nil {
				m.installTasks.SetFailed(taskID, fmt.Sprintf("save ffmpeg config failed: %v", err))
				return
			}
			if m.deps.FFmpeg != nil {
				m.deps.FFmpeg.UpdatePaths(saved.FFmpegPath, saved.FFprobePath)
			}
			m.installTasks.AppendStep(taskID, ffsvc.InstallStep{Stage: "save", Progress: 100, Message: "Config saved (reuse local ffmpeg)"})
			m.installTasks.SetSucceeded(taskID, &ffsvc.InstallResult{
				FFmpegPath:  saved.FFmpegPath,
				FFprobePath: saved.FFprobePath,
				InstallDir:  filepath.Dir(saved.FFmpegPath),
				Progress: []ffsvc.InstallStep{
					{Stage: "detect", Progress: 35, Message: "Local ffmpeg found, skip download"},
					{Stage: "save", Progress: 100, Message: "Config saved (reuse local ffmpeg)"},
				},
			}, saved.FFmpegPath, saved.FFprobePath)
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	m.installTasks.SetCancel(taskID, cancel)
	defer func() {
		cancel()
		m.installTasks.ClearCancel(taskID)
	}()

	installResult, err := ffsvc.InstallRelease(ctx, ffsvc.InstallOptions{
		DataDir:   dataDir,
		CDNPrefix: cdnPrefix,
		ProgressCallback: func(step ffsvc.InstallStep) {
			m.installTasks.Update(taskID, step)
		},
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			cleanupFFmpegDownloadTempFiles(dataDir)
			m.installTasks.SetCancelled(taskID, "Install cancelled by user")
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			cleanupFFmpegDownloadTempFiles(dataDir)
			m.installTasks.SetFailed(taskID, "Install timed out")
			return
		}
		m.installTasks.SetFailed(taskID, fmt.Sprintf("auto install ffmpeg failed: %v", err))
		return
	}
	for _, step := range installResult.Progress {
		m.installTasks.AppendStep(taskID, step)
	}

	if m.deps.ConfigMgr == nil {
		m.installTasks.SetFailed(taskID, "config manager not available")
		return
	}

	nextCfg := m.deps.ConfigMgr.Current()
	nextCfg.FFmpegPath = installResult.FFmpegPath
	if strings.TrimSpace(installResult.FFprobePath) != "" {
		nextCfg.FFprobePath = installResult.FFprobePath
	}
	saved, err := m.deps.ConfigMgr.Save(nextCfg)
	if err != nil {
		m.installTasks.SetFailed(taskID, fmt.Sprintf("save ffmpeg config failed: %v", err))
		return
	}
	if m.deps.FFmpeg != nil {
		m.deps.FFmpeg.UpdatePaths(saved.FFmpegPath, saved.FFprobePath)
	}

	m.installTasks.AppendStep(taskID, ffsvc.InstallStep{Stage: "save", Progress: 100, Message: "Config saved"})
	m.installTasks.SetSucceeded(taskID, installResult, saved.FFmpegPath, saved.FFprobePath)
}

func (m *runtimeConfigModule) installFFmpegStatus(w http.ResponseWriter, r *http.Request) {
	if m.installTasks == nil {
		httpapi.Error(w, -1, "install task manager not available", http.StatusOK)
		return
	}
	taskID := strings.TrimSpace(r.URL.Query().Get("taskId"))
	if taskID == "" {
		httpapi.Error(w, -1, "missing taskId", http.StatusBadRequest)
		return
	}
	task, ok := m.installTasks.Get(taskID)
	if !ok {
		httpapi.Error(w, -1, "install task not found", http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]any{
		"task":      task,
		"done":      isFFmpegInstallTaskDone(task.Status),
		"succeeded": task.Status == ffmpegInstallTaskSucceeded,
	})
}

func (m *runtimeConfigModule) installFFmpegStream(w http.ResponseWriter, r *http.Request) {
	if m.installTasks == nil {
		httpapi.Error(w, -1, "install task manager not available", http.StatusOK)
		return
	}
	taskID := strings.TrimSpace(r.URL.Query().Get("taskId"))
	if taskID == "" {
		httpapi.Error(w, -1, "missing taskId", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		httpapi.Error(w, -1, "streaming not supported", http.StatusOK)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	lastVersion := int64(-1)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()

	for {
		task, version, exists := m.installTasks.GetWithVersion(taskID)
		if !exists {
			_ = writeInstallSSEEvent(w, "error", map[string]any{"message": "install task not found"})
			flusher.Flush()
			return
		}
		if version != lastVersion {
			payload := map[string]any{
				"task":      task,
				"done":      isFFmpegInstallTaskDone(task.Status),
				"succeeded": task.Status == ffmpegInstallTaskSucceeded,
			}
			if err := writeInstallSSEEvent(w, "task", payload); err != nil {
				return
			}
			flusher.Flush()
			lastVersion = version
			if isFFmpegInstallTaskDone(task.Status) {
				_ = writeInstallSSEEvent(w, "done", payload)
				flusher.Flush()
				return
			}
		}

		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		case <-keepAlive.C:
			if _, err := io.WriteString(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (m *runtimeConfigModule) cancelFFmpegInstall(w http.ResponseWriter, r *http.Request) {
	if m.installTasks == nil {
		httpapi.Error(w, -1, "install task manager not available", http.StatusOK)
		return
	}
	taskID := strings.TrimSpace(r.URL.Query().Get("taskId"))
	if taskID == "" {
		type cancelReq struct {
			TaskID string `json:"taskId"`
		}
		req := cancelReq{}
		if err := httpapi.DecodeJSON(r, &req); err != nil && !errors.Is(err, io.EOF) {
			httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
			return
		}
		taskID = strings.TrimSpace(req.TaskID)
	}
	if taskID == "" {
		httpapi.Error(w, -1, "missing taskId", http.StatusBadRequest)
		return
	}
	task, err := m.installTasks.Cancel(taskID, "Install cancelled by user")
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]any{
		"task":      task,
		"done":      isFFmpegInstallTaskDone(task.Status),
		"succeeded": task.Status == ffmpegInstallTaskSucceeded,
	})
}

func writeInstallSSEEvent(w io.Writer, eventName string, payload any) error {
	if strings.TrimSpace(eventName) == "" {
		eventName = "message"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", eventName); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", body); err != nil {
		return err
	}
	return nil
}
