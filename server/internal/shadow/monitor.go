package shadow

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// ResourceSnapshot is one point-in-time sample of heap usage, garbage
// collection, goroutine count and open file descriptors, the signals a soak
// run watches for leaks. It does not sample database or RPC connections, queue
// depth, event lag or dead-letter growth. Report carries the event and
// dead-letter counts, and internal/metrics exposes the Prometheus gauges
// (RedemptionsPendingCount, StorageUp and others) for a live run.
type ResourceSnapshot struct {
	Timestamp      time.Time `json:"timestamp"`
	HeapAllocBytes uint64    `json:"heapAllocBytes"`
	HeapObjects    uint64    `json:"heapObjects"`
	NumGC          uint32    `json:"numGC"`
	NumGoroutine   int       `json:"numGoroutine"`
	// OpenFDs is best effort: it counts entries in /proc/self/fd and is 0
	// where that directory is unavailable (non-Linux systems, or a read error)
	OpenFDs int `json:"openFDs"`
}

// Sample takes one ResourceSnapshot right now.
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

// countOpenFDs counts this process's open file descriptors through
// /proc/self/fd. It returns 0 when the directory cannot be read (for example
// on a non-Linux system), which means "unknown", not "no open descriptors".
func countOpenFDs() int {
	entries, err := os.ReadDir(filepath.Join("/proc", "self", "fd"))
	if err != nil {
		return 0
	}
	return len(entries)
}

// GrowthCheck compares a metric's first and last samples and reports whether it
// grew by more than tolerancePercent. It supports the soak checks for memory
// that keeps rising and for goroutine growth. A first-versus-last comparison
// cannot tell growth at startup that then stabilized from growth that continues
// at the end, so apply it to several windows across the run (see
// docs/operator/soak-runbook.md). When first is 0, grew is last > 0 and percent
// is 100.
func GrowthCheck(first, last uint64, tolerancePercent float64) (grew bool, percent float64) {
	if first == 0 {
		return last > 0, 100
	}
	percent = (float64(last) - float64(first)) / float64(first) * 100
	return percent > tolerancePercent, percent
}

// FormatBytes formats b as a human-readable size in binary units for report and log output
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
