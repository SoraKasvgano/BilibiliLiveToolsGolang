package config

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"
)

type ChangeListener func(Config)

type Manager struct {
	path string

	mu         sync.RWMutex
	cfg        Config
	modTime    time.Time
	size       int64
	listeners  []ChangeListener
	watchStop  chan struct{}
	watchDone  chan struct{}
	watchEvery time.Duration
}

func NewManager() (*Manager, error) {
	path, err := resolveConfigFilePath()
	if err != nil {
		return nil, err
	}
	cfg, info, err := loadOrCreateConfig(path)
	if err != nil {
		return nil, err
	}
	manager := &Manager{
		path:       path,
		cfg:        cfg,
		modTime:    info.ModTime(),
		size:       info.Size(),
		listeners:  make([]ChangeListener, 0),
		watchEvery: 2 * time.Second,
	}
	return manager, nil
}

func (m *Manager) ConfigPath() string {
	return m.path
}

func (m *Manager) Current() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

func (m *Manager) AddListener(listener ChangeListener) {
	if listener == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listeners = append(m.listeners, listener)
}

func (m *Manager) Save(cfg Config) (Config, error) {
	cfg = normalizeConfig(cfg, m.path)
	cfg.ConfigFile = m.path
	if err := writeConfigFile(m.path, cfg); err != nil {
		return Config{}, err
	}
	info, err := os.Stat(m.path)
	if err != nil {
		return Config{}, err
	}
	m.applyConfig(cfg, info)
	return cfg, nil
}

func (m *Manager) ReloadFromDisk() (Config, error) {
	cfg, info, err := readConfigFile(m.path)
	if err != nil {
		return Config{}, err
	}
	m.applyConfig(cfg, info)
	return cfg, nil
}

func (m *Manager) StartWatching() {
	m.mu.Lock()
	if m.watchStop != nil {
		m.mu.Unlock()
		return
	}
	m.watchStop = make(chan struct{})
	m.watchDone = make(chan struct{})
	stop := m.watchStop
	done := m.watchDone
	interval := m.watchEvery
	m.mu.Unlock()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if err := m.tryReloadOnFileChange(); err != nil {
					log.Printf("[config][warn] hot reload failed: %v", err)
				}
			}
		}
	}()
}

func (m *Manager) StopWatching() {
	m.mu.Lock()
	stop := m.watchStop
	done := m.watchDone
	m.watchStop = nil
	m.watchDone = nil
	m.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	if done != nil {
		<-done
	}
}

func (m *Manager) tryReloadOnFileChange() error {
	info, err := os.Stat(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			_, createInfo, createErr := createDefaultConfigFile(m.path)
			if createErr != nil {
				return createErr
			}
			cfg, readInfo, readErr := readConfigFile(m.path)
			if readErr != nil {
				return readErr
			}
			if readInfo == nil {
				readInfo = createInfo
			}
			m.applyConfig(cfg, readInfo)
			return nil
		}
		return err
	}

	m.mu.RLock()
	modTime := m.modTime
	size := m.size
	m.mu.RUnlock()

	if info.ModTime().Equal(modTime) && info.Size() == size {
		return nil
	}
	cfg, readInfo, err := readConfigFile(m.path)
	if err != nil {
		return err
	}
	m.applyConfig(cfg, readInfo)
	return nil
}

func (m *Manager) applyConfig(cfg Config, info os.FileInfo) {
	cfg = normalizeConfig(cfg, m.path)
	cfg.ConfigFile = m.path

	m.mu.Lock()
	changed := !reflect.DeepEqual(m.cfg, cfg)
	m.cfg = cfg
	if info != nil {
		m.modTime = info.ModTime()
		m.size = info.Size()
	}
	listeners := make([]ChangeListener, len(m.listeners))
	copy(listeners, m.listeners)
	m.mu.Unlock()

	if !changed {
		return
	}
	for _, listener := range listeners {
		func(l ChangeListener) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[config][warn] listener panic: %v", r)
				}
			}()
			l(cfg)
		}(listener)
	}
}

func loadOrCreateConfig(path string) (Config, os.FileInfo, error) {
	cfg, info, err := readConfigFile(path)
	if err == nil {
		return cfg, info, nil
	}
	if !os.IsNotExist(err) {
		return Config{}, nil, err
	}
	cfg, info, err = createDefaultConfigFile(path)
	if err != nil {
		return Config{}, nil, err
	}
	return cfg, info, nil
}

func createDefaultConfigFile(path string) (Config, os.FileInfo, error) {
	cfg := defaultConfig(path)
	if err := writeConfigFile(path, cfg); err != nil {
		return Config{}, nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Config{}, nil, err
	}
	return cfg, info, nil
}

func readConfigFile(path string) (Config, os.FileInfo, error) {
	if path == "" {
		return Config{}, nil, errors.New("empty config path")
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		return Config{}, nil, err
	}
	cfg := defaultConfig(path)
	if len(bytes) > 0 {
		if err := json.Unmarshal(bytes, &cfg); err != nil {
			return Config{}, nil, err
		}
	}
	cfg = normalizeConfig(cfg, path)
	cfg.ConfigFile = path
	info, err := os.Stat(path)
	if err != nil {
		return Config{}, nil, err
	}
	return cfg, info, nil
}

func writeConfigFile(path string, cfg Config) error {
	if path == "" {
		return errors.New("empty config path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	cfg = normalizeConfig(cfg, path)
	cfg.ConfigFile = path
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
}
