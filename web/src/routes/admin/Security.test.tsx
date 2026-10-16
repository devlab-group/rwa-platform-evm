import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Security } from "./Security";
import { api } from "../../lib/client";
import { encodeErrorResult, type Address } from "viem";
import { rwaTokenErrorsAbi } from "../../lib/abis";
import {
  readErc20Decimals,
  sendAcceptAdminTransfer,
  sendBeginAdminTransfer,
  sendForcedTransfer,
  sendRoleChange,
  sendSetFrozenTokens,
  sendSetMinPurchaseQuote,
  pauseCall,
  sendSetPaused,
  sendSetStrategyPrice,
  waitForTxReceipt,
} from "../../lib/wallet";
import { roleHash, ROLES } from "../../lib/roles";
import { exportSafeBatch } from "../../lib/safeBatch";
import { installFakeWallet, renderWithWallet } from "../../test/walletHarness";

// Scale quote prices with quote decimals: 2000000 at six decimals displays as 2
vi.mock("../../lib/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/client")>();
  return {
    ...actual,
    api: { ...actual.api, getProject: vi.fn(), getEnforcement: vi.fn() },
  };
});

// Spy on the batch export without downloading a browser file
vi.mock("../../lib/safeBatch", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/safeBatch")>();
  return {
    ...actual,
    exportSafeBatch: vi.fn(() => "Saved batch."),
  };
});

// Stub decimals and spy on writes to verify privileged calls without a chain. Defaults are
// vi.fn implementations, which mockReset restores.
vi.mock("../../lib/wallet", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/wallet")>();
  return {
    ...actual,
    readErc20Decimals: vi.fn(async () => 6),
    sendSetPaused: vi.fn(async () => "0xhash"),
    sendSetStrategyPrice: vi.fn(async () => "0xhash"),
    sendSetMinPurchaseQuote: vi.fn(async () => "0xhash"),
    sendRoleChange: vi.fn(async () => "0xhash"),
    sendSetFrozenTokens: vi.fn(async () => "0xhash"),
    sendForcedTransfer: vi.fn(async () => "0xhash"),
    sendBeginAdminTransfer: vi.fn(async () => "0xhash"),
    sendAcceptAdminTransfer: vi.fn(async () => "0xhash"),
    waitForTxReceipt: vi.fn(async () => undefined),
    // Keep the real revert decoder for error-message assertions
  };
});

// Default enforcement fixture
const NO_ENFORCEMENT = { securityStale: false };

// Reset mocks to keep tests independent
beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(api.getEnforcement).mockResolvedValue(NO_ENFORCEMENT);
});

const BASE = {
  chainId: 31337,
  addresses: { quoteToken: "0x0000000000000000000000000000000000000002" },
  purchasePricePerWholeToken: "2000000",
  redemptionPricePerWholeToken: "5500000",
};

// priceValue finds the value paired with a definition-list label
function priceValue(label: RegExp): string {
  const dt = screen.getByText(label);
  const dd = dt.parentElement?.querySelector("dd");
  return dd?.textContent ?? "";
}

describe("Security current prices", () => {
  beforeEach(() => {
    vi.mocked(readErc20Decimals).mockClear().mockResolvedValue(6);
    vi.mocked(api.getEnforcement).mockReset().mockResolvedValue(NO_ENFORCEMENT);
  });

  it("renders prices in human units using Project.quoteDecimals (no on-chain read)", async () => {
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({ ...BASE, quoteDecimals: 6 });

    render(<Security />);
    await waitFor(() =>
      expect(screen.getByText(/current purchase price/i)).toBeTruthy(),
    );

    // Scale 2000000 / 5500000 at 6 decimals to 2 / 5.5
    await waitFor(() =>
      expect(priceValue(/current purchase price/i)).toBe("2"),
    );
    expect(priceValue(/current redemption price/i)).toBe("5.5");
    // Project.quoteDecimals avoids an on-chain read
    expect(readErc20Decimals).not.toHaveBeenCalled();
  });

  it("falls back to the on-chain quote decimals when quoteDecimals is absent", async () => {
    vi.mocked(api.getProject).mockReset().mockResolvedValue(BASE);

    render(<Security />);
    await waitFor(() =>
      expect(priceValue(/current purchase price/i)).toBe("2"),
    );
    expect(readErc20Decimals).toHaveBeenCalledWith(
      31337,
      BASE.addresses.quoteToken,
    );
  });

  it("shows a hint when no on-chain prices are reported", async () => {
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({ chainId: 31337, quoteDecimals: 6 });

    render(<Security />);
    const card = await screen.findByRole("heading", {
      name: "Current prices",
    });
    await waitFor(() =>
      expect(
        within(card.parentElement as HTMLElement).getByText(
          /No prices reported/i,
        ),
      ).toBeTruthy(),
    );
  });
});

const ADMIN = "0x1111111111111111111111111111111111111111"; // fake wallet's account
const DEPLOYED = {
  chainId: 31337,
  paused: false,
  quoteDecimals: 6,
  addresses: {
    token: "0x0000000000000000000000000000000000000010",
    compliance: "0x0000000000000000000000000000000000000020",
    supplyController: "0x0000000000000000000000000000000000000030",
    vault: "0x0000000000000000000000000000000000000040",
    redemptionEscrow: "0x0000000000000000000000000000000000000050",
    strategy: "0x0000000000000000000000000000000000000060",
    quoteToken: "0x0000000000000000000000000000000000000070",
  },
};

