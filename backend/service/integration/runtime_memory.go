package integration

import (
	"runtime"
	"runtime/debug"
	"time"
)

func (s *Service) RuntimeMemoryStats() map[string]any {
	stats := runtime.MemStats{}
	runtime.ReadMemStats(&stats)
	return map[string]any{
		"time":              time.Now().UTC().Format(time.RFC3339),
		"allocBytes":        stats.Alloc,
		"totalAllocBytes":   stats.TotalAlloc,
		"sysBytes":          stats.Sys,
		"heapAllocBytes":    stats.HeapAlloc,
		"heapInuseBytes":    stats.HeapInuse,
		"heapIdleBytes":     stats.HeapIdle,
		"heapReleasedBytes": stats.HeapReleased,
		"heapObjects":       stats.HeapObjects,
		"gcCount":           stats.NumGC,
		"lastGCTimeUnixNs":  stats.LastGC,
		"goroutines":        runtime.NumGoroutine(),
	}
}

func (s *Service) ForceMemoryGC() map[string]any {
	runtime.GC()
	debug.FreeOSMemory()
	return s.RuntimeMemoryStats()
}
