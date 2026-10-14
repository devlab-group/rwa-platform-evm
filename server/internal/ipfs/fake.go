package ipfs

import (
	"context"
	"fmt"
	"sync"

	gocid "github.com/ipfs/go-cid"
	"github.com/multiformats/go-multihash"
)

// FakeClient is an in-memory Client for tests. It keys blocks by their CIDv1
// (raw, sha2-256), the convention auditpkg uses.
type FakeClient struct {
	mu     sync.RWMutex
	blocks map[string][]byte
}

// NewFakeClient returns an empty FakeClient
func NewFakeClient() *FakeClient {
	return &FakeClient{blocks: map[string][]byte{}}
}

// AddRaw stores data under its CIDv1Raw and returns the CID
func (f *FakeClient) AddRaw(ctx context.Context, data []byte) (string, error) {
	mh, err := multihash.Sum(data, multihash.SHA2_256, -1)
	if err != nil {
		return "", err
	}
	c := gocid.NewCidV1(gocid.Raw, mh).String()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blocks[c] = data
	return c, nil
}

// Pin checks that f holds cid
func (f *FakeClient) Pin(ctx context.Context, cid string) error {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if _, ok := f.blocks[cid]; !ok {
		return fmt.Errorf("ipfs: fake client has no block for CID %s", cid)
	}
	return nil
}

// Reset drops every block, simulating a node that lost its repository
func (f *FakeClient) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blocks = map[string][]byte{}
}

// Get returns the block stored under cid
func (f *FakeClient) Get(ctx context.Context, cid string) ([]byte, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	data, ok := f.blocks[cid]
	if !ok {
		return nil, fmt.Errorf("ipfs: fake client has no block for CID %s", cid)
	}
	return data, nil
}

var _ Client = (*FakeClient)(nil)
