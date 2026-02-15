package logging

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"bilibililivetools/gover/backend/config"
)

type Manager struct {
	mu       sync.Mutex
	file     *os.File
	filePath string
	enabled  bool
}

func New(cfg config.Config) (*Manager, error) {
	manager := &Manager{}
	if err := manager.Update(cfg); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *Manager) Update(cfg config.Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	wantEnabled := cfg.EnableDebugLogs || cfg.DebugMode
	targetPath := ""
	if wantEnabled {
		dataDir := strings.TrimSpace(cfg.DataDir)
		if dataDir == "" {
			dataDir = "data"
		}
		logDir := filepath.Join(dataDir, "log")
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			return err
		}
		targetPath = filepath.Join(logDir, "gover-"+time.Now().Format("20060102")+".log")
	}

	if !wantEnabled {
		if m.file != nil {
			_ = m.file.Close()
			m.file = nil
		}
		m.filePath = ""
		m.enabled = false
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
		log.SetOutput(os.Stdout)
		return nil
	}

	if m.file != nil && m.filePath == targetPath {
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
		log.SetOutput(io.MultiWriter(os.Stdout, m.file))
		m.enabled = true
		return nil
	}

	if m.file != nil {
		_ = m.file.Close()
		m.file = nil
	}
	file, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
		log.SetOutput(os.Stdout)
		m.enabled = false
		m.filePath = ""
		return err
	}
	m.file = file
	m.filePath = targetPath
	m.enabled = true
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(io.MultiWriter(os.Stdout, file))
	log.Printf("[logger] debug file logging enabled: %s", targetPath)
	return nil
}

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(os.Stdout)
	if m.file == nil {
		return nil
	}
	err := m.file.Close()
	m.file = nil
	m.filePath = ""
	m.enabled = false
	return err
}
