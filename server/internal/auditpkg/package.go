package auditpkg

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
)

// Package limits that bound the size of an archive OpenPackage will read
const (
	MaxPackageFiles        = 64
	MaxExpandedPackageSize = 64 << 20 // 64 MiB
	MaxSingleFileSize      = 32 << 20 // 32 MiB
)

// BuildPackage returns a .rwa ZIP and its hex SHA-256 for models.AuditPackage. Contents
// are manifest.json, profile.json, metadata.json, typed-data.json and optional proofs/
// files.
func BuildPackage(primaryType string, profileDigest, metadataDigest [32]byte, profileJSON, metadataJSON []byte, typedData TypedDataDoc, proofs []ProofFile) (zipBytes []byte, sha256Hex string, err error) {
	if primaryType != "MintAttestation" && primaryType != "BurnAttestation" {
		return nil, "", fmt.Errorf("auditpkg: primaryType must be MintAttestation or BurnAttestation, got %q", primaryType)
	}

	typedDataJSON, err := json.Marshal(typedData)
	if err != nil {
		return nil, "", fmt.Errorf("auditpkg: marshal typed-data.json: %w", err)
	}

	files := []struct {
		path string
		data []byte
		mime string
	}{
		{"profile.json", profileJSON, "application/json"},
		{"metadata.json", metadataJSON, "application/json"},
		{"typed-data.json", typedDataJSON, "application/json"},
	}
	for _, p := range proofs {
		files = append(files, struct {
			path string
			data []byte
			mime string
		}{path: "proofs/" + p.Name, data: p.Data, mime: p.MIME})
	}

	manifest := Manifest{
		PackageVersion: "1.0",
		PrimaryType:    primaryType,
		ProfileDigest:  "0x" + hex.EncodeToString(profileDigest[:]),
		MetadataDigest: "0x" + hex.EncodeToString(metadataDigest[:]),
	}
	for _, f := range files {
		sum := sha256.Sum256(f.data)
		manifest.Files = append(manifest.Files, ManifestFile{
			Path:   f.path,
			SHA256: hex.EncodeToString(sum[:]),
			Size:   int64(len(f.data)),
			MIME:   f.mime,
		})
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return nil, "", fmt.Errorf("auditpkg: marshal manifest.json: %w", err)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if err := writeZipFile(zw, "manifest.json", manifestJSON); err != nil {
		return nil, "", err
	}
	for _, f := range files {
		if err := writeZipFile(zw, f.path, f.data); err != nil {
			return nil, "", err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, "", fmt.Errorf("auditpkg: close zip writer: %w", err)
	}

	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:]), nil
}

func writeZipFile(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("auditpkg: create zip entry %s: %w", name, err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("auditpkg: write zip entry %s: %w", name, err)
	}
	return nil
}

// OpenedPackage is a .rwa package that passed OpenPackage's checks
type OpenedPackage struct {
	Manifest Manifest
	Files    map[string][]byte // path -> contents, including manifest.json
}

// OpenPackage reads a .rwa ZIP in memory, rejecting traversal, non-regular entries
// (symlinks, directories, devices), duplicates, excessive entry counts and
// archive/entry size violations. The manifest must exactly match extracted files,
// including sizes and SHA-256 hashes.
func OpenPackage(zipBytes []byte) (*OpenedPackage, error) {
	if len(zipBytes) > MaxExpandedPackageSize {
		return nil, fmt.Errorf("auditpkg: package exceeds max compressed size")
	}
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("auditpkg: invalid zip: %w", err)
	}
	if len(zr.File) > MaxPackageFiles {
		return nil, fmt.Errorf("auditpkg: package has too many entries (%d > %d)", len(zr.File), MaxPackageFiles)
	}

	seen := make(map[string]bool, len(zr.File))
	files := make(map[string][]byte, len(zr.File))
	var totalExpanded int64

	for _, zf := range zr.File {
		name := zf.Name
		if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
			return nil, fmt.Errorf("auditpkg: rejected entry name %q", name)
		}
		clean := path.Clean(name)
		if clean != name || clean == "." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
			return nil, fmt.Errorf("auditpkg: path traversal rejected: %q", name)
		}
		if seen[clean] {
			return nil, fmt.Errorf("auditpkg: duplicate entry: %q", clean)
		}
		seen[clean] = true

		mode := zf.Mode()
		if !mode.IsRegular() {
			return nil, fmt.Errorf("auditpkg: rejected non-regular entry (symlink/dir/device): %q", clean)
		}
		if int64(zf.UncompressedSize64) > MaxSingleFileSize {
			return nil, fmt.Errorf("auditpkg: entry %q exceeds max single-file size", clean)
		}
		totalExpanded += int64(zf.UncompressedSize64)
		if totalExpanded > MaxExpandedPackageSize {
			return nil, fmt.Errorf("auditpkg: package exceeds max expanded size (possible zip bomb)")
		}

		rc, err := zf.Open()
		if err != nil {
			return nil, fmt.Errorf("auditpkg: open entry %q: %w", clean, err)
		}
		data, err := io.ReadAll(io.LimitReader(rc, MaxSingleFileSize+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("auditpkg: read entry %q: %w", clean, err)
		}
		if int64(len(data)) > MaxSingleFileSize {
			return nil, fmt.Errorf("auditpkg: entry %q exceeds max single-file size", clean)
		}
		files[clean] = data
	}

	manifestRaw, ok := files["manifest.json"]
	if !ok {
		return nil, fmt.Errorf("auditpkg: package missing manifest.json")
	}
	manifest, err := parseManifestStrict(manifestRaw)
	if err != nil {
		return nil, err
	}
	declared := make(map[string]bool, len(manifest.Files))
	for _, mf := range manifest.Files {
		declared[mf.Path] = true
		data, ok := files[mf.Path]
		if !ok {
			return nil, fmt.Errorf("auditpkg: manifest references missing file %q", mf.Path)
		}
		if int64(len(data)) != mf.Size {
			return nil, fmt.Errorf("auditpkg: size mismatch for %q: manifest=%d actual=%d", mf.Path, mf.Size, len(data))
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != mf.SHA256 {
			return nil, fmt.Errorf("auditpkg: sha256 mismatch for %q", mf.Path)
		}
	}
	// the loop above catches a declared file missing from the archive; this
	// catches an extracted file the manifest does not declare
	for path := range files {
		if path == "manifest.json" {
			continue
		}
		if !declared[path] {
			return nil, fmt.Errorf("auditpkg: extracted file %q is not declared in manifest.json", path)
		}
	}

	return &OpenedPackage{Manifest: manifest, Files: files}, nil
}
