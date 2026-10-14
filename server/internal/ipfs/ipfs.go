// Package ipfs pins and retrieves public asset metadata. It provides a minimal
// client for the Kubo HTTP RPC API (add, pin and cat), a local-directory
// archive and an in-memory fake that implement the same Client interface, and a
// ReplicationManager that publishes content to a local node and backup
// destinations and tracks and verifies replication.
//
// The platform derives each CID itself from canonical bytes (internal/auditpkg).
// Content is addressed as CIDv1 with the raw codec and a sha2-256 multihash, and
// every verification re-hashes retrieved bytes to that CID.
package ipfs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"time"
)

// Client stores, pins and retrieves content addressed by CIDv1 (raw codec,
// sha2-256)
type Client interface {
	// AddRaw stores data, pins it and returns its CID. Callers should compare
	// the returned CID with the one they computed from data.
	AddRaw(ctx context.Context, data []byte) (cid string, err error)
	// Pin pins cid. FileArchiveClient and FakeClient fail when they do not hold it
	Pin(ctx context.Context, cid string) error
	// Get returns the content stored under cid
	Get(ctx context.Context, cid string) ([]byte, error)
}

// Default timeouts and response-size limit for KuboClient. They stop an
// unresponsive endpoint from hanging a publication call or the periodic
// verification loop, and bound how much of a response is read.
const (
	DefaultDialTimeout           = 5 * time.Second
	DefaultTLSHandshakeTimeout   = 5 * time.Second
	DefaultResponseHeaderTimeout = 10 * time.Second
	// DefaultOverallTimeout bounds a whole request, from connecting through
	// reading the response body (http.Client.Timeout)
	DefaultOverallTimeout = 60 * time.Second
	// DefaultMaxResponseBytes bounds every response body the client reads, 64 MiB
	// like auditpkg.MaxExpandedPackageSize
	DefaultMaxResponseBytes = 64 << 20
)

// KuboClient is a Client backed by a Kubo node's HTTP RPC API. Kubo serves the
// RPC API on port 5001 by default.
type KuboClient struct {
	baseURL          string
	http             *http.Client
	maxResponseBytes int64
}

// NewKuboClient returns a KuboClient for the RPC API at baseURL (for example
// "http://127.0.0.1:5001") with the default timeout and response limit
func NewKuboClient(baseURL string) *KuboClient {
	return NewKuboClientWithLimits(baseURL, DefaultOverallTimeout, DefaultMaxResponseBytes)
}

// NewKuboClientWithLimits is NewKuboClient with an explicit per-request timeout
// and response-size cap
func NewKuboClientWithLimits(baseURL string, overallTimeout time.Duration, maxResponseBytes int64) *KuboClient {
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: DefaultDialTimeout}).DialContext,
		TLSHandshakeTimeout:   DefaultTLSHandshakeTimeout,
		ResponseHeaderTimeout: DefaultResponseHeaderTimeout,
	}
	return &KuboClient{baseURL: baseURL, http: &http.Client{Timeout: overallTimeout, Transport: transport}, maxResponseBytes: maxResponseBytes}
}

// readLimited reads r through a bounded reader and returns an error when the
// body is longer than k.maxResponseBytes, so an oversized body is never read in
// full
func (k *KuboClient) readLimited(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, k.maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > k.maxResponseBytes {
		return nil, fmt.Errorf("ipfs: response exceeds the configured %d byte limit", k.maxResponseBytes)
	}
	return b, nil
}

// maxSingleBlockBytes is the largest content AddRaw stores as one raw leaf. It is
// the chunk size AddRaw asks Kubo for, 1 MiB, which every Kubo version accepts and
// which covers auditpkg.MaxDocumentBytes.
const maxSingleBlockBytes = 1 << 20

// AddRaw adds data with POST /api/v0/add using cid-version=1, raw-leaves=true,
// pin=true and chunker=size-1048576, so data up to 1 MiB comes back as a single
// raw leaf whose CID equals the CIDv1Raw of data. Kubo's default chunker
// (size-262144) would split larger content into a multi-block DAG with a different
// root CID, so AddRaw returns an error for data over 1 MiB.
func (k *KuboClient) AddRaw(ctx context.Context, data []byte) (string, error) {
	if len(data) > maxSingleBlockBytes {
		return "", fmt.Errorf("ipfs: %d bytes exceeds the %d byte single-block limit", len(data), maxSingleBlockBytes)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "block")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	url := k.baseURL + "/api/v0/add?cid-version=1&raw-leaves=true&pin=true&chunker=size-1048576"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := k.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("ipfs: add request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := k.readLimited(resp.Body)
	if err != nil {
		return "", fmt.Errorf("ipfs: read add response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ipfs: add failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var out struct {
		Hash string `json:"Hash"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", fmt.Errorf("ipfs: decode add response: %w", err)
	}
	return out.Hash, nil
}

// Pin pins cid with POST /api/v0/pin/add
func (k *KuboClient) Pin(ctx context.Context, cid string) error {
	url := k.baseURL + "/api/v0/pin/add?arg=" + cid
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	resp, err := k.http.Do(req)
	if err != nil {
		return fmt.Errorf("ipfs: pin request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := k.readLimited(resp.Body)
		return fmt.Errorf("ipfs: pin failed (%d): %s", resp.StatusCode, string(b))
	}
	return nil
}

// Get reads cid with POST /api/v0/cat
func (k *KuboClient) Get(ctx context.Context, cid string) ([]byte, error) {
	url := k.baseURL + "/api/v0/cat?arg=" + cid
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := k.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ipfs: get request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := k.readLimited(resp.Body)
		return nil, fmt.Errorf("ipfs: get failed (%d): %s", resp.StatusCode, string(b))
	}
	b, err := k.readLimited(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ipfs: read get response: %w", err)
	}
	return b, nil
}

var _ Client = (*KuboClient)(nil)
