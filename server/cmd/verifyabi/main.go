// Command verifyabi compares internal/bindings selectors and event topic0 hashes with
// contracts/out artifacts, and the rwa-v2 factory binding with the pinned ABI in
// internal/bindings/testdata/rwa-v2. A missing contracts/out prints a notice and exits 0 so Go
// needs no Foundry build. Once present, missing or unreadable checked artifacts fail
// verification.
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
		fmt.Println("verifyabi: contracts/out not found (run `forge build` first) - skipping drift check")
		return
	}

	checks := []check{
		{"ComplianceRegistry", filepath.Join(root, "ComplianceRegistry.sol", "ComplianceRegistry.json"), func() abi.ABI { return bindings.NewComplianceRegistry().ABI }},
		{"SupplyController", filepath.Join(root, "SupplyController.sol", "SupplyController.json"), func() abi.ABI { return bindings.NewSupplyController().ABI }},
		{"Vault", filepath.Join(root, "Vault.sol", "Vault.json"), func() abi.ABI { return bindings.NewVault().ABI }},
		{"RedemptionEscrow", filepath.Join(root, "RedemptionEscrow.sol", "RedemptionEscrow.json"), func() abi.ABI { return bindings.NewRedemptionEscrow().ABI }},
		{"FixedPriceStrategy", filepath.Join(root, "FixedPriceStrategy.sol", "FixedPriceStrategy.json"), func() abi.ABI { return bindings.NewFixedPriceStrategy().ABI }},
		{"IncomeDistributor", filepath.Join(root, "IncomeDistributor.sol", "IncomeDistributor.json"), func() abi.ABI { return bindings.NewIncomeDistributor().ABI }},
		{"OraclePriceStrategy", filepath.Join(root, "OraclePriceStrategy.sol", "OraclePriceStrategy.json"), func() abi.ABI { return bindings.NewOraclePriceStrategy().ABI }},
		{"AggregatorV3Interface", filepath.Join(root, "AggregatorV3Interface.sol", "AggregatorV3Interface.json"), func() abi.ABI { return bindings.NewAggregatorV3().ABI }},
		{"RWAFactory", filepath.Join(root, "RWAFactory.sol", "RWAFactory.json"), func() abi.ABI { return bindings.NewFactory().ABI }},
		{"RWAToken", filepath.Join(root, "RWAToken.sol", "RWAToken.json"), func() abi.ABI { return bindings.NewRWAToken().ABI }},
		// The ERC-20 binding is also used for the quote token, but each entry in it (the ERC-20 views
		// plus the compliance(), supplyController(), redemptionEscrow() and paused() getters) is declared
		// by RWAToken, so the RWAToken artifact is the one to check it against
		{"RWAToken (ERC-20 views)", filepath.Join(root, "RWAToken.sol", "RWAToken.json"), func() abi.ABI { return bindings.NewERC20().ABI }},
		// rwa-v2 deployments keep their own deploy selector and ProjectDeployed topic, which
		// the compiled rwa-v3 factory no longer carries, so a fixture pins them
		{"RWAFactory (rwa-v2)", findPinnedV2(), func() abi.ABI { return bindings.NewFactory().ABIV2 }},
	}

	// An artifact that cannot be loaded counts as a mismatch, so a renamed or
	// missing Foundry output fails the check instead of skipping that contract.
	// The compared count confirms that every expected artifact was checked.
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
	fmt.Printf("verifyabi: OK, no drift between internal/bindings and the %d contract ABIs checked\n", compared)
}

// findPinnedV2 returns the path of the pinned rwa-v2 RWAFactory ABI, found relative to
// the working directory (server/ or the repository root)
func findPinnedV2() string {
	rel := filepath.Join("internal", "bindings", "testdata", "rwa-v2", "RWAFactory.json")
	for _, candidate := range []string{rel, filepath.Join("server", rel)} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return rel
}

// findContractsOut returns the absolute path of contracts/out, found relative
// to the working directory, which is the repository root or server/. It
// returns "" if none exists.
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
	for errName, e := range mine.Errors {
		re, ok := real.Errors[errName]
		if !ok {
			fmt.Printf("  %s: error %s missing from real ABI\n", name, errName)
			mismatches++
			continue
		}
		if e.ID != re.ID {
			fmt.Printf("  %s: error %s selector mismatch: mine=%s real=%s\n", name, errName, e.ID.Hex(), re.ID.Hex())
			mismatches++
		}
	}
	return mismatches
}
