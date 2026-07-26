// Package rwapkg (directory internal/package) safely opens a .rwa audit
// package: a ZIP container holding manifest.json, profile.json,
// metadata.json, typed-data.json, and optional proofs/.
//
// Extraction is entirely in-memory (nothing is ever written to disk) and
// enforces: no path traversal, no symlinks, no duplicate entry names, only
// Store/Deflate compression, and hard caps on file count and expanded size
// -- the last of which is enforced against the actual bytes decompressed,
// not the (attacker-controlled) size declared in the ZIP header, which is
// what actually stops a zip-bomb rather than merely a lying header.
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

// MaxCompressedSize bounds the compressed (on-disk) size of a .rwa package
// accepted by ReadPackageFile. It is checked against the file's Stat size,
// and enforced again with a bounded reader while reading, before any bytes
// are allocated for the whole file: os.ReadFile'ing an arbitrarily large
// compressed file, ahead of the uncompressed-size limits Extract applies,
// can exhaust memory on its own.
const MaxCompressedSize = 64 * 1024 * 1024 // 64 MiB

// ReadPackageFile reads the .rwa package at path into memory, rejecting it
// before allocation if its compressed size exceeds maxCompressed.
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

	// Bounded read, not a bare io.ReadAll(f): a file can grow between Stat
	// and read (TOCTOU), so the cap is re-enforced against bytes actually
	// read, same principle Extract already applies to decompressed bytes.
	data, err := io.ReadAll(io.LimitReader(f, maxCompressed+1))
	if err != nil {
		return nil, fmt.Errorf("rwapkg: reading %q: %w", path, err)
	}
	if int64(len(data)) > maxCompressed {
		return nil, fmt.Errorf("rwapkg: %q exceeds the maximum compressed package size of %d bytes", path, maxCompressed)
	}
	return data, nil
}

// Options bounds the shape of an acceptable .rwa archive.
type Options struct {
	MaxFileCount              int   // maximum number of entries in the archive
	MaxSingleFileUncompressed int64 // maximum decompressed size of any one entry
	MaxTotalUncompressed      int64 // maximum sum of decompressed sizes across all entries
}

// DefaultOptions returns conservative limits suitable for audit packages,
// which are expected to contain a handful of small JSON documents plus a
// few proof PDFs/images.
func DefaultOptions() Options {
	return Options{
		MaxFileCount:              256,
		MaxSingleFileUncompressed: 64 * 1024 * 1024,  // 64 MiB
		MaxTotalUncompressed:      256 * 1024 * 1024, // 256 MiB
	}
}

// unixSymlinkMode is S_IFLNK (0120000) as stored in the upper 16 bits of a
// ZIP entry's external file attributes on Unix-created archives.
const unixSymlinkMode = 0120000

// Extract opens zipBytes and returns a map of cleaned, relative entry name
// to its decompressed content. It rejects:
//   - more entries than opts.MaxFileCount;
//   - absolute paths, backslashes, empty names, or any ".." path segment
//     (path traversal);
//   - symlink entries;
//   - duplicate (post-cleaning) entry names;
//   - compression methods other than Store and Deflate;
//   - any entry, or the archive as a whole, exceeding the configured
//     uncompressed-size limits -- checked against bytes actually read, so a
//     header that under-reports size cannot bypass the limit.
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
			// Directory entry: skip, nothing to extract.
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
// (shared/schemas/rwa-manifest.schema.json).
type ManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	MIME   string `json:"mime"`
}

// Manifest is the parsed manifest.json, matching
// shared/schemas/rwa-manifest.schema.json.
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

// ParseManifest validates raw manifest.json bytes against
// shared/schemas/rwa-manifest.schema.json.
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
	// Enforce the shape of the manifest.files set as a whole, not just each
	// entry's own field validity: reject duplicate paths, require the three
	// mandatory JSON documents to be listed, and confine every other entry
	// to proofs/. Without this, and given a VerifyFiles (below) that only
	// checks manifest entries exist among the extracted files but not the
	// reverse, an attacker could smuggle extra, unaccounted-for payloads
	// into an otherwise-valid-looking package.
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

// VerifyFiles checks that files (the package's extracted entries, minus
// manifest.json) is EXACTLY the set of paths declared in the manifest --
// every declared path present with the exact declared size and SHA-256
// hash, and no extracted file left undeclared -- the package's real
// contents must be exactly what the manifest describes, not merely a
// superset of it.
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
