package ipfs

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	gocid "github.com/ipfs/go-cid"
	"github.com/multiformats/go-multihash"
)

// ComputeCIDv1Raw derives CIDv1/raw/sha2-256 from bytes so ReplicationManager can
// verify retrieved content independently of a destination's report
func ComputeCIDv1Raw(data []byte) (string, error) {
	mh, err := multihash.Sum(data, multihash.SHA2_256, -1)
	if err != nil {
		return "", err
	}
	return gocid.NewCidV1(gocid.Raw, mh).String(), nil
}

// FileArchiveClient stores plain files named by URL-escaped CIDs in a directory, not a
// CAR archive. Copy the directory for backup; RestoreLocal re-adds file bytes through
// AddRaw after repository loss. Tests need no Kubo node.
type FileArchiveClient struct {
	dir string
}

// NewFileArchiveClient returns a FileArchiveClient rooted at dir, creating the
// directory (mode 0700) if needed
func NewFileArchiveClient(dir string) (*FileArchiveClient, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("ipfs: create archive directory %s: %w", dir, err)
	}
	return &FileArchiveClient{dir: dir}, nil
}

func (c *FileArchiveClient) blockPath(cid string) string {
	// escaping keeps a cid containing a path separator from leaving dir
	return filepath.Join(c.dir, url.PathEscape(cid))
}

// AddRaw writes data to a file named by its CIDv1Raw and returns the CID
func (c *FileArchiveClient) AddRaw(ctx context.Context, data []byte) (string, error) {
	cid, err := ComputeCIDv1Raw(data)
	if err != nil {
		return "", err
	}
	if err := c.writeBlockDurably(cid, data); err != nil {
		return "", err
	}
	return cid, nil
}

// writeBlockDurably writes and fsyncs a same-directory temp file, atomically renames
// it, fsyncs the directory, then verifies read-back content against its CID. A
// pre-rename crash may leave a temp file but no truncated CID file.
func (c *FileArchiveClient) writeBlockDurably(cid string, data []byte) error {
	final := c.blockPath(cid)
	tmp, err := os.CreateTemp(c.dir, ".tmp-"+url.PathEscape(cid)+"-*")
	if err != nil {
		return fmt.Errorf("ipfs: archive create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	// Clean up failed temp files; after rename the temp path no longer exists
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("ipfs: archive write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("ipfs: archive fsync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("ipfs: archive close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, final); err != nil {
		return fmt.Errorf("ipfs: archive rename into place: %w", err)
	}
	if err := syncDir(c.dir); err != nil {
		return fmt.Errorf("ipfs: archive fsync directory: %w", err)
	}

	// read the block back and confirm its digest
	written, err := os.ReadFile(final)
	if err != nil {
		return fmt.Errorf("ipfs: archive verify read-back: %w", err)
	}
	gotCID, err := ComputeCIDv1Raw(written)
	if err != nil {
		return err
	}
	if gotCID != cid {
		return fmt.Errorf("ipfs: archive verify: wrote %s but read back content hashing to %s", cid, gotCID)
	}
	return nil
}

// syncDir fsyncs the directory to persist a POSIX rename across crashes
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Pin checks that the archive holds cid
func (c *FileArchiveClient) Pin(ctx context.Context, cid string) error {
	// a stored block is already durable here, so Pin only confirms it exists
	if _, err := os.Stat(c.blockPath(cid)); err != nil {
		return fmt.Errorf("ipfs: archive has no block for CID %s: %w", cid, err)
	}
	return nil
}

// Get reads the file stored for cid
func (c *FileArchiveClient) Get(ctx context.Context, cid string) ([]byte, error) {
	data, err := os.ReadFile(c.blockPath(cid))
	if err != nil {
		return nil, fmt.Errorf("ipfs: archive read %s: %w", cid, err)
	}
	return data, nil
}

var _ Client = (*FileArchiveClient)(nil)
