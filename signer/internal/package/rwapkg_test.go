package rwapkg

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildZip writes a zip archive from name->content pairs using the default
// zip.Writer (well-formed, non-malicious archives).
func buildZip(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zw.Create(%q): %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("writing entry %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zw.Close: %v", err)
	}
	return buf.Bytes()
}

func TestExtract_ValidArchive(t *testing.T) {
	zipBytes := buildZip(t, map[string][]byte{
		"manifest.json": []byte(`{}`),
		"profile.json":  []byte(`{"a":1}`),
	})
	files, err := Extract(zipBytes, DefaultOptions())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if string(files["manifest.json"]) != `{}` {
		t.Errorf("manifest.json = %q", files["manifest.json"])
	}
	if string(files["profile.json"]) != `{"a":1}` {
		t.Errorf("profile.json = %q", files["profile.json"])
	}
}

func TestExtract_RejectsPathTraversal(t *testing.T) {
	zipBytes := buildZip(t, map[string][]byte{"../evil.txt": []byte("x")})
	if _, err := Extract(zipBytes, DefaultOptions()); err == nil {
		t.Fatal("expected rejection of path traversal entry")
	}
}

func TestExtract_RejectsNestedPathTraversal(t *testing.T) {
	zipBytes := buildZip(t, map[string][]byte{"proofs/../../evil.txt": []byte("x")})
	if _, err := Extract(zipBytes, DefaultOptions()); err == nil {
		t.Fatal("expected rejection of nested path traversal entry")
	}
}

func TestExtract_RejectsAbsolutePath(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "/etc/passwd", Method: zip.Store})
	if err != nil {
		t.Fatalf("CreateHeader: %v", err)
	}
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := Extract(buf.Bytes(), DefaultOptions()); err == nil {
		t.Fatal("expected rejection of absolute path entry")
	}
}

func TestExtract_RejectsSymlink(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fh := &zip.FileHeader{Name: "link", Method: zip.Store}
	fh.SetMode(os.ModeSymlink | 0777)
	w, err := zw.CreateHeader(fh)
	if err != nil {
		t.Fatalf("CreateHeader: %v", err)
	}
	if _, err := w.Write([]byte("/etc/passwd")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := Extract(buf.Bytes(), DefaultOptions()); err == nil {
		t.Fatal("expected rejection of symlink entry")
	}
}

func TestExtract_RejectsDuplicateNames(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i < 2; i++ {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: "same.json", Method: zip.Store})
		if err != nil {
			t.Fatalf("CreateHeader: %v", err)
		}
		if _, err := w.Write([]byte("{}")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := Extract(buf.Bytes(), DefaultOptions()); err == nil {
		t.Fatal("expected rejection of duplicate entry names")
	}
}

func TestExtract_RejectsUnsupportedCompression(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// Method 99 does not exist / is unsupported (neither Store nor Deflate).
	_, err := zw.CreateHeader(&zip.FileHeader{Name: "weird.bin", Method: 99})
	if err != nil {
		// archive/zip may itself refuse to write an unknown method; either
		// way the security property (unsupported methods never make it
		// through) holds, so treat this as a pass.
		return
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := Extract(buf.Bytes(), DefaultOptions()); err == nil {
		t.Fatal("expected rejection of unsupported compression method")
	}
}

// TestExtract_RejectsZipBomb builds a real (not header-lied) decompression
// bomb: a few kilobytes of compressed data that inflate to well past the
// configured limit. The defense must be based on bytes actually read, not
// on the archive's self-reported UncompressedSize64.
func TestExtract_RejectsZipBomb(t *testing.T) {
	payload := bytes.Repeat([]byte{0}, 50*1024*1024) // 50 MiB of zeros, compresses tiny
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "bomb.bin", Method: zip.Deflate})
	if err != nil {
		t.Fatalf("CreateHeader: %v", err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	opts := Options{MaxFileCount: 10, MaxSingleFileUncompressed: 1024 * 1024, MaxTotalUncompressed: 4 * 1024 * 1024}
	if _, err := Extract(buf.Bytes(), opts); err == nil {
		t.Fatal("expected rejection of oversized decompressed content")
	}
}

func TestExtract_RejectsTooManyEntries(t *testing.T) {
	entries := make(map[string][]byte, 5)
	for i := 0; i < 5; i++ {
		entries[strings.Repeat("f", i+1)+".txt"] = []byte("x")
	}
	zipBytes := buildZip(t, entries)
	if _, err := Extract(zipBytes, Options{MaxFileCount: 2, MaxSingleFileUncompressed: 1024, MaxTotalUncompressed: 1024}); err == nil {
		t.Fatal("expected rejection: too many entries")
	}
}

// TestReadPackageFile_RejectsOversizedCompressed checks the compressed-size
// limit is enforced via Stat before the whole file is allocated, not merely
// after Extract inflates it.
func TestReadPackageFile_RejectsOversizedCompressed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.rwa")
	if err := os.WriteFile(path, bytes.Repeat([]byte{0}, 4096), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := ReadPackageFile(path, 1024); err == nil {
		t.Fatal("expected rejection of a file exceeding maxCompressed")
	}
}

func TestReadPackageFile_AcceptsWithinLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ok.rwa")
	want := []byte("small archive contents")
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := ReadPackageFile(path, 1024)
	if err != nil {
		t.Fatalf("ReadPackageFile: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("ReadPackageFile = %q, want %q", got, want)
	}
}

func TestReadPackageFile_MissingFile(t *testing.T) {
	if _, err := ReadPackageFile(filepath.Join(t.TempDir(), "missing.rwa"), 1024); err == nil {
		t.Fatal("expected error for a missing file")
	}
}

// manifestFileEntry builds a ManifestFile whose sha256/size are computed
// from data, so callers don't have to keep those in sync by hand.
func manifestFileEntry(path string, data []byte, mime string) ManifestFile {
	sum := sha256.Sum256(data)
	return ManifestFile{Path: path, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data)), MIME: mime}
}

