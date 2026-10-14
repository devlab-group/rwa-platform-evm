package project

import (
	"context"
	"fmt"
	"math/big"
	"sort"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/models"
)

// verifyBytecode reports whether every deployed address has non-empty on-chain
// code, which catches an empty or wrong address in the factory event. It does
// not compare the code with a known-good hash, because the server does not
// bundle the contract build artifacts from contracts/out.
func verifyBytecode(ctx context.Context, client blockchain.Client, addrs models.Addresses) (bool, error) {
	for _, a := range []string{addrs.Token, addrs.Compliance, addrs.SupplyController, addrs.Vault, addrs.RedemptionEscrow, addrs.Strategy} {
		if a == "" {
			return false, nil
		}
		code, err := client.CodeAt(ctx, common.HexToAddress(a), nil)
		if err != nil {
			return false, fmt.Errorf("project: CodeAt(%s): %w", a, err)
		}
		if len(code) == 0 {
			return false, nil
		}
	}
	return true, nil
}

// knownRoles is every role the platform defines. Deployment verification
// enumerates all of them on every deployed contract, so an unexpected holder of
// any role on any contract is detected, not only the roles a contract normally
// uses.
var knownRoles = []struct {
	name string
	id   [32]byte
}{
	{"DEFAULT_ADMIN_ROLE", bindings.DefaultAdminRole},
	{"PAUSER_ROLE", bindings.PauserRole},
	{"COMPLIANCE_ROLE", bindings.ComplianceRole},
	{"PRICER_ROLE", bindings.PricerRole},
	{"TREASURER_ROLE", bindings.TreasurerRole},
	{"REDEMPTION_MANAGER_ROLE", bindings.RedemptionManagerRole},
}

// verifyRoles enumerates each deployed contract's role holders through
// getRoleMemberCount/getRoleMember and compares them with the signed
// ProjectConfig allowlist. Enumeration detects unexpected grants and leftover
// bootstrap holders that hasRole checks on expected addresses would miss.
// All project contracts expose AccessControlEnumerable.
//
// Returns deduplicated holders by role for display, missing expected holders
// and unexpected actual holders. Either mismatch fails VerifyDeployment.
func verifyRoles(ctx context.Context, client blockchain.Client, p *models.Project) (roles map[string][]string, missing, unexpected []string, err error) {
	ac := bindings.NewAccessControl()
	contracts := []struct {
		label string
		addr  common.Address
	}{
		{"token", common.HexToAddress(p.Addresses.Token)},
		{"compliance", common.HexToAddress(p.Addresses.Compliance)},
		{"supplyController", common.HexToAddress(p.Addresses.SupplyController)},
		{"vault", common.HexToAddress(p.Addresses.Vault)},
		{"redemptionEscrow", common.HexToAddress(p.Addresses.RedemptionEscrow)},
		{"strategy", common.HexToAddress(p.Addresses.Strategy)},
	}

	// expected[contractLabel][roleName] = set of allowed holder hex addresses.
	expected := map[string]map[string]map[string]bool{}
	addExpected := func(cLabel, roleName string, holder common.Address) {
		if holder == (common.Address{}) {
			return // an unconfigured optional role holder: its allowlist stays empty, so any actual holder is unexpected
		}
		if expected[cLabel] == nil {
			expected[cLabel] = map[string]map[string]bool{}
		}
		if expected[cLabel][roleName] == nil {
			expected[cLabel][roleName] = map[string]bool{}
		}
		expected[cLabel][roleName][holder.Hex()] = true
	}
	admin := common.HexToAddress(p.Admin)
	for _, c := range contracts {
		addExpected(c.label, "DEFAULT_ADMIN_ROLE", admin) // admin is DEFAULT_ADMIN on every child
	}
	addExpected("token", "PAUSER_ROLE", admin)
	addExpected("compliance", "COMPLIANCE_ROLE", common.HexToAddress(p.ComplianceOperator))
	addExpected("strategy", "PRICER_ROLE", common.HexToAddress(p.Pricer))
	addExpected("vault", "TREASURER_ROLE", common.HexToAddress(p.Treasurer))
	addExpected("redemptionEscrow", "TREASURER_ROLE", common.HexToAddress(p.Treasurer))
	addExpected("redemptionEscrow", "REDEMPTION_MANAGER_ROLE", common.HexToAddress(p.RedemptionManager))

	roles = map[string][]string{}
	rolesSeen := map[string]map[string]bool{}
	for _, c := range contracts {
		if c.addr == (common.Address{}) {
			continue
		}
		for _, role := range knownRoles {
			holders, err := enumerateRoleHolders(ctx, client, ac, c.addr, role.id)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("project: enumerate %s on %s: %w", role.name, c.label, err)
			}
			actual := map[string]bool{}
			for _, h := range holders {
				actual[h.Hex()] = true
				if rolesSeen[role.name] == nil {
					rolesSeen[role.name] = map[string]bool{}
				}
				if !rolesSeen[role.name][h.Hex()] {
					rolesSeen[role.name][h.Hex()] = true
					roles[role.name] = append(roles[role.name], h.Hex())
				}
			}
			exp := expected[c.label][role.name]
			for want := range exp {
				if !actual[want] {
					missing = append(missing, fmt.Sprintf("%s on %s for %s", role.name, c.label, want))
				}
			}
			for got := range actual {
				if !exp[got] {
					unexpected = append(unexpected, fmt.Sprintf("%s on %s held by unexpected %s", role.name, c.label, got))
				}
			}
		}
	}
	// sort for a deterministic order: map iteration is random, and these feed the
	// persisted VerificationNote
	sort.Strings(missing)
	sort.Strings(unexpected)
	for name := range roles {
		sort.Strings(roles[name])
	}
	return roles, missing, unexpected, nil
}

