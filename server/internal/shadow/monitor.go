package shadow

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// ResourceSnapshot is one point-in-time resource-usage sample: heap usage,
// garbage-collection behavior, goroutine count, and open file descriptors —
// the signals a soak run watches for leaks. DB/RPC connection counts,
// queue depth, event lag, and DLQ growth are deliberately NOT sampled here
// — those are already exposed as Prometheus gauges by internal/metrics
// (RedemptionsPendingCount, StorageUp, ...) and by Report's own
// EventCount/DLQCount, which is where a soak run should actually watch
// them from (its own metrics endpoint + this package's Report), rather than
// this package re-implementing a second metrics system.
type ResourceSnapshot struct {
	Timestamp      time.Time `json:"timestamp"`
	HeapAllocBytes uint64    `json:"heapAllocBytes"`
	HeapObjects    uint64    `json:"heapObjects"`
	NumGC          uint32    `json:"numGC"`
	NumGoroutine   int       `json:"numGoroutine"`
	// OpenFDs is best-effort (Linux /proc/self/fd; 0 elsewhere or on error —
	// not fatal, since it is one signal among several, not the only one).
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

// countOpenFDs best-effort counts this process's open file descriptors via
// /proc/self/fd (Linux only); returns 0 if unavailable (e.g. non-Linux, or
// the directory can't be read), which callers should treat as "unknown,"
// not "zero open descriptors."
func countOpenFDs() int {
	entries, err := os.ReadDir(filepath.Join("/proc", "self", "fd"))
	if err != nil {
		return 0
	}
	return len(entries)
}

// GrowthCheck compares a resource metric's first and last sample in a
// series and reports whether it grew by more than tolerancePercent — a
// mechanical check for the two soak failure modes: memory that keeps
// climbing without stabilizing, and unbounded goroutine growth. A real
// soak run should apply this across many samples spread
// over the full duration, not just first-vs-last, to distinguish "grew
// once during startup then stabilized" from "still climbing at the end" —
// see the runbook (docs/operator/soak-runbook.md) for the recommended
// windowed comparison.
func GrowthCheck(first, last uint64, tolerancePercent float64) (grew bool, percent float64) {
	if first == 0 {
		return last > 0, 100
	}
	percent = (float64(last) - float64(first)) / float64(first) * 100
	return percent > tolerancePercent, percent
}

// FormatBytes is a small human-readable helper for report/log output.
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