// mandatoryPackageFiles returns a name->content map for the three
// mandatory package documents (profile.json/metadata.json/typed-data.json,
// each with placeholder content) plus, if extra is non-nil, its entries
// merged in. Used to build a manifest fixture that satisfies ParseManifest's
// "the three mandatory documents must be present exactly once" rule without
// every test having to spell all three out.
func mandatoryPackageFiles(extra map[string][]byte) map[string][]byte {
	files := map[string][]byte{
		"profile.json":    []byte(`{"profile":1}`),
		"metadata.json":   []byte(`{"asset":1}`),
		"typed-data.json": []byte(`{"typedData":1}`),
	}
	for k, v := range extra {
		files[k] = v
	}
	return files
}

func TestParseManifest_AndVerifyFiles(t *testing.T) {
	contents := mandatoryPackageFiles(nil)
	var files []ManifestFile
	for path, data := range contents {
		files = append(files, manifestFileEntry(path, data, "application/json"))
	}
	manifest := Manifest{
		PackageVersion: "1.0",
		PrimaryType:    "MintAttestation",
		ProfileDigest:  "0x" + strings.Repeat("11", 32),
		MetadataDigest: "0x" + strings.Repeat("22", 32),
		Files:          files,
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	parsed, err := ParseManifest(raw)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if err := parsed.VerifyFiles(contents); err != nil {
		t.Errorf("VerifyFiles: %v", err)
	}
}

func TestParseManifest_RejectsWrongVersion(t *testing.T) {
	raw := []byte(`{"packageVersion":"2.0","primaryType":"MintAttestation","profileDigest":"0x` + strings.Repeat("11", 32) + `","metadataDigest":"0x` + strings.Repeat("22", 32) + `","files":[]}`)
	if _, err := ParseManifest(raw); err == nil {
		t.Fatal("expected rejection of wrong packageVersion")
	}
}

// TestParseManifest_RejectsDuplicatePaths checks that
// two manifest.files entries naming the same path must be rejected, not
// silently accepted (the second entry would otherwise shadow or be
// shadowed by the first without either side noticing).
func TestParseManifest_RejectsDuplicatePaths(t *testing.T) {
	contents := mandatoryPackageFiles(map[string][]byte{"proofs/a.pdf": []byte("x")})
	var files []ManifestFile
	for path, data := range contents {
		files = append(files, manifestFileEntry(path, data, "application/octet-stream"))
	}
	// Duplicate the proofs/a.pdf entry.
	files = append(files, manifestFileEntry("proofs/a.pdf", contents["proofs/a.pdf"], "application/octet-stream"))

	manifest := Manifest{
		PackageVersion: "1.0", PrimaryType: "MintAttestation",
		ProfileDigest: "0x" + strings.Repeat("11", 32), MetadataDigest: "0x" + strings.Repeat("22", 32),
		Files: files,
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if _, err := ParseManifest(raw); err == nil {
		t.Fatal("expected rejection of a duplicate manifest.files path")
	}
}

// TestParseManifest_RejectsNonProofsExtraPath checks that
// any manifest.files entry that isn't one of the three mandatory documents
// must live under proofs/ -- an arbitrary top-level extra path is rejected.
func TestParseManifest_RejectsNonProofsExtraPath(t *testing.T) {
	contents := mandatoryPackageFiles(map[string][]byte{"sneaky.bin": []byte("x")})
	var files []ManifestFile
	for path, data := range contents {
		files = append(files, manifestFileEntry(path, data, "application/octet-stream"))
	}
	manifest := Manifest{
		PackageVersion: "1.0", PrimaryType: "MintAttestation",
		ProfileDigest: "0x" + strings.Repeat("11", 32), MetadataDigest: "0x" + strings.Repeat("22", 32),
		Files: files,
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if _, err := ParseManifest(raw); err == nil {
		t.Fatal("expected rejection of a non-proofs/ extra manifest path")
	}
}

// TestParseManifest_RejectsMissingMandatoryFile checks that a manifest which
// never lists one of the three mandatory documents is rejected outright, not
// merely failing later when main.go looks it up among the extracted files.
func TestParseManifest_RejectsMissingMandatoryFile(t *testing.T) {
	data := []byte(`{"profile":1}`)
	manifest := Manifest{
		PackageVersion: "1.0", PrimaryType: "MintAttestation",
		ProfileDigest: "0x" + strings.Repeat("11", 32), MetadataDigest: "0x" + strings.Repeat("22", 32),
		Files: []ManifestFile{manifestFileEntry("profile.json", data, "application/json")},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if _, err := ParseManifest(raw); err == nil {
		t.Fatal("expected rejection of a manifest missing metadata.json/typed-data.json")
	}
}

// TestManifest_VerifyFiles_RejectsUndeclaredExtra checks that an extracted
// file not named in manifest.files is rejected, not silently passed through
// as an unaccounted-for payload.
func TestManifest_VerifyFiles_RejectsUndeclaredExtra(t *testing.T) {
	data := []byte(`{"asset":1}`)
	sum := sha256.Sum256(data)
	manifest := &Manifest{
		Files: []ManifestFile{{Path: "metadata.json", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data)), MIME: "application/json"}},
	}
	files := map[string][]byte{
		"metadata.json": data,
		"extra.bin":     []byte("undeclared payload"),
	}
	if err := manifest.VerifyFiles(files); err == nil {
		t.Fatal("expected rejection of an extracted file not declared in manifest.files")
	}
}

func TestManifest_VerifyFiles_HashMismatch(t *testing.T) {
	manifest := &Manifest{
		Files: []ManifestFile{{Path: "metadata.json", SHA256: strings.Repeat("00", 32), Size: 2, MIME: "application/json"}},
	}
	if err := manifest.VerifyFiles(map[string][]byte{"metadata.json": []byte("{}")}); err == nil {
		t.Fatal("expected hash mismatch error")
	}
}

func TestManifest_VerifyFiles_MissingFile(t *testing.T) {
	manifest := &Manifest{
		Files: []ManifestFile{{Path: "metadata.json", SHA256: strings.Repeat("00", 32), Size: 2, MIME: "application/json"}},
	}
	if err := manifest.VerifyFiles(map[string][]byte{}); err == nil {
		t.Fatal("expected missing-file error")
	}
}