// enumerateRoleHolders returns every address currently holding role on contract,
// using AccessControlEnumerable's getRoleMemberCount and getRoleMember. It reads
// the members one call at a time, which is acceptable because the holder sets
// are small.
func enumerateRoleHolders(ctx context.Context, client blockchain.Client, ac bindings.AccessControl, contract common.Address, role [32]byte) ([]common.Address, error) {
	countData, err := ac.PackGetRoleMemberCount(role)
	if err != nil {
		return nil, err
	}
	countOut, err := blockchain.Call(ctx, client, contract, countData)
	if err != nil {
		return nil, err
	}
	count, err := ac.UnpackGetRoleMemberCount(countOut)
	if err != nil {
		return nil, err
	}
	n := count.Int64()
	holders := make([]common.Address, 0, n)
	for i := int64(0); i < n; i++ {
		data, err := ac.PackGetRoleMember(role, big.NewInt(i))
		if err != nil {
			return nil, err
		}
		out, err := blockchain.Call(ctx, client, contract, data)
		if err != nil {
			return nil, err
		}
		addr, err := ac.UnpackGetRoleMember(out)
		if err != nil {
			return nil, err
		}
		holders = append(holders, addr)
	}
	return holders, nil
}

// factoryBootstrapRoles lists the roles the factory must not hold after deploy.
// RWAFactory takes a bootstrap COMPLIANCE_ROLE on the registry to allowlist the
// Vault and escrow and renounces it before deploy returns, and it never holds
// DEFAULT_ADMIN_ROLE. The check covers every role on every contract, so a
// factory that kept any of them, and could re-wire the deployment, is caught.
var factoryBootstrapRoles = []struct {
	name string
	id   [32]byte
}{
	{"DEFAULT_ADMIN_ROLE", bindings.DefaultAdminRole},
	{"COMPLIANCE_ROLE", bindings.ComplianceRole},
	{"PAUSER_ROLE", bindings.PauserRole},
	{"PRICER_ROLE", bindings.PricerRole},
	{"TREASURER_ROLE", bindings.TreasurerRole},
	{"REDEMPTION_MANAGER_ROLE", bindings.RedemptionManagerRole},
}

