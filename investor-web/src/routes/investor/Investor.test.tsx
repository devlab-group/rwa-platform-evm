import {
  act,
  fireEvent,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { installFakeWallet, renderWithWallet } from "../../test/walletHarness";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Investor } from "./Investor";
import { api } from "../../lib/client";
import { clearWalletSession, setWalletSession } from "../../lib/walletSession";
import {
  readErc20Allowance,
  readPreviewBuy,
  readPreviewRedeem,
  sendBuy,
  sendCancelRedemption,
  sendClaimRedemption,
  sendErc20Approve,
  sendRequestRedemption,
  waitForTxReceipt,
} from "../../lib/wallet";

// Forms convert whole-unit input to minimal units before contract calls;
// the six-decimal fixture maps "1" to "1000000"
vi.mock("../../lib/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/client")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      getProject: vi.fn(),
      getMyWalletStatus: vi.fn(),
      // The connected wallet's redemption and transaction lists
      listRedemptions: vi.fn(),
      listTransactions: vi.fn(),
    },
  };
});

// Stub decimals/previews and submissions for chain-free tests; mined receipt plus sufficient
// allowance enables Buy
vi.mock("../../lib/wallet", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/wallet")>();
  return {
    ...actual,
    readErc20Decimals: vi.fn().mockResolvedValue(6),
    readPreviewBuy: vi.fn().mockResolvedValue(2000000n),
    readPreviewRedeem: vi.fn().mockResolvedValue(5000000n),
    sendBuy: vi.fn().mockResolvedValue("0xhash"),
    sendRequestRedemption: vi.fn().mockResolvedValue("0xhash"),
    sendClaimRedemption: vi.fn().mockResolvedValue("0xhash"),
    sendCancelRedemption: vi.fn().mockResolvedValue("0xhash"),
    sendErc20Approve: vi.fn().mockResolvedValue("0xapprove"),
    waitForTxReceipt: vi.fn().mockResolvedValue(undefined),
    readErc20Allowance: vi.fn().mockResolvedValue(10n ** 30n),
    readErc20Balance: vi.fn().mockResolvedValue("100000000"),
  };
});

const PROJECT = {
  chainId: 31337,
  decimals: 6,
  tokenUnit: "RWA",
  addresses: {
    token: "0x0000000000000000000000000000000000000001",
    quoteToken: "0x0000000000000000000000000000000000000002",
    vault: "0x0000000000000000000000000000000000000003",
    redemptionEscrow: "0x0000000000000000000000000000000000000004",
  },
};

describe("Investor amount conversion", () => {
  beforeEach(() => {
    vi.mocked(api.getProject).mockReset().mockResolvedValue(PROJECT);
    vi.mocked(readPreviewBuy).mockClear().mockResolvedValue(2000000n);
    vi.mocked(readPreviewRedeem).mockClear().mockResolvedValue(5000000n);
    window.localStorage.clear();
  });

  it("prices the buy token amount in minimal units for the token's decimals", async () => {
    renderWithWallet(<Investor />);
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    fireEvent.change(screen.getByLabelText("RWA token amount (whole units)"), {
      target: { value: "1" },
    });
    fireEvent.click(
      screen.getAllByRole("button", { name: "Preview quote" })[0],
    );

    // Quote 1 whole unit as 1000000n on the project Vault/chain
    await waitFor(() =>
      expect(readPreviewBuy).toHaveBeenCalledWith(
        PROJECT.chainId,
        PROJECT.addresses.vault,
        1000000n,
      ),
    );
  });

  it("prices the redemption amount in minimal units, including fractions", async () => {
    renderWithWallet(<Investor />);
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    fireEvent.change(
      screen.getByLabelText("RWA amount to redeem (whole units)"),
      { target: { value: "2.5" } },
    );
    fireEvent.click(
      screen.getAllByRole("button", { name: "Preview quote" })[1],
    );

    await waitFor(() =>
      expect(readPreviewRedeem).toHaveBeenCalledWith(
        PROJECT.chainId,
        PROJECT.addresses.redemptionEscrow,
        2500000n,
      ),
    );
  });

  it("rejects an over-precise amount with a friendly error and sends nothing", async () => {
    renderWithWallet(<Investor />);
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    fireEvent.change(screen.getByLabelText("RWA token amount (whole units)"), {
      target: { value: "1.2345678" },
    });
    fireEvent.click(
      screen.getAllByRole("button", { name: "Preview quote" })[0],
    );

    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent('"1.2345678"'),
    );
    expect(readPreviewBuy).not.toHaveBeenCalled();
  });
});

