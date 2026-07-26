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

// ComputeCIDv1Raw derives the CIDv1(raw,sha2-256) for data — the same
// convention every Client implementation in this package uses (architecture
// §6.3). Exported so callers (ReplicationManager.Verify in particular) can
// independently re-derive the expected CID from freshly retrieved bytes
// instead of trusting a destination's own report of what it stored (audit
// 3.1 §4: "verification SHOULD retrieve the content and confirm that its
// digest matches the expected CID").
func ComputeCIDv1Raw(data []byte) (string, error) {
	mh, err := multihash.Sum(data, multihash.SHA2_256, -1)
	if err != nil {
		return "", err
	}
	return gocid.NewCidV1(gocid.Raw, mh).String(), nil
}

// FileArchiveClient is a local-filesystem, content-addressed backup archive
// implementing Client. It serves as a reproducible content archive without
// producing a byte-for-byte CARv1 container (that would need an external
// codec dependency this platform does not otherwise use); instead each raw
// block is stored as one file named by its CID under dir, which is itself the
// reproducible archive: restoring a lost local IPFS repository from it is
// exactly re-adding every file's bytes back through AddRaw (see
// ReplicationManager.RestoreLocal), and the archive directory can be
// copied/backed up like any other durable storage. This lets integration
// tests simulate loss of the local repository and restoration from the
// retained archive without a live Kubo node or any external commercial
// pinning service.
type FileArchiveClient struct {
	dir string
}

// NewFileArchiveClient returns a FileArchiveClient rooted at dir, creating
// it if necessary.
func NewFileArchiveClient(dir string) (*FileArchiveClient, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("ipfs: create archive directory %s: %w", dir, err)
	}
	return &FileArchiveClient{dir: dir}, nil
}

func (c *FileArchiveClient) blockPath(cid string) string {
	// url.PathEscape defends against a CID string (never attacker-controlled
	// in practice, since it's always OUR OWN digest-derived value, but cheap
	// insurance regardless) being interpreted as a path traversal segment.
	return filepath.Join(c.dir, url.PathEscape(cid))
}

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

// writeBlockDurably performs a crash-safe write: a temp file on the
// SAME directory (so the final rename is atomic and stays on one
// filesystem) -> fsync the temp file's contents -> atomic rename to the
// final CID path -> fsync the containing directory (POSIX does not
// guarantee a rename is durable until the directory entry itself is
// synced) -> read the result back and verify its digest before reporting
// success. A crash at any point before the rename leaves only a stray,
// harmless temp file — never a truncated block at the real CID path,
// which is exactly what the previous direct os.WriteFile could leave.
func (c *FileArchiveClient) writeBlockDurably(cid string, data []byte) error {
	final := c.blockPath(cid)
	tmp, err := os.CreateTemp(c.dir, ".tmp-"+url.PathEscape(cid)+"-*")
	if err != nil {
		return fmt.Errorf("ipfs: archive create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup of a stray temp file on any failure path below;
	// a no-op once the rename below has already moved it away.
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

	// Verify: read the result back and confirm its digest still matches
	// before reporting success.
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

// syncDir fsyncs a directory so a prior file rename within it is durable —
// on POSIX, a rename is not guaranteed to survive a crash until the
// directory entry itself has been synced, not just the file's own
// contents.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (c *FileArchiveClient) Pin(ctx context.Context, cid string) error {
	// Storing a block IS durable in this archive (no separate "add" vs
	// "pin" distinction like Kubo's default-eligible-for-GC-until-pinned
	// model) — Pin only confirms the block is actually present.
	if _, err := os.Stat(c.blockPath(cid)); err != nil {
		return fmt.Errorf("ipfs: archive has no block for CID %s: %w", cid, err)
	}
	return nil
}

func (c *FileArchiveClient) Get(ctx context.Context, cid string) ([]byte, error) {
	data, err := os.ReadFile(c.blockPath(cid))
	if err != nil {
		return nil, fmt.Errorf("ipfs: archive read %s: %w", cid, err)
	}
	return data, nil
}

var _ Client = (*FileArchiveClient)(nil)
