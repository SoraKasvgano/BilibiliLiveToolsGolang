package handlers

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strings"

	"bilibililivetools/gover/backend/router"
	"bilibililivetools/gover/backend/service/stream"
)

func previewOptionsFromRequest(r *http.Request) stream.PreviewOptions {
	fps := parseIntOrDefault(r.URL.Query().Get("fps"), 8)
	if fps > 20 {
		fps = 20
	}
	width := parseIntOrDefault(r.URL.Query().Get("width"), 960)
	if width > 1920 {
		width = 1920
	}
	return stream.PreviewOptions{
		FPS:   fps,
		Width: width,
	}
}

func previewDebugEnabled(deps *router.Dependencies) bool {
	if deps == nil {
		return false
	}
	if deps.ConfigMgr != nil {
		cfg := deps.ConfigMgr.Current()
		return cfg.EnableDebugLogs || cfg.DebugMode
	}
	return deps.Config.EnableDebugLogs || deps.Config.DebugMode
}

func streamPreviewCommand(w http.ResponseWriter, r *http.Request, command string, args []string, debug bool) error {
	if strings.TrimSpace(command) == "" {
		return errors.New("ffmpeg command is empty")
	}
	cmd := exec.CommandContext(r.Context(), command, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if debug {
		log.Printf("[preview] %s %s", command, strings.Join(args, " "))
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 0, 32*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || !debug {
				continue
			}
			log.Printf("[preview][ffmpeg] %s", line)
		}
		if scanErr := scanner.Err(); scanErr != nil && debug {
			log.Printf("[preview][ffmpeg] stderr scanner error: %v", scanErr)
		}
	}()

	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=ffmpeg")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	_, copyErr := io.Copy(w, stdout)
	waitErr := cmd.Wait()
	<-done

	if copyErr != nil && r.Context().Err() == nil && !errors.Is(copyErr, context.Canceled) {
		if debug {
			log.Printf("[preview] stream copy ended with error: %v", copyErr)
		}
		return copyErr
	}
	if waitErr != nil && r.Context().Err() == nil {
		if debug {
			log.Printf("[preview] ffmpeg exited with error: %v", waitErr)
		}
		return waitErr
	}
	return nil
}