const CONNECTED = "0x1111111111111111111111111111111111111111";

function makeRedemption(over: Record<string, unknown>) {
  const now = Math.floor(Date.now() / 1000);
  return {
    beneficiary: CONNECTED,
    rwaAmount: "1000000",
    quoteAmount: "2000000",
    createdAt: now,
    timeoutAt: now + 3600,
    beneficiaryAllowed: true,
    confirmations: 3,
    claimable: false,
    ...over,
  };
}

// Finds the row containing a redemption's ID
function rowById(id: string): HTMLElement {
  return screen.getByText(id).closest("tr") as HTMLElement;
}

describe("Investor redemptions list (connected wallet)", () => {
  let wallet: ReturnType<typeof installFakeWallet>;

  beforeEach(() => {
    wallet = installFakeWallet({ account: CONNECTED, chainId: 31337 });
    vi.mocked(api.getProject).mockReset().mockResolvedValue(PROJECT);
    vi.mocked(api.listTransactions)
      .mockReset()
      .mockResolvedValue({ items: [] });
    vi.mocked(sendClaimRedemption).mockClear().mockResolvedValue("0xhash");
    vi.mocked(sendCancelRedemption).mockClear().mockResolvedValue("0xhash");
  });

  afterEach(() => {
    wallet.uninstall();
  });

  it("shows Claim on a claimable request and Cancel on a timed-out Pending one, not vice-versa", async () => {
    const now = Math.floor(Date.now() / 1000);
    vi.mocked(api.listRedemptions)
      .mockReset()
      .mockResolvedValue({
        items: [
          makeRedemption({ id: "10", status: "Funded", claimable: true }),
          makeRedemption({
            id: "11",
            status: "Pending",
            timeoutAt: now - 3600,
          }),
        ],
      });

    renderWithWallet(<Investor />, { connected: true });

    // Filter to the connected wallet
    await waitFor(() =>
      expect(api.listRedemptions).toHaveBeenCalledWith(
        expect.objectContaining({ address: CONNECTED }),
      ),
    );
    await screen.findByText("10");

    const claimable = rowById("10");
    expect(
      within(claimable).getByRole("button", { name: "Claim (permissionless)" }),
    ).toBeInTheDocument();
    expect(
      within(claimable).queryByRole("button", {
        name: "Cancel (timeout elapsed)",
      }),
    ).toBeNull();

    const timedOut = rowById("11");
    expect(
      within(timedOut).getByRole("button", {
        name: "Cancel (timeout elapsed)",
      }),
    ).toBeInTheDocument();
    expect(
      within(timedOut).queryByRole("button", {
        name: "Claim (permissionless)",
      }),
    ).toBeNull();
  });

  it("offers no Claim/Cancel on a Funded-but-not-yet-claimable request", async () => {
    vi.mocked(api.listRedemptions)
      .mockReset()
      .mockResolvedValue({
        items: [
          makeRedemption({ id: "12", status: "Funded", claimable: false }),
        ],
      });

    renderWithWallet(<Investor />, { connected: true });
    await screen.findByText("12");

    const row = rowById("12");
    expect(
      within(row).queryByRole("button", { name: "Claim (permissionless)" }),
    ).toBeNull();
    expect(
      within(row).queryByRole("button", { name: "Cancel (timeout elapsed)" }),
    ).toBeNull();
  });

  // Cancel only Pending requests; offer permissionless claim for Funded ones once the server
  // marks them claimable. Claimed/Cancelled requests are final.
  it.each([
    ["Funded", false, false],
    ["Funded", true, true],
    ["Claimed", false, false],
    ["Cancelled", false, false],
  ])(
    "never offers Cancel on a %s request with an elapsed timeout (claimable: %s, Claim shown: %s)",
    async (status, claimable, claimShown) => {
      const now = Math.floor(Date.now() / 1000);
      vi.mocked(api.listRedemptions)
        .mockReset()
        .mockResolvedValue({
          items: [
            makeRedemption({
              id: "20",
              status,
              claimable,
              timeoutAt: now - 3600,
            }),
          ],
        });

      renderWithWallet(<Investor />, { connected: true });
      await screen.findByText("20");

      const row = rowById("20");
      expect(
        within(row).queryByRole("button", { name: "Cancel (timeout elapsed)" }),
      ).toBeNull();
      expect(
        within(row).queryByRole("button", {
          name: "Claim (permissionless)",
        }) !== null,
      ).toBe(claimShown);
    },
  );

  it("offers no Cancel on a Pending request without a timeout", async () => {
    vi.mocked(api.listRedemptions)
      .mockReset()
      .mockResolvedValue({
        items: [
          makeRedemption({ id: "21", status: "Pending", timeoutAt: undefined }),
        ],
      });

    renderWithWallet(<Investor />, { connected: true });
    await screen.findByText("21");

    expect(
      within(rowById("21")).queryByRole("button", {
        name: "Cancel (timeout elapsed)",
      }),
    ).toBeNull();
  });

  it("calls the claim sender with the escrow and the request id", async () => {
    vi.mocked(api.listRedemptions)
      .mockReset()
      .mockResolvedValue({
        items: [
          makeRedemption({ id: "10", status: "Funded", claimable: true }),
        ],
      });

    renderWithWallet(<Investor />, { connected: true });
    await screen.findByText("10");

    fireEvent.click(
      within(rowById("10")).getByRole("button", {
        name: "Claim (permissionless)",
      }),
    );

    await waitFor(() =>
      expect(sendClaimRedemption).toHaveBeenCalledWith(
        PROJECT.chainId,
        CONNECTED,
        PROJECT.addresses.redemptionEscrow,
        10n,
      ),
    );
    await screen.findByText(/Claim submitted:/);
  });

  // The timeout guard permits cancellation at createdAt + timeout, including that exact second
  describe("cancel at the timeout boundary", () => {
    const T = 1_900_000_000;

    beforeEach(() => {
      // Only Date is frozen, so the async rendering helpers keep working
      vi.useFakeTimers({ toFake: ["Date"], now: T * 1000 });
    });
    afterEach(() => {
      vi.useRealTimers();
    });

    it.each([
      { when: "one second before", timeoutAt: T + 1, offered: false },
      { when: "exactly at", timeoutAt: T, offered: true },
      { when: "one second after", timeoutAt: T - 1, offered: true },
    ])(
      "timeoutAt $when now: Cancel offered is $offered",
      async ({ timeoutAt, offered }) => {
        vi.mocked(api.listRedemptions)
          .mockReset()
          .mockResolvedValue({
            items: [makeRedemption({ id: "12", status: "Pending", timeoutAt })],
          });
        renderWithWallet(<Investor />, { connected: true });
        await screen.findByText("12");

        const cancel = within(rowById("12")).queryByRole("button", {
          name: "Cancel (timeout elapsed)",
        });
        expect(cancel !== null).toBe(offered);
      },
    );
  });

  it("calls the cancel sender with the escrow and the request id", async () => {
    const now = Math.floor(Date.now() / 1000);
    vi.mocked(api.listRedemptions)
      .mockReset()
      .mockResolvedValue({
        items: [
          makeRedemption({
            id: "11",
            status: "Pending",
            timeoutAt: now - 3600,
          }),
        ],
      });

    renderWithWallet(<Investor />, { connected: true });
    await screen.findByText("11");

    fireEvent.click(
      within(rowById("11")).getByRole("button", {
        name: "Cancel (timeout elapsed)",
      }),
    );

    await waitFor(() =>
      expect(sendCancelRedemption).toHaveBeenCalledWith(
        PROJECT.chainId,
        CONNECTED,
        PROJECT.addresses.redemptionEscrow,
        11n,
      ),
    );
    await screen.findByText(/Cancel submitted:/);
  });
});

