package ffmpeg

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type DownloadResult struct {
	URL        string `json:"url"`
	TargetPath string `json:"targetPath"`
	Size       int64  `json:"size"`
	StatusCode int    `json:"statusCode"`
}

type DownloadProgress struct {
	DownloadedBytes int64 `json:"downloadedBytes"`
	TotalBytes      int64 `json:"totalBytes"`
	Percent         int   `json:"percent"`
}

type Downloader struct {
	client     *http.Client
	onProgress func(DownloadProgress)
}

func NewDownloader() *Downloader {
	return NewDownloaderWithProgress(nil)
}

func NewDownloaderWithProgress(progress func(DownloadProgress)) *Downloader {
	return &Downloader{
		client:     &http.Client{Timeout: 15 * time.Minute},
		onProgress: progress,
	}
}

func (d *Downloader) Download(ctx context.Context, rawURL string, targetPath string) (*DownloadResult, error) {
	urlValue := strings.TrimSpace(rawURL)
	if urlValue == "" {
		return nil, fmt.Errorf("empty download url")
	}
	pathValue := strings.TrimSpace(targetPath)
	if pathValue == "" {
		return nil, fmt.Errorf("empty target path")
	}
	if err := os.MkdirAll(filepath.Dir(pathValue), 0o755); err != nil {
		return nil, err
	}

	tmpPath := pathValue + ".tmp"
	existingSize, err := fileSizeIfExists(tmpPath)
	if err != nil {
		return nil, err
	}

	statusCode := 0
	finalSize := int64(0)
	if existingSize > 0 {
		resumed, code, size, err := d.tryResume(ctx, urlValue, tmpPath, existingSize)
		if err != nil {
			return nil, err
		}
		if resumed {
			statusCode = code
			finalSize = size
		}
	}

	if finalSize == 0 {
		code, size, err := d.downloadFresh(ctx, urlValue, tmpPath)
		if err != nil {
			return nil, err
		}
		statusCode = code
		finalSize = size
	}

	if err := os.Rename(tmpPath, pathValue); err != nil {
		return nil, err
	}

	return &DownloadResult{
		URL:        urlValue,
		TargetPath: pathValue,
		Size:       finalSize,
		StatusCode: statusCode,
	}, nil
}

func (d *Downloader) tryResume(ctx context.Context, urlValue string, tmpPath string, existingSize int64) (bool, int, int64, error) {
	resp, err := d.doRequest(ctx, urlValue, existingSize, true)
	if err != nil {
		return false, 0, 0, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusPartialContent:
		if start, ok := parseContentRangeStart(resp.Header.Get("Content-Range")); ok && start != existingSize {
			return false, 0, 0, nil
		}
		total := resolveTotalBytes(resp, existingSize)
		written, err := writeTmpFile(tmpPath, resp.Body, true, existingSize, total, d.reportProgress)
		if err != nil {
			return true, resp.StatusCode, existingSize + written, err
		}
		return true, resp.StatusCode, existingSize + written, nil
	case http.StatusOK:
		total := resolveTotalBytes(resp, 0)
		written, err := writeTmpFile(tmpPath, resp.Body, false, 0, total, d.reportProgress)
		if err != nil {
			return true, resp.StatusCode, written, err
		}
		return true, resp.StatusCode, written, nil
	case http.StatusRequestedRangeNotSatisfiable:
		if total, ok := parseContentRangeTotal(resp.Header.Get("Content-Range")); ok && total == existingSize {
			d.reportProgress(existingSize, total)
			return true, resp.StatusCode, existingSize, nil
		}
		return false, 0, 0, nil
	default:
		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			total := resolveTotalBytes(resp, 0)
			written, err := writeTmpFile(tmpPath, resp.Body, false, 0, total, d.reportProgress)
			if err != nil {
				return true, resp.StatusCode, written, err
			}
			return true, resp.StatusCode, written, nil
		}
		bodySnippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return false, 0, 0, fmt.Errorf("download failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodySnippet)))
	}
}

func (d *Downloader) downloadFresh(ctx context.Context, urlValue string, tmpPath string) (int, int64, error) {
	resp, err := d.doRequest(ctx, urlValue, 0, false)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		bodySnippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return 0, 0, fmt.Errorf("download failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodySnippet)))
	}

	total := resolveTotalBytes(resp, 0)
	written, err := writeTmpFile(tmpPath, resp.Body, false, 0, total, d.reportProgress)
	if err != nil {
		return resp.StatusCode, written, err
	}
	return resp.StatusCode, written, nil
}

