// Package ipfs implements a minimal Kubo (go-ipfs) HTTP API client for
// pinning public asset metadata. Only raw-block
// add + pin + get are needed: the platform always derives the CID from
// already-canonicalized bytes itself (internal/auditpkg), so it uploads
// pre-hashed raw blocks rather than asking Kubo to chunk/DAG arbitrary files.
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

// Client pins and retrieves raw IPFS blocks.
type Client interface {
	// AddRaw uploads data as a raw (codec 0x55) block and returns its CID.
	// The caller (internal/auditpkg) already knows the expected CID from
	// the canonical bytes' digest; callers SHOULD verify the returned CID
	// matches before trusting the pin.
	AddRaw(ctx context.Context, data []byte) (cid string, err error)
	// Pin explicitly pins an existing CID (idempotent).
	Pin(ctx context.Context, cid string) error
	// Get retrieves a block's raw bytes by CID.
	Get(ctx context.Context, cid string) ([]byte, error)
}

// Default transport/header/overall timeouts and response-size limits. Without
// them the client would use http.Client{} with no timeout at all, so a wedged
// or maliciously configured endpoint could hang an API publication call or the
// 30-minute verification loop indefinitely, and read every response body fully
// unbounded.
const (
	DefaultDialTimeout           = 5 * time.Second
	DefaultTLSHandshakeTimeout   = 5 * time.Second
	DefaultResponseHeaderTimeout = 10 * time.Second
	// DefaultOverallTimeout bounds one ENTIRE request (connect through
	// reading the full response body — Go's http.Client.Timeout covers
	// the whole round trip, not just headers), generous enough for a
	// large raw-block upload/download on a slow local Kubo node.
	DefaultOverallTimeout = 60 * time.Second
	// DefaultMaxResponseBytes bounds every response body this client will
	// read, success or error — matches auditpkg's own package-size class
	// of bound (MaxExpandedPackageSize); nothing this platform ever
	// legitimately publishes/retrieves is larger.
	DefaultMaxResponseBytes = 64 << 20
)

// KuboClient talks to a local/remote Kubo HTTP API (default :5001).
type KuboClient struct {
	baseURL          string
	http             *http.Client
	maxResponseBytes int64
}

// NewKuboClient constructs a client against the Kubo HTTP API at baseURL
// (e.g. "http://127.0.0.1:5001") using the default timeouts/limits. Use
// NewKuboClientWithLimits to override them.
func NewKuboClient(baseURL string) *KuboClient {
	return NewKuboClientWithLimits(baseURL, DefaultOverallTimeout, DefaultMaxResponseBytes)
}

// NewKuboClientWithLimits is NewKuboClient with an explicit overall
// per-request timeout and response-size cap.
func NewKuboClientWithLimits(baseURL string, overallTimeout time.Duration, maxResponseBytes int64) *KuboClient {
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: DefaultDialTimeout}).DialContext,
		TLSHandshakeTimeout:   DefaultTLSHandshakeTimeout,
		ResponseHeaderTimeout: DefaultResponseHeaderTimeout,
	}
	return &KuboClient{baseURL: baseURL, http: &http.Client{Timeout: overallTimeout, Transport: transport}, maxResponseBytes: maxResponseBytes}
}

// readLimited reads at most k.maxResponseBytes+1 bytes from r (streaming
// through a bounded reader rather than buffering an arbitrary amount
// before checking), returning an error if the response was larger than
// the configured limit, so content above the documented limit is rejected
// rather than read in full.
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

func (k *KuboClient) AddRaw(ctx context.Context, data []byte) (string, error) {
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

	url := k.baseURL + "/api/v0/add?cid-version=1&raw-leaves=true&pin=true"
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