// Check browser-encoded buys/requests target the project contracts with minimal-unit amounts,
// slippage bounds, connected recipient and a future deadline
describe("Investor buy and redemption request (client-encoded)", () => {
  let wallet: ReturnType<typeof installFakeWallet>;

  beforeEach(() => {
    wallet = installFakeWallet({ account: CONNECTED, chainId: 31337 });
    vi.mocked(api.getProject).mockReset().mockResolvedValue(PROJECT);
    vi.mocked(readPreviewBuy).mockClear().mockResolvedValue(2000000n);
    vi.mocked(readPreviewRedeem).mockClear().mockResolvedValue(5000000n);
    vi.mocked(api.listRedemptions).mockReset().mockResolvedValue({ items: [] });
    vi.mocked(api.listTransactions)
      .mockReset()
      .mockResolvedValue({ items: [] });
    vi.mocked(sendBuy).mockClear().mockResolvedValue("0xhash");
    vi.mocked(sendRequestRedemption).mockClear().mockResolvedValue("0xhash");
    vi.mocked(sendErc20Approve).mockClear().mockResolvedValue("0xapprove");
    vi.mocked(waitForTxReceipt).mockClear().mockResolvedValue(undefined);
    vi.mocked(readErc20Allowance)
      .mockClear()
      .mockResolvedValue(10n ** 30n);
  });

  afterEach(() => {
    wallet.uninstall();
  });

  it("buys with the quoted amount, the slippage-ceiled max spend and the connected recipient", async () => {
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    fireEvent.change(screen.getByLabelText("RWA token amount (whole units)"), {
      target: { value: "1" },
    });
    fireEvent.click(
      screen.getAllByRole("button", { name: "Preview quote" })[0],
    );

    fireEvent.click(
      await screen.findByRole("button", {
        name: "1. Approve quote token spend",
      }),
    );
    await screen.findByText(/Approval submitted:/);
    const buy = screen.getByRole("button", { name: "2. Buy" });
    await waitFor(() => expect(buy).toBeEnabled());
    fireEvent.click(buy);

    await waitFor(() => expect(sendBuy).toHaveBeenCalled());
    const [chainId, from, to, args] = vi.mocked(sendBuy).mock.calls[0];
    expect([chainId, from, to]).toEqual([
      PROJECT.chainId,
      CONNECTED,
      PROJECT.addresses.vault,
    ]);
    expect(args).toMatchObject({
      tokenAmount: 1000000n,
      // 2000000 quote + default 50 bps
      maxQuoteAmount: 2010000n,
      recipient: CONNECTED,
    });
    expect(args.deadline).toBeGreaterThan(
      BigInt(Math.floor(Date.now() / 1000)),
    );

    await screen.findByText(/Buy submitted:/);
  });

  it("disables Approve and Buy once the wallet leaves the project chain", async () => {
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());
    fireEvent.change(screen.getByLabelText("RWA token amount (whole units)"), {
      target: { value: "1" },
    });
    fireEvent.click(
      screen.getAllByRole("button", { name: "Preview quote" })[0],
    );
    const approve = await screen.findByRole("button", {
      name: "1. Approve quote token spend",
    });
    await waitFor(() => expect(approve).toBeEnabled());
    fireEvent.click(approve);
    await screen.findByText(/Approval submitted:/);
    const buy = screen.getByRole("button", { name: "2. Buy" });
    await waitFor(() => expect(buy).toBeEnabled());

    act(() => wallet.emitChainChanged(1));

    await waitFor(() => expect(buy).toBeDisabled());
    expect(approve).toBeDisabled();
  });

  it("disables Approve and Request redemption once the wallet leaves the project chain", async () => {
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());
    fireEvent.change(
      screen.getByLabelText("RWA amount to redeem (whole units)"),
      { target: { value: "2.5" } },
    );
    fireEvent.click(
      screen.getAllByRole("button", { name: "Preview quote" })[1],
    );
    const approve = await screen.findByRole("button", {
      name: "1. Approve RWA spend",
    });
    await waitFor(() => expect(approve).toBeEnabled());
    fireEvent.click(approve);
    await screen.findByText(/Approval submitted:/);
    const request = screen.getByRole("button", {
      name: "2. Request redemption",
    });
    await waitFor(() => expect(request).toBeEnabled());

    act(() => wallet.emitChainChanged(1));

    await waitFor(() => expect(request).toBeDisabled());
    expect(approve).toBeDisabled();
  });

  it("disables Claim and Cancel once the wallet leaves the project chain", async () => {
    const now = Math.floor(Date.now() / 1000);
    vi.mocked(api.listRedemptions)
      .mockReset()
      .mockResolvedValue({
        items: [
          makeRedemption({ id: "20", status: "Funded", claimable: true }),
          makeRedemption({ id: "21", status: "Pending", timeoutAt: now - 60 }),
        ],
      });
    renderWithWallet(<Investor />, { connected: true });
    await screen.findByText("20");
    const claim = within(rowById("20")).getByRole("button", {
      name: "Claim (permissionless)",
    });
    const cancel = within(rowById("21")).getByRole("button", {
      name: "Cancel (timeout elapsed)",
    });
    await waitFor(() => expect(claim).toBeEnabled());
    expect(cancel).toBeEnabled();

    act(() => wallet.emitChainChanged(1));

    await waitFor(() => expect(claim).toBeDisabled());
    expect(cancel).toBeDisabled();
  });

  it("requests a redemption with the minimal-unit amount and the slippage-floored min quote out", async () => {
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    fireEvent.change(
      screen.getByLabelText("RWA amount to redeem (whole units)"),
      { target: { value: "2.5" } },
    );
    fireEvent.click(
      screen.getAllByRole("button", { name: "Preview quote" })[1],
    );

    fireEvent.click(
      await screen.findByRole("button", { name: "1. Approve RWA spend" }),
    );
    await screen.findByText(/Approval submitted:/);
    const request = screen.getByRole("button", {
      name: "2. Request redemption",
    });
    await waitFor(() => expect(request).toBeEnabled());
    fireEvent.click(request);

    await waitFor(() => expect(sendRequestRedemption).toHaveBeenCalled());
    const [chainId, from, to, args] = vi.mocked(sendRequestRedemption).mock
      .calls[0];
    expect([chainId, from, to]).toEqual([
      PROJECT.chainId,
      CONNECTED,
      PROJECT.addresses.redemptionEscrow,
    ]);
    expect(args).toMatchObject({
      rwaAmount: 2500000n,
      // 5000000 quote - default 50 bps
      minQuoteOut: 4975000n,
    });
    expect(args.deadline).toBeGreaterThan(
      BigInt(Math.floor(Date.now() / 1000)),
    );

    await screen.findByText(/Request submitted:/);
  });

  it("keeps the redemption request disabled when the approval left too small an allowance", async () => {
    // A mined approval may leave insufficient allowance; keep Request disabled
    // until the allowance covers the amount
    vi.mocked(readErc20Allowance).mockResolvedValue(2499999n);

    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    fireEvent.change(
      screen.getByLabelText("RWA amount to redeem (whole units)"),
      { target: { value: "2.5" } },
    );
    fireEvent.click(
      screen.getAllByRole("button", { name: "Preview quote" })[1],
    );

    fireEvent.click(
      await screen.findByRole("button", { name: "1. Approve RWA spend" }),
    );
    await screen.findByText(/Approval submitted:/);

    const request = screen.getByRole("button", {
      name: "2. Request redemption",
    });
    await waitFor(() => expect(readErc20Allowance).toHaveBeenCalled());
    expect(request).toBeDisabled();
    expect(sendRequestRedemption).not.toHaveBeenCalled();
  });
});