// verifyNoFactoryRoles returns the roles the factory still holds, as
// "ROLE on Contract" entries in a deterministic order, so an empty result means
// the factory kept nothing. A factory that kept a role could grant, revoke or
// use it, which would defeat the configured operators' exclusive control.
// Checking only DEFAULT_ADMIN_ROLE would miss a retained COMPLIANCE_ROLE.
func verifyNoFactoryRoles(ctx context.Context, client blockchain.Client, factoryAddr common.Address, addrs models.Addresses) ([]string, error) {
	ac := bindings.NewAccessControl()
	contracts := []struct{ name, addr string }{
		{"RWAToken", addrs.Token},
		{"ComplianceRegistry", addrs.Compliance},
		{"SupplyController", addrs.SupplyController},
		{"Vault", addrs.Vault},
		{"RedemptionEscrow", addrs.RedemptionEscrow},
		{"FixedPriceStrategy", addrs.Strategy},
	}
	var retained []string
	for _, c := range contracts {
		if c.addr == "" {
			continue
		}
		for _, role := range factoryBootstrapRoles {
			has, err := hasRole(ctx, client, ac, common.HexToAddress(c.addr), role.id, factoryAddr)
			if err != nil {
				return nil, fmt.Errorf("project: hasRole(%s) for factory on %s: %w", role.name, c.name, err)
			}
			if has {
				retained = append(retained, fmt.Sprintf("%s on %s", role.name, c.name))
			}
		}
	}
	return retained, nil
}

func hasRole(ctx context.Context, client blockchain.Client, ac bindings.AccessControl, contract common.Address, role [32]byte, candidate common.Address) (bool, error) {
	data, err := ac.PackHasRole(role, candidate)
	if err != nil {
		return false, err
	}
	out, err := blockchain.Call(ctx, client, contract, data)
	if err != nil {
		return false, err
	}
	return ac.UnpackHasRole(out)
}

// verifySystemAllowlist reports whether the Vault and RedemptionEscrow are pinned
// system addresses in the compliance registry with a permanent Allowed record.
// It checks isSystemAddress and the exact (Allowed, validUntil 0) record from
// getRecord rather than isAllowed, which cannot distinguish a pinned system
// address from an ordinary wallet that an operator allowed. isSystemAddress is
// what makes the registry reject any other status or expiry for the address
// (SystemAddressCannotBeBlocked), and a validUntil of 0 confirms the record
// cannot lapse.
func verifySystemAllowlist(ctx context.Context, client blockchain.Client, addrs models.Addresses) (bool, error) {
	if addrs.Compliance == "" || addrs.Vault == "" || addrs.RedemptionEscrow == "" {
		return false, nil
	}
	cr := bindings.NewComplianceRegistry()
	compliance := common.HexToAddress(addrs.Compliance)
	for _, sys := range []string{addrs.Vault, addrs.RedemptionEscrow} {
		sysAddr := common.HexToAddress(sys)

		data, err := cr.ABI.Pack("isSystemAddress", sysAddr)
		if err != nil {
			return false, err
		}
		out, err := blockchain.Call(ctx, client, compliance, data)
		if err != nil {
			return false, fmt.Errorf("project: isSystemAddress(%s): %w", sys, err)
		}
		unpacked, err := cr.ABI.Unpack("isSystemAddress", out)
		if err != nil {
			return false, err
		}
		if !*abi.ConvertType(unpacked[0], new(bool)).(*bool) {
			return false, nil
		}

		recData, err := cr.PackGetRecord(sysAddr)
		if err != nil {
			return false, err
		}
		recOut, err := blockchain.Call(ctx, client, compliance, recData)
		if err != nil {
			return false, fmt.Errorf("project: getRecord(%s): %w", sys, err)
		}
		rec, err := cr.UnpackGetRecord(recOut)
		if err != nil {
			return false, err
		}
		// a system address needs the permanent record: status Allowed and
		// validUntil exactly 0 (no expiry)
		if rec.Status != uint8(bindings.ComplianceStatusAllowed) || rec.ValidUntil != 0 {
			return false, nil
		}
	}
	return true, nil
}

