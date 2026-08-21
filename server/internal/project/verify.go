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

// verifyBytecode is the "deployed bytecode is present" check: true only if
// every deployed address has non-empty on-chain code, proving something was
// actually deployed at each address (catches a wrong/empty address from a
// misbehaving factory event). It does NOT compare bytecode against a
// known-good hash from contracts/out, since the server does not bundle
// contract build artifacts — a byte-for-byte check (including factory helper
// contracts) would require bundling contracts/out's deployedBytecode into the
// server binary/release process and is a possible follow-up.
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

// knownRoles is every role the platform grants; deployment verification
// enumerates ALL of them on EVERY deployed contract so an unexpected holder
// of any role on any contract is detected, not just the ones an operator
// would normally use.
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

// verifyRoles enumerates the COMPLETE set of holders for every role on every
// deployed contract via AccessControlEnumerable (getRoleMemberCount/
// getRoleMember) and compares it against the exact allowlist derived from the
// DeployRequest. Enumerating the complete holder set (rather than just
// checking hasRole for a few known candidate addresses) verifies both that
// the required holders are present AND that there are no unexpected ones — a
// candidate-only check is structurally blind to an ADDITIONAL malicious or
// accidentally-retained holder, e.g. an out-of-band grantRole to an attacker
// or a bootstrap role the factory failed to renounce. Every child contract
// inherits
// AccessControlEnumerable, so the enumerable calls are always available.
//
// Returns roles (roleName -> actual holder hex addresses, deduped, for
// display), missing (expected holders that are absent), and unexpected
// (actual holders not on the allowlist). VerifyDeployment fails the
// deployment when either missing or unexpected is non-empty.
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
			return // an unconfigured optional role holder: the allowlist for it stays empty, so ANY actual holder is unexpected
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
	// Deterministic order — the maps above iterate randomly, and these feed
	// directly into the persisted VerificationNote reason string.
	sort.Strings(missing)
	sort.Strings(unexpected)
	for name := range roles {
		sort.Strings(roles[name])
	}
	return roles, missing, unexpected, nil
}

// enumerateRoleHolders returns every address currently holding role on
// contract, via AccessControlEnumerable's getRoleMemberCount + getRoleMember.
// Role holder sets are tiny (single-digit) in this system, so a simple
// count-then-index loop is fine.
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

// factoryBootstrapRoles is every role RWAFactory.deploy grants to ITSELF
// during construction (to wire the child contracts and register system
// addresses) and MUST renounce before returning. It is not enough to check
// only DEFAULT_ADMIN_ROLE: the factory also takes a bootstrap COMPLIANCE_ROLE
// (and others) that it must renounce. Checked on every contract that hosts
// the corresponding role, not just the ones a normal
// operator would use — a factory that retained ANY of these could re-wire
// the deployment after the fact.
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

// verifyNoFactoryRoles confirms the factory itself retains NONE of
// factoryBootstrapRoles on any deployed contract. A factory that still held
// any bootstrap role somewhere could re-grant, revoke, or directly exercise
// that role at will, silently defeating the configured operator's
// exclusivity — checking only DEFAULT_ADMIN_ROLE would miss exactly this for
// COMPLIANCE_ROLE and the rest.
func verifyNoFactoryRoles(ctx context.Context, client blockchain.Client, factoryAddr common.Address, addrs models.Addresses) (bool, error) {
	ac := bindings.NewAccessControl()
	for _, a := range []string{addrs.Token, addrs.Compliance, addrs.SupplyController, addrs.Vault, addrs.RedemptionEscrow, addrs.Strategy} {
		if a == "" {
			continue
		}
		for _, role := range factoryBootstrapRoles {
			has, err := hasRole(ctx, client, ac, common.HexToAddress(a), role.id, factoryAddr)
			if err != nil {
				return false, fmt.Errorf("project: hasRole(%s) for factory on %s: %w", role.name, a, err)
			}
			if has {
				return false, nil
			}
		}
	}
	return true, nil
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

// verifySystemAllowlist confirms both pinned system addresses (Vault and
// RedemptionEscrow) are registered Allowed in the compliance registry. It
// checks isSystemAddress AND the exact permanent (Allowed, validUntil=0)
// record, not just isAllowed: isAllowed alone cannot tell "this is a pinned
// system address" apart from "this happens to be an ordinary wallet a
// compliance operator manually allowed with the same validUntil semantics as
// any other subject". isSystemAddress is the actual protection here (see
// ComplianceRegistry's SystemAddressCannotBeBlocked check), and getRecord's
// exact (Allowed, validUntil=0) tuple confirms nothing set a finite expiry
// that could later lapse.
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
		// ComplianceStatusAllowed==1, ValidUntil must be exactly 0 (no
		// expiry) — anything else means the pin isn't the permanent,
		// unconditional Allowed record a system address requires.
		if rec.Status != uint8(bindings.ComplianceStatusAllowed) || rec.ValidUntil != 0 {
			return false, nil
		}
	}
	return true, nil
}

// verifyImmutableConfig cross-checks each contract's on-chain immutable/
// config getters against the deploy request that produced them: the token's
// compliance/supplyController wiring, every contract's cross-reference back to
// token/vault/strategy, and decimals. Not all of these are actually immutable
// at the Solidity level (some, like Vault.treasury and
// SupplyController.auditor, are admin-mutable later; a mismatch checked
// here, immediately post-deploy, still means the factory did not wire the
// requested value in the first place). Returns (false, humanReadableReason,
// nil) on the first mismatch found, or a non-nil error only for an
// RPC/decode failure.
//
// vaultStrategy is what Vault.strategy() must currently equal. It is a
// parameter rather than p.Addresses.Strategy because Vault.setStrategy is
// admin-callable: at deploy time the two are the same, but a later drift
// re-check has to compare against the LIVE strategy (Security.Strategy) or
// it would report every legitimate swap as a mismatch. RedemptionEscrow's
// strategy is immutable and so keeps comparing against the deployed one.
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
		// The token's one-time redemptionEscrow pointer is what authorizes
		// returnEscrowedRWA's pause-bypassing escrow return path
		// (RWAToken.returnEscrowedRWA gates on msg.sender == redemptionEscrow),
		// so a mismatch here would later misauthorize or break escrow returns.
		// Factory bytecode verification makes an accidental mismatch unlikely,
		// but a corrupted deployment record or future factory regression would
		// otherwise escape this explicit wiring verification.
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

// viewAddress/viewUint64/viewUint256/viewBytes32 call a no-argument view
// function on contract via client.CodeAt/CallContract, using contractABI to
// pack the call and unpack a single-value result. These exist because
// internal/bindings (a hand-written stand-in — see its package doc) only wraps
// the specific calls each workflow package needs; adding a Pack.../Unpack...
// method per getter for every immutable-config check below would be pure
// boilerplate over functions the ABI literals there already declare.
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