describe("Security admin actions (role-gated, connected wallet broadcasts)", () => {
  let wallet: ReturnType<typeof installFakeWallet>;

  beforeEach(() => {
    wallet = installFakeWallet({ account: ADMIN, chainId: 31337 });
    vi.mocked(api.getEnforcement).mockReset().mockResolvedValue(NO_ENFORCEMENT);
    vi.mocked(sendSetPaused).mockClear().mockResolvedValue("0xhash");
    vi.mocked(sendSetStrategyPrice).mockClear().mockResolvedValue("0xhash");
    vi.mocked(sendRoleChange).mockClear().mockResolvedValue("0xhash");
    vi.mocked(sendBeginAdminTransfer).mockClear().mockResolvedValue("0xhash");
    vi.mocked(sendAcceptAdminTransfer).mockClear().mockResolvedValue("0xhash");
  });

  afterEach(() => {
    wallet.uninstall();
  });

  it("hides an action behind a notice when the connected wallet lacks the role", async () => {
    // No connected role holder; pause requires a route selection
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({
        ...DEPLOYED,
        roles: {},
      });
    renderWithWallet(<Security />, { connected: true });

    const heading = await screen.findByRole("heading", {
      name: "Pause / unpause trading",
    });
    const section = heading.closest("section") as HTMLElement;
    // Name the required role before a Safe is selected
    expect(within(section).getByText("PAUSER_ROLE")).toBeInTheDocument();
    expect(
      within(section).queryByRole("button", { name: "Pause trading" }),
    ).toBeNull();
  });

  it("gates admin actions by target contract", async () => {
    // Token-only admin can edit PAUSER_ROLE; transferring admin needs all governance contracts
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({
        ...DEPLOYED,
        roles: { DEFAULT_ADMIN_ROLE: [ADMIN] },
        rolesByContract: {
          token: { DEFAULT_ADMIN_ROLE: [ADMIN] },
          vault: {
            DEFAULT_ADMIN_ROLE: ["0x00000000000000000000000000000000000000a1"],
          },
        },
      });
    renderWithWallet(<Security />, { connected: true });

    expect(
      await screen.findByRole("button", { name: "Grant role" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Begin admin transfer" }),
    ).toBeNull();
  });

  it("broadcasts RWAToken pause() when the wallet holds PAUSER_ROLE", async () => {
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({ ...DEPLOYED, roles: { PAUSER_ROLE: [ADMIN] } });
    renderWithWallet(<Security />, { connected: true });

    const pauseBtn = await screen.findByRole("button", {
      name: "Pause trading",
    });
    fireEvent.click(pauseBtn);

    await waitFor(() =>
      expect(sendSetPaused).toHaveBeenCalledWith(
        31337,
        ADMIN,
        DEPLOYED.addresses.token,
        true,
      ),
    );
  });

  it("scales and broadcasts a purchase price to the strategy when the wallet holds PRICER_ROLE", async () => {
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({ ...DEPLOYED, roles: { PRICER_ROLE: [ADMIN] } });
    renderWithWallet(<Security />, { connected: true });

    const input = await screen.findByLabelText(
      /Purchase price \(whole quote-token units\)/,
    );
    fireEvent.change(input, { target: { value: "3" } });
    fireEvent.click(screen.getByRole("button", { name: "Set purchase price" }));

    await waitFor(() =>
      // 3 whole quote-token units at 6 decimals -> 3000000n.
      expect(sendSetStrategyPrice).toHaveBeenCalledWith(
        31337,
        ADMIN,
        DEPLOYED.addresses.strategy,
        "purchase",
        3000000n,
      ),
    );
  });

  it("grants a role on its target contract when the wallet holds DEFAULT_ADMIN_ROLE", async () => {
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({
        ...DEPLOYED,
        roles: { DEFAULT_ADMIN_ROLE: [ADMIN] },
      });
    renderWithWallet(<Security />, { connected: true });

    const account = await screen.findByLabelText("Account address");
    const grantee = "0x0000000000000000000000000000000000009999";
    fireEvent.change(account, { target: { value: grantee } });
    // Default role selection is PAUSER_ROLE, held only on the token.
    fireEvent.click(screen.getByRole("button", { name: "Grant role" }));

    await waitFor(() =>
      expect(sendRoleChange).toHaveBeenCalledWith(
        31337,
        ADMIN,
        DEPLOYED.addresses.token,
        "grant",
        roleHash(ROLES.pauser),
        grantee,
      ),
    );
  });
});

describe("Security accept admin role (incoming admin, not role-gated)", () => {
  let wallet: ReturnType<typeof installFakeWallet>;

  // Acceptance targets in adminTransferTargets order
  const GOVERNANCE_TARGETS = [
    DEPLOYED.addresses.token,
    DEPLOYED.addresses.compliance,
    DEPLOYED.addresses.supplyController,
    DEPLOYED.addresses.vault,
    DEPLOYED.addresses.redemptionEscrow,
    DEPLOYED.addresses.strategy,
  ];

  beforeEach(() => {
    wallet = installFakeWallet({ account: ADMIN, chainId: 31337 });
    vi.mocked(sendAcceptAdminTransfer).mockClear().mockResolvedValue("0xhash");
  });

  afterEach(() => {
    wallet.uninstall();
  });

  it("accepts on every governance contract when the connected wallet is the pending admin", async () => {
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({ ...DEPLOYED, roles: {}, pendingAdmin: ADMIN });
    renderWithWallet(<Security />, { connected: true });

    const acceptBtn = await screen.findByRole("button", {
      name: "Accept admin role",
    });
    fireEvent.click(acceptBtn);

    await waitFor(() =>
      expect(sendAcceptAdminTransfer).toHaveBeenCalledTimes(
        GOVERNANCE_TARGETS.length,
      ),
    );
    for (const target of GOVERNANCE_TARGETS) {
      expect(sendAcceptAdminTransfer).toHaveBeenCalledWith(
        31337,
        ADMIN,
        target,
      );
    }
  });

  it("saves a pause batch for a Safe holding PAUSER_ROLE instead of broadcasting", async () => {
    const safe = "0x9a7F3dfDEeBb51eAbBbA3c8eCCf8cF2a983A1C72";
    vi.mocked(exportSafeBatch).mockClear();
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({ ...DEPLOYED, roles: { PAUSER_ROLE: [safe] } });
    renderWithWallet(<Security />, { connected: true });

    fireEvent.change(await screen.findByLabelText(/Safe holding PAUSER_ROLE/), {
      target: { value: safe },
    });
    fireEvent.click(
      await screen.findByRole("button", { name: "Pause trading" }),
    );

    await screen.findByText("Saved batch.");
    expect(exportSafeBatch).toHaveBeenCalledWith(31337, safe, "Pause trading", [
      pauseCall(DEPLOYED.addresses.token as Address, true),
    ]);
    expect(sendSetPaused).not.toHaveBeenCalled();
  });

  it("saves one acceptance batch covering every governance contract for an incoming Safe admin", async () => {
    const safe = "0x9a7F3dfDEeBb51eAbBbA3c8eCCf8cF2a983A1C72";
    vi.mocked(exportSafeBatch).mockClear();
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({ ...DEPLOYED, roles: {}, pendingAdmin: safe });
    renderWithWallet(<Security />, { connected: true });

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Save acceptance batch for the incoming admin",
      }),
    );

    await screen.findByText("Saved batch.");
    const [chainId, target, name, calls] =
      vi.mocked(exportSafeBatch).mock.calls[0];
    expect([chainId, target, name]).toEqual([31337, safe, "Accept admin role"]);
    expect(calls.map((c) => c.to)).toEqual([
      DEPLOYED.addresses.token,
      DEPLOYED.addresses.compliance,
      DEPLOYED.addresses.supplyController,
      DEPLOYED.addresses.vault,
      DEPLOYED.addresses.redemptionEscrow,
      DEPLOYED.addresses.strategy,
    ]);
    expect(sendAcceptAdminTransfer).not.toHaveBeenCalled();
  });

  it("shows a read-only note (no accept button) when the pending admin is someone else", async () => {
    const other = "0x2222222222222222222222222222222222222222";
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({ ...DEPLOYED, roles: {}, pendingAdmin: other });
    renderWithWallet(<Security />, { connected: true });

    const heading = await screen.findByRole("heading", {
      name: "Accept admin role",
    });
    const section = heading.closest("section") as HTMLElement;
    expect(
      within(section).queryByRole("button", { name: "Accept admin role" }),
    ).toBeNull();
    expect(
      within(section).getByText(/admin transfer is pending to/i),
    ).toBeTruthy();
    expect(sendAcceptAdminTransfer).not.toHaveBeenCalled();
  });

  it("renders no accept section when there is no pending admin transfer", async () => {
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({ ...DEPLOYED, roles: {} });
    renderWithWallet(<Security />, { connected: true });

    // Wait for the loaded Status card
    await screen.findByRole("heading", { name: "Status" });
    expect(
      screen.queryByRole("heading", { name: "Accept admin role" }),
    ).toBeNull();
  });
});

