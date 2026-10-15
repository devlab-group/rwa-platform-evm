// Package rwapkg reads .rwa ZIPs in memory for manifest, profile, metadata, typed data
// and optional proofs. Extract rejects unsafe names, Unix symlinks, duplicates,
// unsupported compression and exceeded count/byte limits; ParseManifest and VerifyFiles
// enforce payload inventory.
package rwapkg

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
)

// MaxCompressedSize caps ReadPackageFile's compressed input through Stat and a limited
// reader, before Extract enforces expanded-size limits
const MaxCompressedSize = 64 * 1024 * 1024 // 64 MiB

// ReadPackageFile reads at most maxCompressed+1 bytes, rejecting oversized Stat results
// before payload reads and growth beyond maxCompressed afterward
func ReadPackageFile(path string, maxCompressed int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("rwapkg: opening %q: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("rwapkg: stat %q: %w", path, err)
	}
	if info.Size() > maxCompressed {
		return nil, fmt.Errorf("rwapkg: %q is %d bytes, exceeds the maximum compressed package size of %d bytes", path, info.Size(), maxCompressed)
	}

	// Cap reads as well as Stat size, as Extract does for decompression
	data, err := io.ReadAll(io.LimitReader(f, maxCompressed+1))
	if err != nil {
		return nil, fmt.Errorf("rwapkg: reading %q: %w", path, err)
	}
	if int64(len(data)) > maxCompressed {
		return nil, fmt.Errorf("rwapkg: %q exceeds the maximum compressed package size of %d bytes", path, maxCompressed)
	}
	return data, nil
}

// Options bounds the shape of an acceptable .rwa archive
type Options struct {
	MaxFileCount              int   // maximum number of entries in the archive
	MaxSingleFileUncompressed int64 // maximum decompressed size of any one entry
	MaxTotalUncompressed      int64 // maximum sum of decompressed sizes across all entries
}

// DefaultOptions limits entry count, individual expansion and total expansion for JSON
// documents and attached proofs
func DefaultOptions() Options {
	return Options{
		MaxFileCount:              256,
		MaxSingleFileUncompressed: 64 * 1024 * 1024,  // 64 MiB
		MaxTotalUncompressed:      256 * 1024 * 1024, // 256 MiB
	}
}

// unixSymlinkMode is S_IFLNK (0120000) in the upper 16 bits of ZIP external Unix
// attributes
const unixSymlinkMode = 0120000

// Extract returns decompressed entries by name, rejecting:
//
//   - Empty archives or counts above opts.MaxFileCount
//   - Empty/absolute names, backslashes, parent traversal or names changed by path.Clean
//     (including repeated/trailing slashes and dot segments)
//   - Unix symlinks, duplicate names or methods other than Store/Deflate
//   - Entry or total expansion above configured limits, counting bytes read rather than
//     header declarations
func Extract(zipBytes []byte, opts Options) (map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("rwapkg: not a valid zip archive: %w", err)
	}
	if len(zr.File) == 0 {
		return nil, fmt.Errorf("rwapkg: archive contains no entries")
	}
	if len(zr.File) > opts.MaxFileCount {
		return nil, fmt.Errorf("rwapkg: archive has %d entries, exceeds max of %d", len(zr.File), opts.MaxFileCount)
	}

	files := make(map[string][]byte, len(zr.File))
	remaining := opts.MaxTotalUncompressed

	for _, f := range zr.File {
		name := f.Name

		if strings.Contains(name, "\\") {
			return nil, fmt.Errorf("rwapkg: entry %q uses backslash path separators", name)
		}
		if name == "" || strings.HasPrefix(name, "/") {
			return nil, fmt.Errorf("rwapkg: entry has an unsafe absolute or empty name %q", name)
		}
		cleaned := path.Clean(name)
		if cleaned != name || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
			return nil, fmt.Errorf("rwapkg: entry %q contains path traversal", name)
		}
		if strings.HasSuffix(name, "/") {
			// Unreachable while the clean-name check above rejects a trailing slash
			continue
		}

		if f.Mode()&0170000 == 0120000 || (f.ExternalAttrs>>16)&0170000 == unixSymlinkMode {
			return nil, fmt.Errorf("rwapkg: entry %q is a symlink, which is not allowed", name)
		}

		if _, dup := files[cleaned]; dup {
			return nil, fmt.Errorf("rwapkg: duplicate entry name %q", cleaned)
		}

		if f.Method != zip.Store && f.Method != zip.Deflate {
			return nil, fmt.Errorf("rwapkg: entry %q uses unsupported compression method %d", name, f.Method)
		}

		capN := opts.MaxSingleFileUncompressed
		if remaining < capN {
			capN = remaining
		}
		if capN < 0 {
			capN = 0
		}

		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("rwapkg: opening entry %q: %w", name, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(rc, capN+1))
		closeErr := rc.Close()
		if readErr != nil {
			return nil, fmt.Errorf("rwapkg: reading entry %q: %w", name, readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("rwapkg: closing entry %q: %w", name, closeErr)
		}
		if int64(len(data)) > capN {
			return nil, fmt.Errorf("rwapkg: entry %q exceeds the archive size limits (possible zip bomb)", name)
		}

		remaining -= int64(len(data))
		files[cleaned] = data
	}

	return files, nil
}

