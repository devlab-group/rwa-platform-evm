package shadow

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// ResourceSnapshot samples heap, GC, goroutines and open file descriptors for soak leak
// checks. It excludes database/RPC connections, queues, event lag and DLQ growth.
// Report provides final event/DLQ counts; internal/metrics exposes live gauges such as
// RedemptionsPendingCount and StorageUp.
type ResourceSnapshot struct {
	Timestamp      time.Time `json:"timestamp"`
	HeapAllocBytes uint64    `json:"heapAllocBytes"`
	HeapObjects    uint64    `json:"heapObjects"`
	NumGC          uint32    `json:"numGC"`
	NumGoroutine   int       `json:"numGoroutine"`
	// OpenFDs counts /proc/self/fd entries; 0 means unknown when unavailable or
	// unreadable, including on non-Linux systems
	OpenFDs int `json:"openFDs"`
}

// Sample takes a current ResourceSnapshot
func Sample() ResourceSnapshot {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return ResourceSnapshot{
		Timestamp:      time.Now().UTC(),
		HeapAllocBytes: m.HeapAlloc,
		HeapObjects:    m.HeapObjects,
		NumGC:          m.NumGC,
		NumGoroutine:   runtime.NumGoroutine(),
		OpenFDs:        countOpenFDs(),
	}
}

// countOpenFDs counts /proc/self/fd entries, returning 0 (unknown) on read failure
func countOpenFDs() int {
	entries, err := os.ReadDir(filepath.Join("/proc", "self", "fd"))
	if err != nil {
		return 0
	}
	return len(entries)
}

// GrowthCheck compares first/last samples, reporting growth above tolerancePercent. Use
// several windows to distinguish sustained memory/goroutine growth from startup growth
// (docs/operator/soak-runbook.md); one comparison cannot.
//
// When first is 0, percent is 100 and grew is last > 0.
func GrowthCheck(first, last uint64, tolerancePercent float64) (grew bool, percent float64) {
	if first == 0 {
		return last > 0, 100
	}
	percent = (float64(last) - float64(first)) / float64(first) * 100
	return percent > tolerancePercent, percent
}

// FormatBytes formats b in human-readable binary units for reports and logs
func FormatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return strconv.FormatUint(b, 10) + "B"
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return strconv.FormatFloat(float64(b)/float64(div), 'f', 1, 64) + " " + string("KMGTPE"[exp]) + "iB"
}