describe("Security frozen balances (indexed read state)", () => {
  it("renders each frozen holder in whole tokens and keeps the staleness notice", async () => {
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({ ...DEPLOYED, decimals: 18 });
    vi.mocked(api.getEnforcement)
      .mockReset()
      .mockResolvedValue({
        securityAsOfBlock: 4321,
        securityStale: true,
        frozenBalances: {
          "0x000000000000000000000000000000000000AAA1": "1500000000000000000",
          // Preserve digits beyond float64 precision
          "0x000000000000000000000000000000000000BBB2":
            "123456789012345678901234567890",
        },
      });

    render(<Security />);
    const heading = await screen.findByRole("heading", {
      name: "Frozen balances",
    });
    const section = heading.closest("section") as HTMLElement;

    await waitFor(() =>
      expect(within(section).getByText("1.5")).toBeInTheDocument(),
    );
    expect(
      within(section).getByText("123,456,789,012.34567890123456789"),
    ).toBeInTheDocument();
    expect(within(section).getByText(/possibly stale/i)).toBeInTheDocument();
  });

  it("shows an empty state when nothing is frozen", async () => {
    vi.mocked(api.getProject).mockReset().mockResolvedValue(DEPLOYED);
    vi.mocked(api.getEnforcement).mockReset().mockResolvedValue(NO_ENFORCEMENT);

    render(<Security />);
    const heading = await screen.findByRole("heading", {
      name: "Frozen balances",
    });
    const section = heading.closest("section") as HTMLElement;
    await waitFor(() =>
      expect(
        within(section).getByText("No tokens are frozen."),
      ).toBeInTheDocument(),
    );
  });

  it("summarizes the last forced transfer only when one has happened", async () => {
    vi.mocked(api.getProject).mockReset().mockResolvedValue(DEPLOYED);
    vi.mocked(api.getEnforcement).mockReset().mockResolvedValue(NO_ENFORCEMENT);
    const { unmount } = render(<Security />);
    await screen.findByRole("heading", { name: "Frozen balances" });
    expect(
      screen.queryByRole("heading", { name: "Last forced transfer" }),
    ).toBeNull();
    unmount();

    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({ ...DEPLOYED, decimals: 18 });
    vi.mocked(api.getEnforcement)
      .mockReset()
      .mockResolvedValue({
        securityStale: false,
        lastForcedTransfer: {
          from: "0x000000000000000000000000000000000000AAA1",
          to: "0x000000000000000000000000000000000000BBB2",
          amount: "2000000000000000000",
          txHash: "0xabc",
          blockNumber: 77,
          logIndex: 2,
        },
      });
    render(<Security />);
    const heading = await screen.findByRole("heading", {
      name: "Last forced transfer",
    });
    const section = heading.closest("section") as HTMLElement;
    expect(within(section).getByText("2")).toBeInTheDocument();
    expect(within(section).getByText("77")).toBeInTheDocument();
  });
});