// verifyImmutableConfig compares on-chain getters with p: token wiring,
// cross-contract references, quote token, treasury, auditor, profileDigest,
// redemptionTimeout, decimals and non-empty price expectations. Adoption uses
// deploy values; CheckConfigDrift supplies live expectations for mutable fields.
// The first mismatch returns (false, reason, nil); RPC or decoding failures
// return an error.
//
// vaultStrategy supplies the expected Vault.strategy separately from
// p.Addresses.Strategy, allowing drift checks to use its live projection.
// The escrow strategy remains compared with the deployed address.
func verifyImmutableConfig(ctx context.Context, client blockchain.Client, p *models.Project, vaultStrategy common.Address) (bool, string, error) {
	tokenABI := bindings.NewERC20().ABI
	vaultABI := bindings.NewVault().ABI
	escrowABI := bindings.NewRedemptionEscrow().ABI
	controllerABI := bindings.NewSupplyController().ABI
	strategyABI := bindings.NewFixedPriceStrategy().ABI

	token := common.HexToAddress(p.Addresses.Token)
	compliance := common.HexToAddress(p.Addresses.Compliance)
	vault := common.HexToAddress(p.Addresses.Vault)
	escrow := common.HexToAddress(p.Addresses.RedemptionEscrow)
	controller := common.HexToAddress(p.Addresses.SupplyController)
	strategy := common.HexToAddress(p.Addresses.Strategy)
	wantQuoteToken := common.HexToAddress(p.QuoteToken)
	wantTreasury := common.HexToAddress(p.Treasury)
	wantAuditor := common.HexToAddress(p.Auditor)

	addressChecks := []struct {
		label    string
		abi      abi.ABI
		contract common.Address
		method   string
		want     common.Address
	}{
		{"token.compliance", tokenABI, token, "compliance", compliance},
		{"token.supplyController", tokenABI, token, "supplyController", controller},
		// RWAToken.returnEscrowedRWA, the escrow's return path that bypasses the
		// pause, accepts only msg.sender == redemptionEscrow, so a wrong pointer
		// would misauthorize or break escrow returns
		{"token.redemptionEscrow", tokenABI, token, "redemptionEscrow", escrow},
		{"vault.token", vaultABI, vault, "token", token},
		{"vault.quoteToken", vaultABI, vault, "quoteToken", wantQuoteToken},
		{"vault.treasury", vaultABI, vault, "treasury", wantTreasury},
		{"vault.strategy", vaultABI, vault, "strategy", vaultStrategy},
		{"escrow.token", escrowABI, escrow, "token", token},
		{"escrow.quoteToken", escrowABI, escrow, "quoteToken", wantQuoteToken},
		{"escrow.vault", escrowABI, escrow, "vault", vault},
		{"escrow.strategy", escrowABI, escrow, "strategy", strategy},
		{"controller.vault", controllerABI, controller, "vault", vault},
		{"controller.token", controllerABI, controller, "token", token},
		{"controller.auditor", controllerABI, controller, "auditor", wantAuditor},
	}
	for _, c := range addressChecks {
		got, err := viewAddress(ctx, client, c.abi, c.contract, c.method)
		if err != nil {
			return false, "", fmt.Errorf("project: read %s: %w", c.label, err)
		}
		if got != c.want {
			return false, fmt.Sprintf("%s = %s, want %s", c.label, got.Hex(), c.want.Hex()), nil
		}
	}

	if got, err := viewBytes32(ctx, client, controllerABI, controller, "profileDigest"); err != nil {
		return false, "", fmt.Errorf("project: read controller.profileDigest: %w", err)
	} else {
		want, err := hexToBytes32(p.ProfileDigest)
		if err != nil {
			return false, "", err
		}
		if got != want {
			return false, fmt.Sprintf("controller.profileDigest = 0x%x, want %s", got, p.ProfileDigest), nil
		}
	}

	if got, err := viewUint64(ctx, client, escrowABI, escrow, "redemptionTimeout"); err != nil {
		return false, "", fmt.Errorf("project: read escrow.redemptionTimeout: %w", err)
	} else if int64(got) != p.RedemptionTimeout {
		return false, fmt.Sprintf("escrow.redemptionTimeout = %d, want %d", got, p.RedemptionTimeout), nil
	}

	if got, err := viewUint8(ctx, client, tokenABI, token, "decimals"); err != nil {
		return false, "", fmt.Errorf("project: read token.decimals: %w", err)
	} else if got != p.TokenDecimals {
		return false, fmt.Sprintf("token.decimals = %d, want %d", got, p.TokenDecimals), nil
	}
	if got, err := viewUint8(ctx, client, strategyABI, strategy, "tokenDecimals"); err != nil {
		return false, "", fmt.Errorf("project: read strategy.tokenDecimals: %w", err)
	} else if got != p.TokenDecimals {
		return false, fmt.Sprintf("strategy.tokenDecimals = %d, want %d", got, p.TokenDecimals), nil
	}

	if p.PurchasePricePerWholeToken != "" {
		want, err := parseBigInt(p.PurchasePricePerWholeToken)
		if err != nil {
			return false, "", err
		}
		got, err := viewUint256(ctx, client, strategyABI, strategy, "purchasePricePerWholeToken")
		if err != nil {
			return false, "", fmt.Errorf("project: read strategy.purchasePricePerWholeToken: %w", err)
		}
		if got.Cmp(want) != 0 {
			return false, fmt.Sprintf("strategy.purchasePricePerWholeToken = %s, want %s", got, want), nil
		}
	}
	if p.RedemptionPricePerWholeToken != "" {
		want, err := parseBigInt(p.RedemptionPricePerWholeToken)
		if err != nil {
			return false, "", err
		}
		got, err := viewUint256(ctx, client, strategyABI, strategy, "redemptionPricePerWholeToken")
		if err != nil {
			return false, "", fmt.Errorf("project: read strategy.redemptionPricePerWholeToken: %w", err)
		}
		if got.Cmp(want) != 0 {
			return false, fmt.Sprintf("strategy.redemptionPricePerWholeToken = %s, want %s", got, want), nil
		}
	}
	return true, "", nil
}

