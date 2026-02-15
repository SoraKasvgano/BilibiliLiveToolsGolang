package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"bilibililivetools/gover/backend/httpapi"
	"bilibililivetools/gover/backend/router"
	"bilibililivetools/gover/backend/store"
)

type materialModule struct {
	deps *router.Dependencies
}

func init() {
	router.Register(func(deps *router.Dependencies) router.Module {
		return &materialModule{deps: deps}
	})
}

func (m *materialModule) Prefix() string {
	return m.deps.Config.APIBase + "/materials"
}

func (m *materialModule) Routes() []router.Route {
	return []router.Route{
		{Method: http.MethodGet, Pattern: "", Summary: "List materials", Handler: m.list},
		{Method: http.MethodPost, Pattern: "/upload", Summary: "Upload materials", Handler: m.upload},
		{Method: http.MethodPost, Pattern: "/delete", Summary: "Delete materials", Handler: m.delete},
		{Method: http.MethodGet, Pattern: "/{id}/download", Summary: "Download material by id", Handler: m.download},
	}
}

func (m *materialModule) list(w http.ResponseWriter, r *http.Request) {
	fileType := store.FileTypeUnknown
	if raw := r.URL.Query().Get("fileType"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			fileType = store.FileType(parsed)
		}
	}
	page := parseIntOrDefault(r.URL.Query().Get("page"), 1)
	limit := parseIntOrDefault(r.URL.Query().Get("limit"), 10)
	result, err := m.deps.Store.ListMaterials(r.Context(), store.MaterialListPageRequest{
		FileName: r.URL.Query().Get("fileName"),
		FileType: fileType,
		Page:     page,
		Limit:    limit,
		Field:    r.URL.Query().Get("field"),
		Order:    r.URL.Query().Get("order"),
	}, m.deps.Config.MediaDir)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, result)
}

func (m *materialModule) upload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(512 << 20); err != nil {
		httpapi.Error(w, -1, "parse upload form failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		// fallback for clients that send any file key
		for _, f := range r.MultipartForm.File {
			files = append(files, f...)
		}
	}
	if len(files) == 0 {
		httpapi.Error(w, -1, "upload file is empty", http.StatusOK)
		return
	}

	for _, header := range files {
		if err := m.saveSingleUpload(r, header); err != nil {
			httpapi.Error(w, -1, err.Error(), http.StatusOK)
			return
		}
	}
	httpapi.OKMessage(w, "Success")
}

func (m *materialModule) saveSingleUpload(r *http.Request, header *multipart.FileHeader) error {
	if header == nil || strings.TrimSpace(header.Filename) == "" {
		return errors.New("empty filename")
	}
	source, err := header.Open()
	if err != nil {
		return err
	}
	defer source.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	fileType := detectFileType(ext)
	if fileType == store.FileTypeUnknown {
		return errors.New("unsupported file type: " + ext)
	}

	relDir := time.Now().Format("20060102")
	newName := randomName() + ext
	relPath := filepath.ToSlash(filepath.Join(relDir, newName))
	absPath := filepath.Join(m.deps.Config.MediaDir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return err
	}
	target, err := os.Create(absPath)
	if err != nil {
		return err
	}
	defer target.Close()

	size, err := io.Copy(target, source)
	if err != nil {
		return err
	}

	mediaInfo := ""
	if ext != ".txt" {
		if info, infoErr := m.deps.FFmpeg.AnalyzeMedia(r.Context(), absPath); infoErr == nil {
			mediaInfo = info
		}
	}
	return m.deps.Store.CreateMaterial(r.Context(), store.Material{
		Name:        header.Filename,
		Path:        relPath,
		SizeKB:      size / 1024,
		FileType:    fileType,
		Description: "",
		MediaInfo:   mediaInfo,
	})
}

func (m *materialModule) delete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	items, err := m.deps.Store.DeleteMaterials(r.Context(), req.IDs)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	for _, item := range items {
		filePath := filepath.Join(m.deps.Config.MediaDir, filepath.FromSlash(item.Path))
		_ = os.Remove(filePath)
	}
	httpapi.OKMessage(w, "Success")
}

func (m *materialModule) download(w http.ResponseWriter, r *http.Request) {
	idRaw := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idRaw, 10, 64)
	if err != nil || id <= 0 {
		httpapi.Error(w, -1, "invalid material id", http.StatusBadRequest)
		return
	}
	item, err := m.deps.Store.GetMaterialByID(r.Context(), id)
	if err != nil {
		httpapi.Error(w, -1, "material not found", http.StatusNotFound)
		return
	}
	fullPath := filepath.Join(m.deps.Config.MediaDir, filepath.FromSlash(item.Path))
	if _, err := os.Stat(fullPath); err != nil {
		httpapi.Error(w, -1, "file not found", http.StatusNotFound)
		return
	}
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(item.Name)))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+urlEncode(item.Name))
	http.ServeFile(w, r, fullPath)
}

func parseIntOrDefault(raw string, fallback int) int {
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func detectFileType(ext string) store.FileType {
	switch ext {
	case ".wmv", ".asf", ".asx", ".rm", ".rmvb", ".mp4", ".mov", ".m4v", ".avi", ".dat", ".mkv", ".flv", ".txt", ".ts", ".m3u8":
		return store.FileTypeVideo
	case ".wav", ".flac", ".ape", ".alac", ".mp3", ".aac", ".ogg":
		return store.FileTypeMusic
	default:
		return store.FileTypeUnknown
	}
}

func randomName() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buf)
}

func urlEncode(value string) string {
	return url.QueryEscape(value)
}