describe("Security ERC-7943 enforcement controls", () => {
  let wallet: ReturnType<typeof installFakeWallet>;
  const HOLDER = "0x000000000000000000000000000000000000AAA1";
  const RECIPIENT = "0x000000000000000000000000000000000000BBB2";
  const AS_ADMIN = {
    ...DEPLOYED,
    decimals: 18,
    roles: { DEFAULT_ADMIN_ROLE: [ADMIN] },
  };

  beforeEach(() => {
    wallet = installFakeWallet({ account: ADMIN, chainId: 31337 });
    vi.mocked(sendSetFrozenTokens).mockClear().mockResolvedValue("0xhash");
    vi.mocked(sendForcedTransfer).mockClear().mockResolvedValue("0xhash");
    vi.mocked(api.getProject).mockReset().mockResolvedValue(AS_ADMIN);
    vi.mocked(api.getEnforcement).mockReset().mockResolvedValue(NO_ENFORCEMENT);
  });

  afterEach(() => {
    wallet.uninstall();
  });

  async function freezeForm() {
    const account = await screen.findByLabelText("Holder address");
    const amount = screen.getByLabelText(/absolute frozen amount/i);
    const submit = screen.getByRole("button", { name: "Set frozen amount" });
    return { account, amount, submit };
  }

  async function forcedForm() {
    const holder = await screen.findByLabelText(/From \(holder\)/);
    const recipient = screen.getByLabelText(/To \(recipient\)/);
    const amount = screen.getByLabelText(/Amount \(whole tokens\)/);
    return { holder, recipient, amount };
  }

  it("hides both controls behind a notice when the wallet is not the admin", async () => {
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({ ...DEPLOYED, roles: {} });
    renderWithWallet(<Security />, { connected: true });

    const freeze = (
      await screen.findByRole("heading", { name: "Freeze tokens" })
    ).closest("section") as HTMLElement;
    expect(within(freeze).getByText("DEFAULT_ADMIN_ROLE")).toBeInTheDocument();
    expect(
      within(freeze).queryByRole("button", { name: "Set frozen amount" }),
    ).toBeNull();

    const forced = (
      await screen.findByRole("heading", { name: "Forced transfer" })
    ).closest("section") as HTMLElement;
    expect(
      within(forced).queryByRole("button", { name: "Force transfer" }),
    ).toBeNull();
  });

  it("broadcasts setFrozenTokens with exact token units", async () => {
    renderWithWallet(<Security />, { connected: true });
    const { account, amount, submit } = await freezeForm();

    fireEvent.change(account, { target: { value: HOLDER } });
    fireEvent.change(amount, { target: { value: "1.5" } });
    fireEvent.click(submit);

    await waitFor(() =>
      // 1.5 whole tokens at 18 decimals, as an exact bigint.
      expect(sendSetFrozenTokens).toHaveBeenCalledWith(
        31337,
        ADMIN,
        DEPLOYED.addresses.token,
        HOLDER,
        1500000000000000000n,
      ),
    );
  });

  it("treats a zero amount as a release rather than rejecting it", async () => {
    renderWithWallet(<Security />, { connected: true });
    const { account, amount, submit } = await freezeForm();

    fireEvent.change(account, { target: { value: HOLDER } });
    fireEvent.change(amount, { target: { value: "0" } });
    fireEvent.click(submit);

    await waitFor(() =>
      expect(sendSetFrozenTokens).toHaveBeenCalledWith(
        31337,
        ADMIN,
        DEPLOYED.addresses.token,
        HOLDER,
        0n,
      ),
    );
    expect(
      await screen.findByText(/released every frozen token/i),
    ).toBeInTheDocument();
  });

  it("says the freeze is absolute rather than additive", async () => {
    renderWithWallet(<Security />, { connected: true });
    const heading = await screen.findByRole("heading", {
      name: "Freeze tokens",
    });
    const section = heading.closest("section") as HTMLElement;
    expect(
      within(section).getByText(/replacing the previous hold/i),
    ).toBeInTheDocument();
  });

  it("rejects a malformed address, an over-precise amount, and a system address before signing", async () => {
    renderWithWallet(<Security />, { connected: true });
    const { account, amount, submit } = await freezeForm();

    fireEvent.change(account, { target: { value: "0xnope" } });
    fireEvent.change(amount, { target: { value: "1" } });
    fireEvent.click(submit);
    expect(
      await screen.findByText(/is not a valid address/i),
    ).toBeInTheDocument();

    // Reject precision beyond the token's 18 decimals
    fireEvent.change(account, { target: { value: HOLDER } });
    fireEvent.change(amount, { target: { value: "1.0000000000000000001" } });
    fireEvent.click(submit);
    expect(
      await screen.findByText(/is not a valid amount/i),
    ).toBeInTheDocument();

    fireEvent.change(account, { target: { value: DEPLOYED.addresses.vault } });
    fireEvent.change(amount, { target: { value: "1" } });
    fireEvent.click(submit);
    expect(await screen.findByText(/can never be frozen/i)).toBeInTheDocument();

    expect(sendSetFrozenTokens).not.toHaveBeenCalled();
  });

  it("confirms what a forced transfer bypasses before broadcasting it", async () => {
    renderWithWallet(<Security />, { connected: true });
    const { holder, recipient, amount } = await forcedForm();

    fireEvent.change(holder, { target: { value: HOLDER } });
    fireEvent.change(recipient, { target: { value: RECIPIENT } });
    fireEvent.change(amount, { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Force transfer" }));

    // Require warning review and confirmation before signing
    expect(
      await screen.findByText(
        /Bypasses pause and blocked\/expired holder status/i,
      ),
    ).toBeInTheDocument();
    expect(sendForcedTransfer).not.toHaveBeenCalled();

    fireEvent.click(
      screen.getByRole("button", { name: "Confirm forced transfer" }),
    );
    await waitFor(() =>
      expect(sendForcedTransfer).toHaveBeenCalledWith(
        31337,
        ADMIN,
        DEPLOYED.addresses.token,
        HOLDER,
        RECIPIENT,
        2000000000000000000n,
      ),
    );
  });

  it("refuses to seize from the Vault or the redemption escrow", async () => {
    renderWithWallet(<Security />, { connected: true });
    const { holder, recipient, amount } = await forcedForm();

    for (const system of [
      DEPLOYED.addresses.vault,
      DEPLOYED.addresses.redemptionEscrow,
    ]) {
      fireEvent.change(holder, { target: { value: system } });
      fireEvent.change(recipient, { target: { value: RECIPIENT } });
      fireEvent.change(amount, { target: { value: "1" } });
      fireEvent.click(screen.getByRole("button", { name: "Force transfer" }));
      expect(
        await screen.findByText(/cannot be seizure sources/i),
      ).toBeInTheDocument();
    }
    expect(sendForcedTransfer).not.toHaveBeenCalled();
  });

  it("tells the operator to pause first, since both calls are front-runnable", async () => {
    renderWithWallet(<Security />, { connected: true });
    const freeze = (
      await screen.findByRole("heading", { name: "Freeze tokens" })
    ).closest("section") as HTMLElement;
    const forced = (
      await screen.findByRole("heading", { name: "Forced transfer" })
    ).closest("section") as HTMLElement;

    for (const section of [freeze, forced]) {
      expect(
        within(section).getByText(/see this transaction in the mempool/i),
      ).toBeInTheDocument();
      expect(
        within(section).getByText(/pause the project/i),
      ).toBeInTheDocument();
    }
  });

  it("rejects a forced transfer to the same address", async () => {
    renderWithWallet(<Security />, { connected: true });
    const { holder, recipient, amount } = await forcedForm();

    fireEvent.change(holder, { target: { value: HOLDER } });
    fireEvent.change(recipient, { target: { value: HOLDER.toLowerCase() } });
    fireEvent.change(amount, { target: { value: "1" } });
    fireEvent.click(screen.getByRole("button", { name: "Force transfer" }));

    expect(
      await screen.findByText(/sender and recipient must differ/i),
    ).toBeInTheDocument();
    expect(sendForcedTransfer).not.toHaveBeenCalled();
  });

  it("echoes the exact values being seized and freezes the inputs while confirming", async () => {
    renderWithWallet(<Security />, { connected: true });
    const { holder, recipient, amount } = await forcedForm();

    fireEvent.change(holder, { target: { value: HOLDER } });
    fireEvent.change(recipient, { target: { value: RECIPIENT } });
    fireEvent.change(amount, { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Force transfer" }));

    // Confirm full addresses so transcription errors remain visible
    const panel = (await screen.findByRole("alert")) as HTMLElement;
    expect(within(panel).getByText(HOLDER)).toBeInTheDocument();
    expect(within(panel).getByText(RECIPIENT)).toBeInTheDocument();
    expect(
      within(panel).getByText(/2000000000000000000 minimal units/),
    ).toBeInTheDocument();

    // Keep submission bound to reviewed values despite input changes
    expect((recipient as HTMLInputElement).disabled).toBe(true);
    fireEvent.change(recipient, {
      target: { value: "0x000000000000000000000000000000000000dEaD" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Confirm forced transfer" }),
    );
    await waitFor(() =>
      expect(sendForcedTransfer).toHaveBeenCalledWith(
        31337,
        ADMIN,
        DEPLOYED.addresses.token,
        HOLDER,
        RECIPIENT,
        2000000000000000000n,
      ),
    );
  });

  it("reports a mined-but-reverted seizure as a failure, not a success", async () => {
    vi.mocked(waitForTxReceipt).mockRejectedValueOnce(
      new Error(
        "Transaction 0xhash was mined but reverted on-chain. Contract state is unchanged.",
      ),
    );
    renderWithWallet(<Security />, { connected: true });
    const { holder, recipient, amount } = await forcedForm();

    fireEvent.change(holder, { target: { value: HOLDER } });
    fireEvent.change(recipient, { target: { value: RECIPIENT } });
    fireEvent.change(amount, { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Force transfer" }));
    fireEvent.click(
      screen.getByRole("button", { name: "Confirm forced transfer" }),
    );

    expect(await screen.findByText(/reverted on-chain/i)).toBeInTheDocument();
    expect(screen.queryByText(/^Moved 2 from/)).toBeNull();
  });

  it("names the contract's own revert reason when the wallet returns one", async () => {
    // SystemAddressCannotBeSeized(vault), as the token would encode it.
    const revert = encodeErrorResult({
      abi: rwaTokenErrorsAbi,
      errorName: "SystemAddressCannotBeSeized",
      args: [DEPLOYED.addresses.vault as `0x${string}`],
    });
    vi.mocked(sendSetFrozenTokens).mockRejectedValueOnce(
      Object.assign(new Error("execution reverted"), {
        cause: { data: revert },
      }),
    );
    renderWithWallet(<Security />, { connected: true });
    const { account, amount, submit } = await freezeForm();

    fireEvent.change(account, { target: { value: HOLDER } });
    fireEvent.change(amount, { target: { value: "1" } });
    fireEvent.click(submit);

    expect(
      await screen.findByText(/cannot be a seizure source/i),
    ).toBeInTheDocument();
  });

  it("surfaces a rejected or reverted transaction instead of reporting success", async () => {
    vi.mocked(sendSetFrozenTokens).mockRejectedValueOnce(
      new Error("User rejected the request."),
    );
    renderWithWallet(<Security />, { connected: true });
    const { account, amount, submit } = await freezeForm();

    fireEvent.change(account, { target: { value: HOLDER } });
    fireEvent.change(amount, { target: { value: "1" } });
    fireEvent.click(submit);

    expect(
      await screen.findByText("User rejected the request."),
    ).toBeInTheDocument();
    expect(screen.queryByText(/frozen amount for/i)).toBeNull();
  });

  it("reloads the indexed state after a confirmed freeze", async () => {
    renderWithWallet(<Security />, { connected: true });
    const { account, amount, submit } = await freezeForm();
    const callsBefore = vi.mocked(api.getProject).mock.calls.length;

    fireEvent.change(account, { target: { value: HOLDER } });
    fireEvent.change(amount, { target: { value: "1" } });
    fireEvent.click(submit);

    await waitFor(() =>
      expect(vi.mocked(api.getProject).mock.calls.length).toBeGreaterThan(
        callsBefore,
      ),
    );
  });
});

const DISTRIBUTOR = "0x0000000000000000000000000000000000000080";
const V3 = {
  ...DEPLOYED,
  version: "rwa-v3",
  strategyKind: "Fixed" as const,
  minPurchaseQuote: "25000000",
  addresses: { ...DEPLOYED.addresses, incomeDistributor: DISTRIBUTOR },
};
const ORACLE = {
  feed: "0x00000000000000000000000000000000000000fe",
  maxAge: 3600,
  purchaseSpreadBps: 50,
  redemptionSpreadBps: 75,
  feedDecimals: 8,
  available: true,
  price: "250000000",
  updatedAt: 1_800_000_000,
};

describe("Security rwa-v3 minimum purchase", () => {
  let wallet: ReturnType<typeof installFakeWallet>;

  beforeEach(() => {
    wallet = installFakeWallet({ account: ADMIN, chainId: 31337 });
    vi.mocked(api.getEnforcement).mockReset().mockResolvedValue(NO_ENFORCEMENT);
    vi.mocked(sendSetMinPurchaseQuote).mockClear().mockResolvedValue("0xhash");
    vi.mocked(exportSafeBatch).mockClear();
  });

  afterEach(() => {
    wallet.uninstall();
  });

  it("shows the current minimum scaled by quote decimals", async () => {
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({ ...V3, roles: {} });
    renderWithWallet(<Security />, { connected: true });

    expect(
      await screen.findByText(/Minimum purchase \(quote token\)/),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(priceValue(/Minimum purchase \(quote token\)/)).toBe("25"),
    );
  });

  it("shows None for a zero minimum", async () => {
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({ ...V3, minPurchaseQuote: "0", roles: {} });
    renderWithWallet(<Security />, { connected: true });
    await waitFor(() =>
      expect(priceValue(/Minimum purchase \(quote token\)/)).toBe("None"),
    );
  });

  it("broadcasts setMinPurchaseQuote to the Vault, scaled, when the wallet is Vault admin", async () => {
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({ ...V3, roles: { DEFAULT_ADMIN_ROLE: [ADMIN] } });
    renderWithWallet(<Security />, { connected: true });

    fireEvent.change(
      await screen.findByLabelText(/Minimum purchase \(whole quote-token/),
      { target: { value: "12.5" } },
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Set minimum purchase" }),
    );

    await waitFor(() =>
      expect(sendSetMinPurchaseQuote).toHaveBeenCalledWith(
        31337,
        ADMIN,
        V3.addresses.vault,
        12_500_000n,
      ),
    );
    // The result survives the project reload that follows a confirmed write
    expect(await screen.findByText("Minimum purchase updated.")).toBeVisible();
    await waitFor(() => expect(api.getProject).toHaveBeenCalledTimes(2));
    expect(screen.getByText("Minimum purchase updated.")).toBeVisible();
  });

  it("keeps the chain and quote decimals while the project reloads after a write", async () => {
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValueOnce({ ...V3, roles: { DEFAULT_ADMIN_ROLE: [ADMIN] } })
      .mockReturnValue(new Promise(() => {}) as never); // reloads stay pending
    renderWithWallet(<Security />, { connected: true });

    fireEvent.change(
      await screen.findByLabelText(/Minimum purchase \(whole quote-token/),
      { target: { value: "12.5" } },
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Set minimum purchase" }),
    );
    await screen.findByText("Minimum purchase updated.");
    await waitFor(() => expect(api.getProject).toHaveBeenCalledTimes(2));

    // a second write during the reload targets the project's chain with the same scaling
    expect(
      screen.getByText(/Current minimum:/).querySelector("strong")?.textContent,
    ).toBe("25");
    fireEvent.change(
      screen.getByLabelText(/Minimum purchase \(whole quote-token/),
      { target: { value: "20" } },
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Set minimum purchase" }),
    );
    await waitFor(() =>
      expect(sendSetMinPurchaseQuote).toHaveBeenLastCalledWith(
        31337,
        ADMIN,
        V3.addresses.vault,
        20_000_000n,
      ),
    );
  });

  it("hides the form from a wallet that lacks DEFAULT_ADMIN_ROLE on the Vault", async () => {
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({
        ...V3,
        roles: { DEFAULT_ADMIN_ROLE: [ADMIN] },
        rolesByContract: {
          vault: {
            DEFAULT_ADMIN_ROLE: ["0x00000000000000000000000000000000000000a1"],
          },
        },
      });
    renderWithWallet(<Security />, { connected: true });

    const heading = await screen.findByRole("heading", {
      name: "Minimum purchase",
    });
    const section = heading.closest("section") as HTMLElement;
    expect(
      within(section).queryByRole("button", { name: "Set minimum purchase" }),
    ).toBeNull();
    expect(within(section).getByText("DEFAULT_ADMIN_ROLE")).toBeInTheDocument();
  });

  it("saves a Safe batch holding the Vault call for a Safe admin", async () => {
    const SAFE = "0x00000000000000000000000000000000000000a1";
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({
        ...V3,
        roles: { DEFAULT_ADMIN_ROLE: [SAFE] },
        rolesByContract: { vault: { DEFAULT_ADMIN_ROLE: [SAFE] } },
      });
    renderWithWallet(<Security />, { connected: true });

    const heading = await screen.findByRole("heading", {
      name: "Minimum purchase",
    });
    const section = heading.closest("section") as HTMLElement;
    fireEvent.change(within(section).getByRole("combobox"), {
      target: { value: SAFE },
    });
    fireEvent.change(
      within(section).getByLabelText(/Minimum purchase \(whole quote-token/),
      { target: { value: "1" } },
    );
    fireEvent.click(
      within(section).getByRole("button", { name: "Set minimum purchase" }),
    );

    await waitFor(() => expect(exportSafeBatch).toHaveBeenCalled());
    const [chainId, safe, name, calls] =
      vi.mocked(exportSafeBatch).mock.calls[0];
    expect([chainId, safe, name]).toEqual([
      31337,
      SAFE,
      "Set minimum purchase",
    ]);
    expect(calls).toHaveLength(1);
    expect(calls[0].to).toBe(V3.addresses.vault);
    expect(sendSetMinPurchaseQuote).not.toHaveBeenCalled();
  });

  it("disables the action while paused, since the Vault would revert", async () => {
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({
        ...V3,
        paused: true,
        roles: { DEFAULT_ADMIN_ROLE: [ADMIN] },
      });
    renderWithWallet(<Security />, { connected: true });

    fireEvent.change(
      await screen.findByLabelText(/Minimum purchase \(whole quote-token/),
      { target: { value: "1" } },
    );
    expect(
      screen.getByRole("button", { name: "Set minimum purchase" }),
    ).toBeDisabled();
  });

  it("has no minimum section on an rwa-v2 project", async () => {
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({ ...DEPLOYED, roles: {} });
    renderWithWallet(<Security />, { connected: true });
    await screen.findByRole("heading", { name: "Update prices" });
    expect(
      screen.queryByRole("heading", { name: "Minimum purchase" }),
    ).toBeNull();
  });
});

describe("Security oracle strategy", () => {
  let wallet: ReturnType<typeof installFakeWallet>;

  beforeEach(() => {
    wallet = installFakeWallet({ account: ADMIN, chainId: 31337 });
    vi.mocked(api.getEnforcement).mockReset().mockResolvedValue(NO_ENFORCEMENT);
  });

  afterEach(() => {
    wallet.uninstall();
  });

  it("hides the price setters and shows the oracle parameters", async () => {
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({
        ...V3,
        strategyKind: "Oracle",
        oracle: {
          ...ORACLE,
          sequencerUptimeFeed: "0x00000000000000000000000000000000000000fd",
          gracePeriod: 1800,
        },
        roles: { PRICER_ROLE: [ADMIN] },
      });
    renderWithWallet(<Security />, { connected: true });

    const oracleHeading = await screen.findByRole("heading", {
      name: "Price oracle",
    });
    const card = oracleHeading.closest("section") as HTMLElement;
    expect(within(card).getByText("Available")).toBeInTheDocument();
    expect(within(card).getByText("3600 s")).toBeInTheDocument();
    expect(within(card).getByText("50 bps")).toBeInTheDocument();
    expect(within(card).getByText("75 bps")).toBeInTheDocument();
    expect(within(card).getByText("1800 s")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Update prices" })).toBeNull();
    expect(screen.queryByLabelText(/Purchase price \(whole/)).toBeNull();
  });

  it("names the failure when the feed is unavailable", async () => {
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({
        ...V3,
        strategyKind: "Oracle",
        oracle: {
          ...ORACLE,
          available: false,
          price: undefined,
          error: "StalePrice",
        },
        roles: {},
      });
    renderWithWallet(<Security />, { connected: true });

    const card = (
      await screen.findByRole("heading", { name: "Price oracle" })
    ).closest("section") as HTMLElement;
    expect(within(card).getByText("StalePrice")).toBeInTheDocument();
    expect(
      await screen.findByText(/price feed is unavailable \(StalePrice\)/),
    ).toBeInTheDocument();
  });

  it("starts an admin transfer on the IncomeDistributor and skips the Oracle strategy", async () => {
    const SAFE = "0x00000000000000000000000000000000000000a1";
    vi.mocked(exportSafeBatch).mockClear();
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({
        ...V3,
        strategyKind: "Oracle",
        oracle: ORACLE,
        roles: { DEFAULT_ADMIN_ROLE: [SAFE] },
        rolesByContract: {
          token: { DEFAULT_ADMIN_ROLE: [SAFE] },
          compliance: { DEFAULT_ADMIN_ROLE: [SAFE] },
          supplyController: { DEFAULT_ADMIN_ROLE: [SAFE] },
          vault: { DEFAULT_ADMIN_ROLE: [SAFE] },
          redemptionEscrow: { DEFAULT_ADMIN_ROLE: [SAFE] },
          incomeDistributor: { DEFAULT_ADMIN_ROLE: [SAFE] },
        },
      });
    renderWithWallet(<Security />, { connected: true });

    const heading = await screen.findByRole("heading", {
      name: "Manage roles & admin",
    });
    const section = heading.closest("section") as HTMLElement;
    // The second gate (transfer admin) offers the Safe holding admin on every contract
    const selects = await within(section).findAllByRole("combobox");
    fireEvent.change(selects[selects.length - 1], { target: { value: SAFE } });
    fireEvent.change(within(section).getByLabelText(/New admin address/), {
      target: { value: "0x00000000000000000000000000000000000000b2" },
    });
    fireEvent.click(
      within(section).getByRole("button", { name: "Begin admin transfer" }),
    );

    await waitFor(() => expect(exportSafeBatch).toHaveBeenCalled());
    const calls = vi.mocked(exportSafeBatch).mock.calls[0][3];
    const targets = calls.map((c) => c.to);
    expect(targets).toContain(DISTRIBUTOR);
    expect(targets).not.toContain(V3.addresses.strategy);
    expect(targets).toHaveLength(6);
  });

  it("warns on the admin handover when the strategy kind is unknown", async () => {
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({
        ...V3,
        strategyKind: "Unknown",
        roles: { DEFAULT_ADMIN_ROLE: [ADMIN] },
      });
    renderWithWallet(<Security />, { connected: true });

    const heading = await screen.findByRole("heading", {
      name: "Manage roles & admin",
    });
    const section = heading.closest("section") as HTMLElement;
    expect(
      await within(section).findByText(/pricing strategy contract is excluded/),
    ).toBeInTheDocument();
  });
});