// The view helpers (viewAddress, viewUint8, viewUint64, viewUint256, viewString,
// viewBytes32) call a no-argument view function on contract through
// blockchain.Call, packing the call and unpacking a single-value result with
// contractABI. They avoid adding a Pack and Unpack method to internal/bindings
// for every getter that the checks above read, since the ABI literals there
// already declare the functions.
func viewAddress(ctx context.Context, client blockchain.Client, contractABI abi.ABI, contract common.Address, method string) (common.Address, error) {
	out, err := callView(ctx, client, contractABI, contract, method)
	if err != nil {
		return common.Address{}, err
	}
	return *abi.ConvertType(out[0], new(common.Address)).(*common.Address), nil
}

func viewUint64(ctx context.Context, client blockchain.Client, contractABI abi.ABI, contract common.Address, method string) (uint64, error) {
	out, err := callView(ctx, client, contractABI, contract, method)
	if err != nil {
		return 0, err
	}
	return *abi.ConvertType(out[0], new(uint64)).(*uint64), nil
}

func viewUint8(ctx context.Context, client blockchain.Client, contractABI abi.ABI, contract common.Address, method string) (uint8, error) {
	out, err := callView(ctx, client, contractABI, contract, method)
	if err != nil {
		return 0, err
	}
	return *abi.ConvertType(out[0], new(uint8)).(*uint8), nil
}

func viewString(ctx context.Context, client blockchain.Client, contractABI abi.ABI, contract common.Address, method string) (string, error) {
	out, err := callView(ctx, client, contractABI, contract, method)
	if err != nil {
		return "", err
	}
	return *abi.ConvertType(out[0], new(string)).(*string), nil
}

func viewUint256(ctx context.Context, client blockchain.Client, contractABI abi.ABI, contract common.Address, method string) (*big.Int, error) {
	out, err := callView(ctx, client, contractABI, contract, method)
	if err != nil {
		return nil, err
	}
	return abi.ConvertType(out[0], new(big.Int)).(*big.Int), nil
}

func viewBytes32(ctx context.Context, client blockchain.Client, contractABI abi.ABI, contract common.Address, method string) ([32]byte, error) {
	out, err := callView(ctx, client, contractABI, contract, method)
	if err != nil {
		return [32]byte{}, err
	}
	return *abi.ConvertType(out[0], new([32]byte)).(*[32]byte), nil
}

func callView(ctx context.Context, client blockchain.Client, contractABI abi.ABI, contract common.Address, method string) ([]any, error) {
	data, err := contractABI.Pack(method)
	if err != nil {
		return nil, fmt.Errorf("pack %s: %w", method, err)
	}
	raw, err := blockchain.Call(ctx, client, contract, data)
	if err != nil {
		return nil, fmt.Errorf("call %s on %s: %w", method, contract.Hex(), err)
	}
	out, err := contractABI.Unpack(method, raw)
	if err != nil {
		return nil, fmt.Errorf("unpack %s: %w", method, err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s returned no values", method)
	}
	return out, nil
}