func (d *Downloader) doRequest(ctx context.Context, urlValue string, startAt int64, useRange bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlValue, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "gover-ffmpeg-installer/1.0")
	if useRange && startAt > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", startAt))
	}
	return d.client.Do(req)
}

func (d *Downloader) reportProgress(downloaded int64, total int64) {
	if d.onProgress == nil {
		return
	}
	percent := 0
	if total > 0 {
		percent = int((downloaded * 100) / total)
		if percent > 100 {
			percent = 100
		}
	}
	d.onProgress(DownloadProgress{DownloadedBytes: downloaded, TotalBytes: total, Percent: percent})
}

type progressReporter struct {
	base        int64
	total       int64
	written     int64
	lastPercent int
	lastAt      time.Time
	emit        func(downloaded int64, total int64)
}

func newProgressReporter(base int64, total int64, emit func(downloaded int64, total int64)) *progressReporter {
	return &progressReporter{
		base:        base,
		total:       total,
		lastPercent: -1,
		emit:        emit,
	}
}

func (r *progressReporter) Write(p []byte) (int, error) {
	n := len(p)
	if n <= 0 {
		return n, nil
	}
	r.written += int64(n)
	r.report(false)
	return n, nil
}

func (r *progressReporter) report(force bool) {
	if r.emit == nil {
		return
	}
	downloaded := r.base + r.written
	percent := 0
	if r.total > 0 {
		percent = int((downloaded * 100) / r.total)
		if percent > 100 {
			percent = 100
		}
	}
	now := time.Now()
	if !force {
		if r.total > 0 {
			if percent == r.lastPercent && now.Sub(r.lastAt) < 350*time.Millisecond {
				return
			}
		} else if now.Sub(r.lastAt) < 350*time.Millisecond {
			return
		}
	}
	r.lastPercent = percent
	r.lastAt = now
	r.emit(downloaded, r.total)
}

func writeTmpFile(tmpPath string, body io.Reader, appendMode bool, baseDownloaded int64, totalSize int64, emit func(downloaded int64, total int64)) (int64, error) {
	flags := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(tmpPath, flags, 0o644)
	if err != nil {
		return 0, err
	}

	reporter := newProgressReporter(baseDownloaded, totalSize, emit)
	reporter.report(true)

	written, copyErr := io.Copy(file, io.TeeReader(body, reporter))
	reporter.report(true)
	closeErr := file.Close()
	if copyErr != nil {
		return written, copyErr
	}
	if closeErr != nil {
		return written, closeErr
	}
	return written, nil
}

func resolveTotalBytes(resp *http.Response, base int64) int64 {
	if resp == nil {
		return 0
	}
	if total, ok := parseContentRangeTotal(resp.Header.Get("Content-Range")); ok && total > 0 {
		return total
	}
	if resp.ContentLength < 0 {
		return 0
	}
	if resp.StatusCode == http.StatusPartialContent && base > 0 {
		return base + resp.ContentLength
	}
	return resp.ContentLength
}

func fileSizeIfExists(pathValue string) (int64, error) {
	fi, err := os.Stat(pathValue)
	if err == nil {
		return fi.Size(), nil
	}
	if os.IsNotExist(err) {
		return 0, nil
	}
	return 0, err
}

func parseContentRangeStart(value string) (int64, bool) {
	s := strings.TrimSpace(value)
	if len(s) <= len("bytes ") || !strings.HasPrefix(strings.ToLower(s), "bytes ") {
		return 0, false
	}
	dash := strings.IndexByte(s, '-')
	if dash <= len("bytes ") {
		return 0, false
	}
	startText := strings.TrimSpace(s[len("bytes "):dash])
	start, err := strconv.ParseInt(startText, 10, 64)
	if err != nil || start < 0 {
		return 0, false
	}
	return start, true
}

func parseContentRangeTotal(value string) (int64, bool) {
	s := strings.TrimSpace(value)
	slash := strings.LastIndexByte(s, '/')
	if slash < 0 || slash >= len(s)-1 {
		return 0, false
	}
	totalText := strings.TrimSpace(s[slash+1:])
	if totalText == "*" {
		return 0, false
	}
	total, err := strconv.ParseInt(totalText, 10, 64)
	if err != nil || total < 0 {
		return 0, false
	}
	return total, true
}
