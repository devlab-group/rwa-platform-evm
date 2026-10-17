import { fireEvent, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { installFakeWallet, renderWithWallet } from "../../test/walletHarness";
import { Investor } from "./Investor";
import { api } from "../../lib/client";
import { clearWalletSession, setWalletSession } from "../../lib/walletSession";
import {
  sendCancelRedemption,
  sendClaimRedemption,
  sendIncomeClaim,
} from "../../lib/wallet";

// Server values are untrusted: a malformed amount or id must show an error, not blank the page
vi.mock("../../lib/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/client")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      getProject: vi.fn(),
      getMyWalletStatus: vi.fn(),
      listRedemptions: vi.fn(),
      listTransactions: vi.fn(),
      listIncomeClaims: vi.fn(),
    },
  };
});

vi.mock("../../lib/wallet", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/wallet")>();
  return {
    ...actual,
    readErc20Decimals: vi.fn().mockResolvedValue(6),
    readErc20Balance: vi.fn().mockResolvedValue("100000000"),
    sendIncomeClaim: vi.fn().mockResolvedValue("0xclaimhash"),
    sendClaimRedemption: vi.fn().mockResolvedValue("0xhash"),
    sendCancelRedemption: vi.fn().mockResolvedValue("0xhash"),
    waitForTxReceipt: vi.fn().mockResolvedValue(undefined),
  };
});

const CONNECTED = "0x1111111111111111111111111111111111111111";
const DISTRIBUTOR = "0x0000000000000000000000000000000000000005";
const PROJECT = {
  chainId: 31337,
  decimals: 6,
  quoteDecimals: 6,
  tokenUnit: "RWA",
  addresses: {
    token: "0x0000000000000000000000000000000000000001",
    quoteToken: "0x0000000000000000000000000000000000000002",
    vault: "0x0000000000000000000000000000000000000003",
    redemptionEscrow: "0x0000000000000000000000000000000000000004",
    incomeDistributor: DISTRIBUTOR,
  },
};

function claim(over: Record<string, unknown>) {
  return {
    distributionId: "3",
    account: CONNECTED,
    amount: "1500000",
    proof: [`0x${"ab".repeat(32)}`],
    merkleRoot: `0x${"cd".repeat(32)}`,
    claimDeadline: 4102444800,
    snapshotURI: "ipfs://bafy",
    status: "Claimable",
    ...over,
  };
}

function redemption(over: Record<string, unknown>) {
  const now = Math.floor(Date.now() / 1000);
  return {
    beneficiary: CONNECTED,
    rwaAmount: "1000000",
    quoteAmount: "2000000",
    createdAt: now - 7200,
    timeoutAt: now - 3600,
    beneficiaryAllowed: true,
    confirmations: 50,
    claimable: false,
    ...over,
  };
}

describe("Investor with malformed server values", () => {
  let wallet: ReturnType<typeof installFakeWallet>;

  beforeEach(() => {
    wallet = installFakeWallet({ account: CONNECTED, chainId: 31337 });
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue(PROJECT as never);
    vi.mocked(api.listRedemptions).mockReset().mockResolvedValue({ items: [] });
    vi.mocked(api.listTransactions)
      .mockReset()
      .mockResolvedValue({ items: [] });
    vi.mocked(api.listIncomeClaims).mockReset().mockResolvedValue([]);
    vi.mocked(sendIncomeClaim).mockClear();
    vi.mocked(sendClaimRedemption).mockClear();
    vi.mocked(sendCancelRedemption).mockClear();
    window.localStorage.clear();
  });

  afterEach(() => {
    wallet.uninstall();
    clearWalletSession(CONNECTED);
  });

  it.each([
    ["amount", { amount: "1.5e6" }],
    ["distribution id", { distributionId: "three" }],
  ])(
    "shows an error and sends nothing when an income claim has a malformed %s",
    async (_name, over) => {
      vi.mocked(api.listIncomeClaims).mockResolvedValue([claim(over)] as never);
      renderWithWallet(<Investor />, { connected: true });

      fireEvent.click(await screen.findByRole("button", { name: "Claim" }));
      fireEvent.click(
        await screen.findByRole("button", { name: "Confirm claim" }),
      );

      const alert = await screen.findByRole("alert");
      // The error names the offending server value
      expect(alert).toHaveTextContent(String(Object.values(over)[0]));
      expect(sendIncomeClaim).not.toHaveBeenCalled();
      // The page is still usable: the entitlement can be retried
      expect(screen.getByRole("heading", { name: "Income" })).toBeVisible();
      expect(
        screen.getByRole("button", { name: "Confirm claim" }),
      ).toBeEnabled();
    },
  );

  it("renders an income entitlement with a non-numeric amount instead of crashing", async () => {
    vi.mocked(api.listIncomeClaims).mockResolvedValue([
      claim({ amount: "lots" }),
    ] as never);
    renderWithWallet(<Investor />, { connected: true });

    const item = (await screen.findByText(/Distribution 3:/)).closest("li");
    expect(item).toHaveTextContent("lots");
    fireEvent.click(
      within(item as HTMLElement).getByRole("button", { name: "Claim" }),
    );
    expect(
      await screen.findByRole("region", { name: /Income claim preview/ }),
    ).toBeVisible();
  });

  it("shows an error and sends nothing when a claimable redemption has a malformed id", async () => {
    vi.mocked(api.listRedemptions).mockResolvedValue({
      items: [redemption({ id: "abc", status: "Funded", claimable: true })],
    });
    renderWithWallet(<Investor />, { connected: true });

    fireEvent.click(
      await screen.findByRole("button", { name: "Claim (permissionless)" }),
    );

    expect(await screen.findByRole("alert")).toHaveTextContent("abc");
    expect(sendClaimRedemption).not.toHaveBeenCalled();
  });

  it("shows an error and sends nothing when a timed-out redemption has a malformed id", async () => {
    vi.mocked(api.listRedemptions).mockResolvedValue({
      items: [redemption({ id: "1.5", status: "Pending" })],
    });
    renderWithWallet(<Investor />, { connected: true });

    fireEvent.click(
      await screen.findByRole("button", { name: "Cancel (timeout elapsed)" }),
    );

    expect(await screen.findByRole("alert")).toHaveTextContent("1.5");
    expect(sendCancelRedemption).not.toHaveBeenCalled();
  });

  it("renders redemption amounts that are not integers as received", async () => {
    vi.mocked(api.listRedemptions).mockResolvedValue({
      items: [
        redemption({
          id: "7",
          status: "Pending",
          timeoutAt: undefined,
          rwaAmount: "NaN",
          quoteAmount: "-",
        }),
      ],
    });
    renderWithWallet(<Investor />, { connected: true });

    const row = (await screen.findByText("7")).closest("tr") as HTMLElement;
    expect(within(row).getByText("NaN")).toBeVisible();
  });

  it("treats an unparseable frozen amount as zero and still shows the balance", async () => {
    setWalletSession(CONNECTED, {
      token: "session-token",
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
    });
    vi.mocked(api.getMyWalletStatus).mockResolvedValue({
      address: CONNECTED,
      status: "Allowed",
      ownershipVerified: true,
      frozenTokens: "forty",
    });
    renderWithWallet(<Investor />, { connected: true });

    fireEvent.click(
      await screen.findByRole("button", { name: "Read balance" }),
    );
    await screen.findByText("100 RWA");
    expect(screen.queryByText(/frozen the other/)).toBeNull();
  });
});