describe("Investor frozen balance", () => {
  let wallet: ReturnType<typeof installFakeWallet>;

  beforeEach(() => {
    wallet = installFakeWallet({ account: CONNECTED, chainId: 31337 });
    vi.mocked(api.getProject).mockReset().mockResolvedValue(PROJECT);
    vi.mocked(api.listRedemptions).mockReset().mockResolvedValue({ items: [] });
    vi.mocked(api.listTransactions)
      .mockReset()
      .mockResolvedValue({ items: [] });
    setWalletSession(CONNECTED, {
      token: "session-token",
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
    });
  });

  afterEach(() => {
    wallet.uninstall();
    clearWalletSession(CONNECTED);
  });

  // Explain why frozen tokens appear in balance but cannot be transferred
  it("shows how much of the balance is actually spendable", async () => {
    vi.mocked(api.getMyWalletStatus).mockResolvedValue({
      address: CONNECTED,
      status: "Allowed",
      ownershipVerified: true,
      frozenTokens: "40000000", // 40 of the 100 held, at 6 decimals
    });
    renderWithWallet(<Investor />, { connected: true });

    fireEvent.click(
      await screen.findByRole("button", { name: "Read balance" }),
    );
    expect(
      await screen.findByText(/60 RWA of that is available/),
    ).toBeInTheDocument();
    expect(screen.getByText(/frozen the other/)).toBeInTheDocument();
    expect(screen.getByText(/40/)).toBeInTheDocument();
  });

  it("says nothing about freezing when the wallet has none", async () => {
    vi.mocked(api.getMyWalletStatus).mockResolvedValue({
      address: CONNECTED,
      status: "Allowed",
      ownershipVerified: true,
    });
    renderWithWallet(<Investor />, { connected: true });

    fireEvent.click(
      await screen.findByRole("button", { name: "Read balance" }),
    );
    await screen.findByText("100 RWA");
    expect(screen.queryByText(/available to send or redeem/)).toBeNull();
  });
});