// ManifestFile is one entry of manifest.json's "files" array
// (shared/schemas/rwa-manifest.schema.json)
type ManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	MIME   string `json:"mime"`
}

// Manifest is the parsed manifest.json, matching
// shared/schemas/rwa-manifest.schema.json
type Manifest struct {
	PackageVersion string         `json:"packageVersion"`
	PrimaryType    string         `json:"primaryType"`
	ProfileDigest  string         `json:"profileDigest"`
	MetadataDigest string         `json:"metadataDigest"`
	Files          []ManifestFile `json:"files"`
}

var pathPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,255}$`)
var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var hexDigestPattern = regexp.MustCompile(`^0x[0-9a-f]{64}$`)

// ParseManifest checks manifest.json format and inventory rules from
// shared/schemas/rwa-manifest.schema.json
func ParseManifest(raw []byte) (*Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("rwapkg: parsing manifest.json: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("rwapkg: trailing data after manifest.json")
	}

	if m.PackageVersion != "1.0" {
		return nil, fmt.Errorf("rwapkg: manifest packageVersion must be \"1.0\"")
	}
	if m.PrimaryType != "MintAttestation" && m.PrimaryType != "BurnAttestation" {
		return nil, fmt.Errorf("rwapkg: manifest primaryType must be MintAttestation or BurnAttestation")
	}
	if !hexDigestPattern.MatchString(m.ProfileDigest) {
		return nil, fmt.Errorf("rwapkg: manifest profileDigest must match ^0x[0-9a-f]{64}$")
	}
	if !hexDigestPattern.MatchString(m.MetadataDigest) {
		return nil, fmt.Errorf("rwapkg: manifest metadataDigest must match ^0x[0-9a-f]{64}$")
	}
	// Require unique paths, profile.json/metadata.json/typed-data.json and proofs/-only
	// extras. VerifyFiles then rejects undeclared extracted payloads.
	const (
		mandatoryProfile   = "profile.json"
		mandatoryMetadata  = "metadata.json"
		mandatoryTypedData = "typed-data.json"
		proofsPrefix       = "proofs/"
	)
	mandatorySeen := map[string]bool{mandatoryProfile: false, mandatoryMetadata: false, mandatoryTypedData: false}
	seenPaths := make(map[string]bool, len(m.Files))
	for i, mf := range m.Files {
		if !pathPattern.MatchString(mf.Path) {
			return nil, fmt.Errorf("rwapkg: manifest.files[%d].path %q is invalid", i, mf.Path)
		}
		if !sha256Pattern.MatchString(mf.SHA256) {
			return nil, fmt.Errorf("rwapkg: manifest.files[%d].sha256 is invalid", i)
		}
		if mf.Size < 0 || mf.Size > 104857600 {
			return nil, fmt.Errorf("rwapkg: manifest.files[%d].size %d is out of bounds", i, mf.Size)
		}
		if mf.MIME == "" {
			return nil, fmt.Errorf("rwapkg: manifest.files[%d].mime must not be empty", i)
		}
		if seenPaths[mf.Path] {
			return nil, fmt.Errorf("rwapkg: manifest.files[%d].path %q is a duplicate entry", i, mf.Path)
		}
		seenPaths[mf.Path] = true

		if _, mandatory := mandatorySeen[mf.Path]; mandatory {
			mandatorySeen[mf.Path] = true
		} else if !strings.HasPrefix(mf.Path, proofsPrefix) {
			return nil, fmt.Errorf("rwapkg: manifest.files[%d].path %q is neither a mandatory package document nor under %q", i, mf.Path, proofsPrefix)
		}
	}
	for path, seen := range mandatorySeen {
		if !seen {
			return nil, fmt.Errorf("rwapkg: manifest.files is missing the mandatory entry %q", path)
		}
	}

	return &m, nil
}

// VerifyFiles requires the extracted payload map (excluding manifest.json) to match
// declared paths, sizes and SHA-256 hashes exactly, without extras
func (m *Manifest) VerifyFiles(files map[string][]byte) error {
	declared := make(map[string]bool, len(m.Files))
	for _, mf := range m.Files {
		declared[mf.Path] = true

		data, ok := files[mf.Path]
		if !ok {
			return fmt.Errorf("rwapkg: manifest references missing file %q", mf.Path)
		}
		if int64(len(data)) != mf.Size {
			return fmt.Errorf("rwapkg: file %q size %d does not match manifest size %d", mf.Path, len(data), mf.Size)
		}
		sum := sha256.Sum256(data)
		got := hex.EncodeToString(sum[:])
		if got != mf.SHA256 {
			return fmt.Errorf("rwapkg: file %q sha256 %s does not match manifest sha256 %s", mf.Path, got, mf.SHA256)
		}
	}
	for path := range files {
		if !declared[path] {
			return fmt.Errorf("rwapkg: extracted file %q is not declared in manifest.files", path)
		}
	}
	return nil
}
