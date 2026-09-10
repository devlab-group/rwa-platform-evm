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
import { encodeErrorResult } from "viem";
import { rwaTokenErrorsAbi } from "../../lib/abis";
import {
  readErc20Decimals,
  sendAcceptAdminTransfer,
  sendBeginAdminTransfer,
  sendForcedTransfer,
  sendRoleChange,
  sendSetFrozenTokens,
  sendSetPaused,
  sendSetStrategyPrice,
  waitForTxReceipt,
} from "../../lib/wallet";
import { roleHash, ROLES } from "../../lib/roles";
import { installFakeWallet, renderWithWallet } from "../../test/walletHarness";

// The live prices are quote-token MINIMAL units and must render as HUMAN whole
// units scaled by the QUOTE token's decimals (6 here). "2000000" -> "2".
vi.mock("../../lib/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/client")>();
  return {
    ...actual,
    api: { ...actual.api, getProject: vi.fn(), getEnforcement: vi.fn() },
  };
});

// readErc20Decimals is stubbed; the admin write helpers are spied so the
// role-gated action tests can assert the exact broadcast without a chain.
vi.mock("../../lib/wallet", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/wallet")>();
  return {
    ...actual,
    readErc20Decimals: vi.fn().mockResolvedValue(6),
    sendSetPaused: vi.fn().mockResolvedValue("0xhash"),
    sendSetStrategyPrice: vi.fn().mockResolvedValue("0xhash"),
    sendRoleChange: vi.fn().mockResolvedValue("0xhash"),
    sendSetFrozenTokens: vi.fn().mockResolvedValue("0xhash"),
    sendForcedTransfer: vi.fn().mockResolvedValue("0xhash"),
    sendBeginAdminTransfer: vi.fn().mockResolvedValue("0xhash"),
    sendAcceptAdminTransfer: vi.fn().mockResolvedValue("0xhash"),
    waitForTxReceipt: vi.fn().mockResolvedValue(undefined),
    // describeWalletError is the real one: it is the thing under test in the
    // revert-reason case, and it falls back to err.message for everything else.
  };
});

/** Default for tests that do not care about enforcement state. */
const NO_ENFORCEMENT = { securityStale: false };

const BASE = {
  chainId: 31337,
  addresses: { quoteToken: "0x0000000000000000000000000000000000000002" },
  purchasePricePerWholeToken: "2000000",
  redemptionPricePerWholeToken: "5500000",
};

/** The <dd> value of the row whose <dt> matches `label`. */
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

    // 2000000 / 5500000 minimal at 6 decimals -> "2" / "5.5" whole units.
    await waitFor(() =>
      expect(priceValue(/current purchase price/i)).toBe("2"),
    );
    expect(priceValue(/current redemption price/i)).toBe("5.5");
    // Server supplied quoteDecimals, so no on-chain round trip.
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
          /no on-chain prices reported/i,
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
    // Connected wallet holds no roles → the pause action is gated out.
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
    // The gate renders a notice naming the required role instead of the button.
    expect(within(section).getByText("PAUSER_ROLE")).toBeInTheDocument();
    expect(
      within(section).queryByRole("button", { name: "Pause trading" }),
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

  // The six governance contracts an admin transfer is accepted on, in the order
  // adminTransferTargets() resolves them from DEPLOYED.addresses.
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

    // Wait for the page to settle (the Status card renders once loaded).
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
          // Beyond float64's exact integer range: must render digit-for-digit.
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
      within(section).getByText(/does not add to an existing hold/i),
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

    // 19 fractional digits on an 18-decimal token would silently round.
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

    // Nothing is signed until the operator reads the warning and confirms.
    expect(
      await screen.findByText(/even while trading is paused/i),
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
        await screen.findByText(/cannot be seized from/i),
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
        within(section).getByText(/visible in the mempool/i),
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

    // The confirmation names the parties in full, not shortened, since a
    // mistyped-but-well-formed address is the mistake it exists to catch.
    const panel = (await screen.findByRole("alert")) as HTMLElement;
    expect(within(panel).getByText(HOLDER)).toBeInTheDocument();
    expect(within(panel).getByText(RECIPIENT)).toBeInTheDocument();
    expect(
      within(panel).getByText(/2000000000000000000 minimal units/),
    ).toBeInTheDocument();

    // An edit behind the open panel cannot change what gets sent.
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
        "Transaction 0xhash was mined but reverted on-chain. Nothing changed.",
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
      await screen.findByText(/can never be seized from/i),
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
