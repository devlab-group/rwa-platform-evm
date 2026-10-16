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

// verifyBytecode checks code presence at deployed child addresses, including rwa-v3 income.
// It does not verify identity or hashes; the server does not bundle contracts/out artifacts.
func verifyBytecode(ctx context.Context, client blockchain.Client, addrs models.Addresses) (bool, error) {
	list := []string{addrs.Token, addrs.Compliance, addrs.SupplyController, addrs.Vault, addrs.RedemptionEscrow, addrs.Strategy}
	if addrs.IncomeDistributor != "" {
		list = append(list, addrs.IncomeDistributor)
	}
	for _, a := range list {
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

// knownRoles lists all platform-defined roles; verification checks each on every child
// contract, including roles that contract normally leaves unused
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

// incomeManagerRole is INCOME_MANAGER_ROLE, which rwa-v3 projects add to knownRoles
var incomeManagerRole = struct {
	name string
	id   [32]byte
}{"INCOME_MANAGER_ROLE", bindings.IncomeManagerRole}

// rolesFor adds INCOME_MANAGER_ROLE to knownRoles for checks on rwa-v3 role-bearing contracts
func rolesFor(p *models.Project) []struct {
	name string
	id   [32]byte
} {
	if p.Addresses.IncomeDistributor == "" {
		return knownRoles
	}
	return append(append([]struct {
		name string
		id   [32]byte
	}{}, knownRoles...), incomeManagerRole)
}

// verifyRoles compares each child's getRoleMemberCount/getRoleMember enumeration with
// the signed ProjectConfig allowlist. All children expose AccessControlEnumerable;
// enumeration catches unexpected grants and bootstrap holders that expected-address
// hasRole checks miss.
//
// Returns deduplicated holders by role, missing holders and unexpected holders; either
// mismatch fails VerifyDeployment.
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
	if p.Addresses.IncomeDistributor != "" {
		contracts = append(contracts, struct {
			label string
			addr  common.Address
		}{"incomeDistributor", common.HexToAddress(p.Addresses.IncomeDistributor)})
	}
	// An OraclePriceStrategy has no AccessControl, so it has no roles to enumerate
	if p.StrategyKind == models.StrategyKindOracle {
		kept := contracts[:0:0]
		for _, c := range contracts {
			if c.label != "strategy" {
				kept = append(kept, c)
			}
		}
		contracts = kept
	}

	// expected[contractLabel][roleName] = set of allowed holder hex addresses.
	expected := map[string]map[string]map[string]bool{}
	addExpected := func(cLabel, roleName string, holder common.Address) {
		if holder == (common.Address{}) {
			return // Unset optional holder: an empty allowlist makes every actual holder unexpected
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
	addExpected("incomeDistributor", "INCOME_MANAGER_ROLE", common.HexToAddress(p.IncomeManager))

	roles = map[string][]string{}
	rolesSeen := map[string]map[string]bool{}
	for _, c := range contracts {
		if c.addr == (common.Address{}) {
			continue
		}
		for _, role := range rolesFor(p) {
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
	// Sort map-derived entries for deterministic persisted VerificationNote text
	sort.Strings(missing)
	sort.Strings(unexpected)
	for name := range roles {
		sort.Strings(roles[name])
	}
	return roles, missing, unexpected, nil
}

// enumerateRoleHolders reads AccessControlEnumerable's getRoleMemberCount and then each
// getRoleMember in separate calls to list role holders on contract
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

// factoryBootstrapRoles lists known roles the factory must not retain on any child
// after deployment. Its temporary COMPLIANCE_ROLE allowlists the Vault/escrow and is
// renounced before deploy returns; it never holds DEFAULT_ADMIN_ROLE.
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

// verifyNoFactoryRoles returns retained factory roles as deterministic "ROLE on
// Contract" entries. Empty means none of the checked roles remain; checking only
// DEFAULT_ADMIN_ROLE would miss a retained COMPLIANCE_ROLE and its privileges.
func verifyNoFactoryRoles(ctx context.Context, client blockchain.Client, factoryAddr common.Address, p *models.Project) ([]string, error) {
	ac := bindings.NewAccessControl()
	addrs := p.Addresses
	contracts := []struct{ name, addr string }{
		{"RWAToken", addrs.Token},
		{"ComplianceRegistry", addrs.Compliance},
		{"SupplyController", addrs.SupplyController},
		{"Vault", addrs.Vault},
		{"RedemptionEscrow", addrs.RedemptionEscrow},
		{"FixedPriceStrategy", addrs.Strategy},
		{"IncomeDistributor", addrs.IncomeDistributor},
	}
	var retained []string
	for _, c := range contracts {
		if c.addr == "" {
			continue
		}
		if c.name == "FixedPriceStrategy" && p.StrategyKind == models.StrategyKindOracle {
			continue // no AccessControl to hold a role
		}
		roles := factoryBootstrapRoles
		if c.name == "IncomeDistributor" {
			roles = append(append(roles[:0:0], roles...), struct {
				name string
				id   [32]byte
			}{incomeManagerRole.name, incomeManagerRole.id})
		}
		for _, role := range roles {
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

// verifySystemAllowlist requires the Vault and RedemptionEscrow to be pinned system
// addresses with exact getRecord values (Allowed, validUntil 0). isAllowed alone cannot
// distinguish ordinary allowlisting from pinning. isSystemAddress makes status/expiry
// changes fail with SystemAddressCannotBeBlocked; validUntil 0 prevents expiry.
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
		// Pinned addresses require Allowed with validUntil 0 (no expiry)
		if rec.Status != uint8(bindings.ComplianceStatusAllowed) || rec.ValidUntil != 0 {
			return false, nil
		}
	}
	return true, nil
}

// verifyImmutableConfig compares getters with p: token wiring, contract references,
// quote token, treasury, auditor, profileDigest, redemptionTimeout, decimals and
// non-empty prices. Adoption uses deploy expectations; CheckConfigDrift substitutes
// mutable values.
//
// vaultStrategy is the separate Vault.strategy expectation. The rwa-v2 escrow holds the
// deployed p.Addresses.Strategy immutably; the rwa-v3 escrow reads Vault.strategy(), so
// it must equal vaultStrategy. An rwa-v3 project additionally checks the income
// distributor's wiring, Vault.minPurchaseQuote and, for an Oracle strategy, its
// immutables against the signed calldata. The first mismatch returns (false, reason,
// nil); RPC, decoding or invalid expected-value errors propagate.
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
	v3 := p.Version == bindings.FactoryVersionV3
	escrowStrategy := strategy
	if v3 {
		escrowStrategy = vaultStrategy
	}

	addressChecks := []struct {
		label    string
		abi      abi.ABI
		contract common.Address
		method   string
		want     common.Address
	}{
		{"token.compliance", tokenABI, token, "compliance", compliance},
		{"token.supplyController", tokenABI, token, "supplyController", controller},
		// RWAToken.returnEscrowedRWA bypasses pause only for msg.sender == redemptionEscrow;
		// a wrong pointer misauthorizes or breaks returns
		{"token.redemptionEscrow", tokenABI, token, "redemptionEscrow", escrow},
		{"vault.token", vaultABI, vault, "token", token},
		{"vault.quoteToken", vaultABI, vault, "quoteToken", wantQuoteToken},
		{"vault.treasury", vaultABI, vault, "treasury", wantTreasury},
		{"vault.strategy", vaultABI, vault, "strategy", vaultStrategy},
		{"escrow.token", escrowABI, escrow, "token", token},
		{"escrow.quoteToken", escrowABI, escrow, "quoteToken", wantQuoteToken},
		{"escrow.vault", escrowABI, escrow, "vault", vault},
		{"escrow.strategy", escrowABI, escrow, "strategy", escrowStrategy},
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

	if v3 {
		if ok, reason, err := verifyV3Config(ctx, client, p, vault, token, wantQuoteToken, strategy); err != nil || !ok {
			return ok, reason, err
		}
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

// verifyV3Config checks the rwa-v3 additions: the income distributor's token,
// quoteToken and vault, Vault.minPurchaseQuote and the Oracle strategy's immutables.
// strategy is the deployed strategy, whose immutables the signed calldata describes.
func verifyV3Config(ctx context.Context, client blockchain.Client, p *models.Project, vault, token, quoteToken, strategy common.Address) (bool, string, error) {
	incomeABI := bindings.NewIncomeDistributor().ABI
	income := common.HexToAddress(p.Addresses.IncomeDistributor)
	for _, c := range []struct {
		label string
		want  common.Address
	}{{"token", token}, {"quoteToken", quoteToken}, {"vault", vault}} {
		got, err := viewAddress(ctx, client, incomeABI, income, c.label)
		if err != nil {
			return false, "", fmt.Errorf("project: read incomeDistributor.%s: %w", c.label, err)
		}
		if got != c.want {
			return false, fmt.Sprintf("incomeDistributor.%s = %s, want %s", c.label, got.Hex(), c.want.Hex()), nil
		}
	}

	if p.MinPurchaseQuote != "" {
		want, err := parseBigInt(p.MinPurchaseQuote)
		if err != nil {
			return false, "", err
		}
		got, err := viewUint256(ctx, client, bindings.NewVault().ABI, vault, "minPurchaseQuote")
		if err != nil {
			return false, "", fmt.Errorf("project: read vault.minPurchaseQuote: %w", err)
		}
		if got.Cmp(want) != 0 {
			return false, fmt.Sprintf("vault.minPurchaseQuote = %s, want %s", got, want), nil
		}
	}

	if p.StrategyKind != models.StrategyKindOracle {
		return true, "", nil
	}
	// roles skip an Oracle strategy, so a missing config must not leave it unchecked
	if p.Oracle == nil {
		return false, "oracle strategy has no stored configuration to verify against", nil
	}
	oracleABI := bindings.NewOraclePriceStrategy().ABI
	cfg := p.Oracle
	for _, c := range []struct {
		label  string
		method string
		want   common.Address
	}{
		{"feed", "feed", common.HexToAddress(cfg.Feed)},
		{"sequencerUptimeFeed", "sequencerUptimeFeed", common.HexToAddress(cfg.SequencerUptimeFeed)},
	} {
		got, err := viewAddress(ctx, client, oracleABI, strategy, c.method)
		if err != nil {
			return false, "", fmt.Errorf("project: read strategy.%s: %w", c.label, err)
		}
		if got != c.want {
			return false, fmt.Sprintf("strategy.%s = %s, want %s", c.label, got.Hex(), c.want.Hex()), nil
		}
	}
	for _, c := range []struct {
		method string
		want   uint64
	}{{"maxAge", cfg.MaxAge}, {"gracePeriod", cfg.GracePeriod}} {
		got, err := viewUint64(ctx, client, oracleABI, strategy, c.method)
		if err != nil {
			return false, "", fmt.Errorf("project: read strategy.%s: %w", c.method, err)
		}
		if got != c.want {
			return false, fmt.Sprintf("strategy.%s = %d, want %d", c.method, got, c.want), nil
		}
	}
	for _, c := range []struct {
		method string
		want   uint64
	}{{"purchaseSpreadBps", uint64(cfg.PurchaseSpreadBps)}, {"redemptionSpreadBps", uint64(cfg.RedemptionSpreadBps)}} {
		out, err := callView(ctx, client, oracleABI, strategy, c.method)
		if err != nil {
			return false, "", fmt.Errorf("project: read strategy.%s: %w", c.method, err)
		}
		if got := *abi.ConvertType(out[0], new(uint16)).(*uint16); uint64(got) != c.want {
			return false, fmt.Sprintf("strategy.%s = %d, want %d", c.method, got, c.want), nil
		}
	}
	// quoteDecimals is not in the calldata; it must equal the quote token's decimals()
	quoteDecimals, err := viewUint8(ctx, client, bindings.NewERC20().ABI, quoteToken, "decimals")
	if err != nil {
		return false, "", fmt.Errorf("project: read quoteToken.decimals: %w", err)
	}
	if got, err := viewUint8(ctx, client, oracleABI, strategy, "quoteDecimals"); err != nil {
		return false, "", fmt.Errorf("project: read strategy.quoteDecimals: %w", err)
	} else if got != quoteDecimals {
		return false, fmt.Sprintf("strategy.quoteDecimals = %d, want %d", got, quoteDecimals), nil
	}
	return true, "", nil
}

// viewAddress, viewUint8, viewUint64, viewUint256, viewString and viewBytes32 pack
// no-argument views, call blockchain.Call and unpack one result using contractABI.
// Existing ABI declarations avoid per-getter Pack/Unpack wrappers in internal/bindings.
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
