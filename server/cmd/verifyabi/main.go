// Command verifyabi checks that internal/bindings' hand-written ABI
// literals produce the same function selectors and event topic0 hashes as
// the real compiled contract artifacts under contracts/out (forge build
// output). internal/bindings exists because it was written before the
// contracts compiled; this tool is the CI drift check that verifies the
// stand-in hasn't drifted from the frozen interfaces it was hand-transcribed
// from. It exits 0 (skipping) if contracts/out is absent entirely — e.g.
// before `forge build` has run — so it never requires a Foundry build to exist
// for unrelated `go build`/`go test` runs. Once the directory IS there, though,
// every contract it checks is required: a missing or unreadable artifact is a
// hard failure, not a skip, so a renamed Foundry output can't silently take a
// contract out of the drift gate.
//
// Once internal/bindings is replaced by real abigen output, this command
// (and its whole rationale) can be deleted.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"

	"github.com/rwa-platform/server/internal/bindings"
)

type artifact struct {
	ABI json.RawMessage `json:"abi"`
}

func loadArtifact(path string) (abi.ABI, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return abi.ABI{}, err
	}
	var art artifact
	if err := json.Unmarshal(data, &art); err != nil {
		return abi.ABI{}, err
	}
	return abi.JSON(strings.NewReader(string(art.ABI)))
}

type check struct {
	name     string
	artifact string
	get      func() abi.ABI
}

func main() {
	root := findContractsOut()
	if root == "" {
		fmt.Println("verifyabi: contracts/out not found (run `forge build` first) — skipping drift check")
		return
	}

	checks := []check{
		{"ComplianceRegistry", filepath.Join(root, "ComplianceRegistry.sol", "ComplianceRegistry.json"), func() abi.ABI { return bindings.NewComplianceRegistry().ABI }},
		{"SupplyController", filepath.Join(root, "SupplyController.sol", "SupplyController.json"), func() abi.ABI { return bindings.NewSupplyController().ABI }},
		{"Vault", filepath.Join(root, "Vault.sol", "Vault.json"), func() abi.ABI { return bindings.NewVault().ABI }},
		{"RedemptionEscrow", filepath.Join(root, "RedemptionEscrow.sol", "RedemptionEscrow.json"), func() abi.ABI { return bindings.NewRedemptionEscrow().ABI }},
		{"FixedPriceStrategy", filepath.Join(root, "FixedPriceStrategy.sol", "FixedPriceStrategy.json"), func() abi.ABI { return bindings.NewFixedPriceStrategy().ABI }},
		{"RWAFactory", filepath.Join(root, "RWAFactory.sol", "RWAFactory.json"), func() abi.ABI { return bindings.NewFactory().ABI }},
	}

	// Once contracts/out exists, every contract in `checks` is required: an
	// artifact that cannot be loaded means a renamed/missing Foundry output,
	// and skipping it would leave that contract's drift unchecked while the
	// gate still reported success. Fail closed, and prove afterwards that
	// every expected artifact was actually compared.
	mismatches := 0
	compared := 0
	for _, c := range checks {
		real, err := loadArtifact(c.artifact)
		if err != nil {
			fmt.Printf("  %s: could not load required artifact %s: %v\n", c.name, c.artifact, err)
			mismatches++
			continue
		}
		mismatches += compare(c.name, real, c.get())
		compared++
	}

	if compared != len(checks) {
		fmt.Printf("verifyabi: only %d of %d expected artifacts were compared\n", compared, len(checks))
		os.Exit(1)
	}
	if mismatches > 0 {
		fmt.Printf("verifyabi: %d mismatch(es) found\n", mismatches)
		os.Exit(1)
	}
	fmt.Printf("verifyabi: OK, no drift between internal/bindings and the %d compiled contract ABIs\n", compared)
}

// findContractsOut looks for ../contracts/out or contracts/out relative to
// common working directories, since this tool may be invoked either from
// the repo root or from server/.
func findContractsOut() string {
	for _, candidate := range []string{
		filepath.Join("..", "contracts", "out"),
		filepath.Join("contracts", "out"),
		filepath.Join("..", "..", "contracts", "out"),
	} {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			abs, _ := filepath.Abs(candidate)
			return abs
		}
	}
	return ""
}

func compare(name string, real, mine abi.ABI) int {
	mismatches := 0
	for methodName, m := range mine.Methods {
		rm, ok := real.Methods[methodName]
		if !ok {
			fmt.Printf("  %s: method %s missing from real ABI\n", name, methodName)
			mismatches++
			continue
		}
		if fmt.Sprintf("%x", m.ID) != fmt.Sprintf("%x", rm.ID) {
			fmt.Printf("  %s: method %s selector mismatch: mine=%x real=%x\n", name, methodName, m.ID, rm.ID)
			mismatches++
		}
	}
	for evName, e := range mine.Events {
		re, ok := real.Events[evName]
		if !ok {
			fmt.Printf("  %s: event %s missing from real ABI\n", name, evName)
			mismatches++
			continue
		}
		if e.ID != re.ID {
			fmt.Printf("  %s: event %s topic0 mismatch: mine=%s real=%s\n", name, evName, e.ID.Hex(), re.ID.Hex())
			mismatches++
		}
	}
	return mismatches
}
