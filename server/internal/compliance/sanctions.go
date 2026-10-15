package compliance

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// SanctionsList loads operator-supplied 0x addresses, one per line, ignoring case, blank lines and
// full-line # comments. A nil list matches nothing.
type SanctionsList struct {
	path string

	mu    sync.RWMutex
	set   map[string]struct{}
	mtime time.Time
}

// NewSanctionsList returns an unloaded list, or nil for an empty path; call Reload to load it
func NewSanctionsList(path string) *SanctionsList {
	if path == "" {
		return nil
	}
	return &SanctionsList{path: path, set: map[string]struct{}{}}
}

// ParseSanctionsList rejects invalid address lines; blank lines and full-line # comments are
// ignored
func ParseSanctionsList(text string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(line), "0x") || !common.IsHexAddress(line) {
			return nil, fmt.Errorf("line %d is not a 0x address", n)
		}
		out[strings.ToLower(line)] = struct{}{}
	}
	return out, sc.Err()
}

// Reload reads a file with a changed modification time and reports whether it replaced the list.
// Read/parse errors keep the old list until another mtime change; stat errors retry on the next
// call.
func (l *SanctionsList) Reload() (bool, error) {
	if l == nil {
		return false, nil
	}
	info, err := os.Stat(l.path)
	if err != nil {
		return false, fmt.Errorf("sanctions list %q: %w", l.path, err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if info.ModTime().Equal(l.mtime) {
		return false, nil
	}
	l.mtime = info.ModTime()
	data, err := os.ReadFile(l.path)
	if err != nil {
		return false, fmt.Errorf("sanctions list %q: %w", l.path, err)
	}
	set, err := ParseSanctionsList(string(data))
	if err != nil {
		return false, fmt.Errorf("sanctions list %q rejected, keeping the list in force: %w", l.path, err)
	}
	l.set = set
	return true, nil
}

// Contains reports whether address is listed, ignoring case
func (l *SanctionsList) Contains(address string) bool {
	if l == nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, ok := l.set[strings.ToLower(address)]
	return ok
}
