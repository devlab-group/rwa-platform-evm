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
import {
  readErc20Decimals,
  sendAcceptAdminTransfer,
  sendBeginAdminTransfer,
  sendRoleChange,
  sendSetPaused,
  sendSetStrategyPrice,
} from "../../lib/wallet";
import { roleHash, ROLES } from "../../lib/roles";
import { installFakeWallet, renderWithWallet } from "../../test/walletHarness";

// The live prices are quote-token MINIMAL units and must render as HUMAN whole
// units scaled by the QUOTE token's decimals (6 here). "2000000" -> "2".
vi.mock("../../lib/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/client")>();
  return {
    ...actual,
    api: { ...actual.api, getProject: vi.fn() },
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
    sendBeginAdminTransfer: vi.fn().mockResolvedValue("0xhash"),
    sendAcceptAdminTransfer: vi.fn().mockResolvedValue("0xhash"),
    waitForTxReceipt: vi.fn().mockResolvedValue(undefined),
  };
});

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
    vi.mocked(api.getProject).mockReset().mockResolvedValue({
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
      expect(sendAcceptAdminTransfer).toHaveBeenCalledWith(31337, ADMIN, target);
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
    expect(within(section).getByText(/admin transfer is pending to/i)).toBeTruthy();
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
