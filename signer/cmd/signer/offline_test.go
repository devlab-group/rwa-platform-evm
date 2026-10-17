package main

import (
	"os/exec"
	"strings"
	"testing"
)

// Exclude known HTTP/TLS/RPC client packages from signer dependencies; plain "net"
// remains allowed for standard-library and go-ethereum address/IP parsing
func TestSignerBinaryLinksNoNetworkClient(t *testing.T) {
	goBin := findGoBinary(t)
	out, err := exec.Command(goBin, "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	deps := strings.Fields(string(out))
	if len(deps) < 50 {
		t.Fatalf("go list -deps returned only %d packages, the listing looks broken:\n%s", len(deps), out)
	}
	for _, dep := range deps {
		switch {
		case dep == "net/http", dep == "crypto/tls", dep == "net/rpc",
			strings.HasSuffix(dep, "go-ethereum/rpc"),
			strings.HasSuffix(dep, "go-ethereum/ethclient"),
			strings.Contains(dep, "go-ethereum/ethclient/"):
			t.Errorf("cmd/signer depends on %s, a network client", dep)
		}
	}
}
