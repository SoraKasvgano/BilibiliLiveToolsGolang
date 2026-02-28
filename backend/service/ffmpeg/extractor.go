package ffmpeg

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type ExtractResult struct {
	TargetDir   string `json:"targetDir"`
	FFmpegPath  string `json:"ffmpegPath"`
	FFprobePath string `json:"ffprobePath,omitempty"`
}

func ExtractFFmpegArchive(archivePath string, targetDir string) (*ExtractResult, error) {
	archive := strings.TrimSpace(archivePath)
	if archive == "" {
		return nil, fmt.Errorf("empty archive path")
	}
	baseDir := strings.TrimSpace(targetDir)
	if baseDir == "" {
		return nil, fmt.Errorf("empty target dir")
	}
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, err
	}

	r, err := zip.OpenReader(archive)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	result := &ExtractResult{TargetDir: baseDir}
	for _, file := range r.File {
		if file.FileInfo().IsDir() {
			continue
		}
		base := filepath.Base(file.Name)
		lower := strings.ToLower(base)
		if lower != "ffmpeg" && lower != "ffmpeg.exe" && lower != "ffprobe" && lower != "ffprobe.exe" {
			continue
		}
		outputPath := filepath.Join(baseDir, base)
		if err := extractZipFile(file, outputPath); err != nil {
			return nil, err
		}
		if runtime.GOOS != "windows" {
			_ = os.Chmod(outputPath, 0o755)
		}
		if strings.HasPrefix(lower, "ffmpeg") {
			result.FFmpegPath = outputPath
		} else if strings.HasPrefix(lower, "ffprobe") {
			result.FFprobePath = outputPath
		}
	}

	if strings.TrimSpace(result.FFmpegPath) == "" {
		return nil, fmt.Errorf("ffmpeg binary not found in archive")
	}
	return result, nil
}

func extractZipFile(file *zip.File, targetPath string) error {
	source, err := file.Open()
	if err != nil {
		return err
	}
	defer source.Close()

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}

	tmpPath := targetPath + ".tmp"
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}

	_, copyErr := io.Copy(out, source)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return closeErr
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}
