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

// ComputeCIDv1Raw returns the CIDv1 (raw codec, sha2-256) string for data.
// ReplicationManager uses it to re-derive the CID from retrieved bytes instead
// of trusting a destination's report of what it stored.
func ComputeCIDv1Raw(data []byte) (string, error) {
	mh, err := multihash.Sum(data, multihash.SHA2_256, -1)
	if err != nil {
		return "", err
	}
	return gocid.NewCidV1(gocid.Raw, mh).String(), nil
}

// FileArchiveClient is a Client that stores each block as one file named by its
// URL-escaped CID in a local directory. It writes plain files, not a CAR
// archive. Restoring a lost IPFS repository means re-adding each file's bytes
// through AddRaw (see ReplicationManager.RestoreLocal), and the directory can be
// copied like any other storage. Because it needs no Kubo node, integration
// tests use it to simulate losing the local repository and restoring it.
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

// writeBlockDurably writes data so a crash cannot leave a truncated block at the
// CID path. It writes a temp file in the same directory, fsyncs it, renames it
// to the CID path (atomic on one filesystem), fsyncs the directory so the rename
// is durable, then reads the file back and checks its digest. A crash before
// the rename leaves only a stray temp file.
func (c *FileArchiveClient) writeBlockDurably(cid string, data []byte) error {
	final := c.blockPath(cid)
	tmp, err := os.CreateTemp(c.dir, ".tmp-"+url.PathEscape(cid)+"-*")
	if err != nil {
		return fmt.Errorf("ipfs: archive create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	// removes the temp file on a failure path; after the rename it is a no-op
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

// syncDir fsyncs a directory, which a rename needs to survive a crash on POSIX
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
