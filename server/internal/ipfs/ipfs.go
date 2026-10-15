// Package ipfs stores and retrieves public metadata through Kubo HTTP RPC,
// local-directory archives and memory fakes. ReplicationManager publishes to
// local/backup clients and tracks verified copies.
// Canonical metadata uses auditpkg-derived CIDv1/raw/sha2-256 content addresses;
// verification re-hashes retrieved bytes against the expected CID.
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
	// AddRaw stores/pins bytes and returns a CID; callers should compare it with their
	// independently computed CID
	AddRaw(ctx context.Context, data []byte) (cid string, err error)
	// Pin pins cid. FileArchiveClient and FakeClient fail when they do not hold it
	Pin(ctx context.Context, cid string) error
	// Get returns the content stored under cid
	Get(ctx context.Context, cid string) ([]byte, error)
}

// Default Kubo timeouts bound stalled publication/verification requests, and response
// limits cap bytes read
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

// KuboClient implements Client through Kubo's HTTP RPC API, normally served on port
// 5001
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

// readLimited reads at most maxResponseBytes+1 and rejects oversized bodies
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

// maxSingleBlockBytes is AddRaw's 1 MiB raw-leaf limit and requested chunk size,
// matching auditpkg.MaxDocumentBytes
const maxSingleBlockBytes = 1 << 20

// AddRaw posts /api/v0/add with cid-version=1, raw-leaves=true, pin=true and
// chunker=size-1048576. It rejects data over 1 MiB to keep one raw leaf matching
// CIDv1Raw, rather than a multi-block DAG produced by the default 256 KiB chunker.
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
