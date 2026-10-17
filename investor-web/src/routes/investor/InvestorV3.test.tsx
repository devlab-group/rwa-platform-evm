import {
  act,
  fireEvent,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { encodeErrorResult } from "viem";
import { installFakeWallet, renderWithWallet } from "../../test/walletHarness";
import { Investor } from "./Investor";
import { api } from "../../lib/client";
import { incomeDistributorAbi, oracleErrorsAbi } from "../../lib/abis";
import {
  readPreviewBuy,
  sendIncomeClaim,
  waitForTxReceipt,
} from "../../lib/wallet";

vi.mock("../../lib/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/client")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      getProject: vi.fn(),
      getInventory: vi.fn(),
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
    readPreviewBuy: vi.fn().mockResolvedValue(2000000n),
    readPreviewRedeem: vi.fn().mockResolvedValue(5000000n),
    sendIncomeClaim: vi.fn().mockResolvedValue("0xclaimhash"),
    waitForTxReceipt: vi.fn().mockResolvedValue(undefined),
    readErc20Balance: vi.fn().mockResolvedValue("100000000"),
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
  },
};
const PROOF = [`0x${"ab".repeat(32)}`];
const CLAIMS = [
  {
    distributionId: "3",
    account: CONNECTED,
    amount: "1500000",
    proof: PROOF,
    merkleRoot: `0x${"cd".repeat(32)}`,
    claimDeadline: 4102444800,
    snapshotURI: "ipfs://bafy",
    status: "Claimable",
  },
  {
    distributionId: "2",
    account: CONNECTED,
    amount: "700000",
    proof: PROOF,
    merkleRoot: `0x${"cd".repeat(32)}`,
    claimDeadline: 1700000000,
    snapshotURI: "https://example.com/snap.json",
    status: "Claimed",
    claimTxHash: "0xdone",
  },
  {
    distributionId: "1",
    account: CONNECTED,
    amount: "100000",
    proof: PROOF,
    merkleRoot: `0x${"cd".repeat(32)}`,
    claimDeadline: 1600000000,
    snapshotURI: "javascript:alert(1)",
    status: "Expired",
  },
];

function fillAndPreviewBuy(value = "1") {
  fireEvent.change(screen.getByLabelText("RWA token amount (whole units)"), {
    target: { value },
  });
  fireEvent.click(screen.getAllByRole("button", { name: "Preview quote" })[0]);
}

describe("Investor rwa-v3 surfaces", () => {
  let wallet: ReturnType<typeof installFakeWallet>;

  beforeEach(() => {
    wallet = installFakeWallet({ account: CONNECTED, chainId: 31337 });
    vi.mocked(api.getProject).mockReset().mockResolvedValue(PROJECT);
    vi.mocked(api.getInventory)
      .mockReset()
      .mockResolvedValue({} as never);
    vi.mocked(api.listRedemptions).mockReset().mockResolvedValue({ items: [] });
    vi.mocked(api.listTransactions)
      .mockReset()
      .mockResolvedValue({ items: [] });
    vi.mocked(api.listIncomeClaims)
      .mockReset()
      .mockResolvedValue(CLAIMS as never);
    vi.mocked(readPreviewBuy).mockReset().mockResolvedValue(2000000n);
    vi.mocked(sendIncomeClaim).mockClear().mockResolvedValue("0xclaimhash");
    vi.mocked(waitForTxReceipt).mockClear().mockResolvedValue(undefined);
    window.localStorage.clear();
  });

  afterEach(() => {
    wallet.uninstall();
  });

  it("shows the minimum and refuses a quote below it without opening the wallet", async () => {
    vi.mocked(api.getProject).mockResolvedValue({
      ...PROJECT,
      minPurchaseQuote: "5000000",
    } as never);
    renderWithWallet(<Investor />, { connected: true });
    await screen.findByText(/Minimum purchase:/);
    expect(screen.getByText(/Minimum purchase:/)).toHaveTextContent("5");

    fillAndPreviewBuy();
    expect(
      await screen.findByText(/below the project minimum of 5/),
    ).toBeVisible();
    expect(
      screen.queryByRole("button", { name: "1. Approve quote token spend" }),
    ).toBeNull();
  });

  it("allows a quote at the minimum", async () => {
    vi.mocked(api.getProject).mockResolvedValue({
      ...PROJECT,
      minPurchaseQuote: "2000000",
    } as never);
    renderWithWallet(<Investor />, { connected: true });
    await screen.findByText(/Minimum purchase:/);
    fillAndPreviewBuy();
    expect(
      await screen.findByRole("button", {
        name: "1. Approve quote token spend",
      }),
    ).toBeInTheDocument();
  });

  it("says prices follow an oracle and when it updated", async () => {
    vi.mocked(api.getProject).mockResolvedValue({
      ...PROJECT,
      strategyKind: "Oracle",
      oracle: { available: true, updatedAt: 1700000000 },
    } as never);
    renderWithWallet(<Investor />);
    await screen.findByText(/Prices follow an oracle price feed/);
    expect(screen.getByText(/Last feed update/)).toBeVisible();
    expect(screen.queryByText(/Prices unavailable/)).toBeNull();
  });

  it("disables new buys and requests when the oracle is unavailable", async () => {
    vi.mocked(api.getProject).mockResolvedValue({
      ...PROJECT,
      strategyKind: "Oracle",
      oracle: { available: false, error: "StalePrice", updatedAt: 1 },
    } as never);
    renderWithWallet(<Investor />, { connected: true });
    await screen.findByText(
      /Prices unavailable: the price feed has not updated/,
    );
    fireEvent.change(screen.getByLabelText("RWA token amount (whole units)"), {
      target: { value: "1" },
    });
    for (const b of screen.getAllByRole("button", { name: "Preview quote" }))
      expect(b).toBeDisabled();
  });

  it("disables new buys on an inventory priceError", async () => {
    vi.mocked(api.getProject).mockResolvedValue({
      ...PROJECT,
      strategyKind: "Oracle",
      oracle: { available: true },
    } as never);
    vi.mocked(api.getInventory).mockResolvedValue({
      priceError: "SequencerDown",
    } as never);
    renderWithWallet(<Investor />);
    await screen.findByText(/the L2 sequencer is down/);
    fireEvent.change(screen.getByLabelText("RWA token amount (whole units)"), {
      target: { value: "1" },
    });
    expect(
      screen.getAllByRole("button", { name: "Preview quote" })[0],
    ).toBeDisabled();
  });

  it("shows prices unavailable when a preview reverts with an oracle error", async () => {
    vi.mocked(readPreviewBuy).mockRejectedValue({
      data: encodeErrorResult({
        abi: oracleErrorsAbi,
        errorName: "InvalidPrice",
        args: [0n, 1n],
      }),
    });
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());
    fillAndPreviewBuy();
    expect(
      await screen.findByText(
        /Prices unavailable: the price feed returned an invalid/,
      ),
    ).toBeVisible();
  });

  it("hides income without a distributor and shows no request without a wallet", async () => {
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());
    expect(screen.queryByRole("heading", { name: "Income" })).toBeNull();
    expect(api.listIncomeClaims).not.toHaveBeenCalled();
  });

  it("lists claims, links only https snapshots and claims with the proof", async () => {
    vi.mocked(api.getProject).mockResolvedValue({
      ...PROJECT,
      addresses: { ...PROJECT.addresses, incomeDistributor: DISTRIBUTOR },
    } as never);
    renderWithWallet(<Investor />, { connected: true });
    await screen.findByText(/Distribution 3:/);
    expect(api.listIncomeClaims).toHaveBeenCalledWith(
      CONNECTED,
      expect.anything(),
    );
    expect(
      screen.getByRole("link", { name: "https://example.com/snap.json" }),
    ).toBeVisible();
    expect(
      screen.queryByRole("link", { name: "javascript:alert(1)" }),
    ).toBeNull();
    expect(screen.getByText("javascript:alert(1)")).toBeVisible();
    expect(screen.getByText("0xdone")).toBeVisible();
    // Only the claimable entry offers Claim
    expect(screen.getAllByRole("button", { name: "Claim" })).toHaveLength(1);

    fireEvent.click(screen.getByRole("button", { name: "Claim" }));
    fireEvent.click(
      await screen.findByRole("button", { name: "Confirm claim" }),
    );
    await waitFor(() => expect(sendIncomeClaim).toHaveBeenCalled());
    expect(vi.mocked(sendIncomeClaim).mock.calls[0]).toEqual([
      31337,
      CONNECTED,
      DISTRIBUTOR,
      3n,
      1500000n,
      PROOF,
    ]);
    await waitFor(() => expect(api.listIncomeClaims).toHaveBeenCalledTimes(2));
  });

  it("decodes a claim revert", async () => {
    vi.mocked(api.getProject).mockResolvedValue({
      ...PROJECT,
      addresses: { ...PROJECT.addresses, incomeDistributor: DISTRIBUTOR },
    } as never);
    vi.mocked(sendIncomeClaim).mockRejectedValue({
      data: encodeErrorResult({
        abi: incomeDistributorAbi,
        errorName: "InvalidProof",
      }),
    });
    renderWithWallet(<Investor />, { connected: true });
    fireEvent.click(await screen.findByRole("button", { name: "Claim" }));
    fireEvent.click(
      await screen.findByRole("button", { name: "Confirm claim" }),
    );
    expect(await screen.findByText(/proof does not match/)).toBeVisible();
  });

  it("shows entitlements in whole quote units once the project loads", async () => {
    vi.mocked(api.getProject).mockResolvedValue({
      ...PROJECT,
      addresses: { ...PROJECT.addresses, incomeDistributor: DISTRIBUTOR },
    } as never);
    renderWithWallet(<Investor />, { connected: true });
    expect(await screen.findByText(/Distribution 3: 1\.5 quote/)).toBeVisible();
  });

  it("does not use claim wording for a shared selector on a buy", async () => {
    vi.mocked(readPreviewBuy).mockRejectedValue({
      data: encodeErrorResult({
        abi: incomeDistributorAbi,
        errorName: "ProjectPaused",
      }),
    });
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());
    fillAndPreviewBuy();
    await waitFor(() => expect(readPreviewBuy).toHaveBeenCalled());
    expect(screen.queryByText(/claims resume/)).toBeNull();
  });

  it("re-enables previews after the feed recovers without a reload", async () => {
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValueOnce({
        ...PROJECT,
        strategyKind: "Oracle",
        oracle: { available: false, error: "StalePrice" },
      } as never)
      .mockResolvedValue({
        ...PROJECT,
        strategyKind: "Oracle",
        oracle: { available: true },
      } as never);
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      renderWithWallet(<Investor />, { connected: true });
      await screen.findByText(/Prices unavailable/);
      await act(() => vi.advanceTimersByTimeAsync(15_000));
      await waitFor(() =>
        expect(screen.queryByText(/Prices unavailable/)).toBeNull(),
      );
    } finally {
      vi.useRealTimers();
    }
  });

  // Buy and Request must follow feed recovery with the notice and Preview, without a reload
  it.each([
    {
      side: "buy",
      label: "RWA token amount (whole units)",
      approve: "1. Approve quote token spend",
    },
    {
      side: "redemption",
      label: "RWA amount to redeem (whole units)",
      approve: "1. Approve RWA spend",
    },
  ])(
    "enables the $side approval after the feed recovers without a reload",
    async ({ label, approve }) => {
      vi.mocked(api.getProject)
        .mockReset()
        .mockResolvedValueOnce({
          ...PROJECT,
          strategyKind: "Oracle",
          oracle: { available: false, error: "StalePrice" },
        } as never)
        .mockResolvedValue({
          ...PROJECT,
          strategyKind: "Oracle",
          oracle: { available: true },
        } as never);
      vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
      try {
        renderWithWallet(<Investor />, { connected: true });
        await screen.findByText(/Prices unavailable/);
        await act(() => vi.advanceTimersByTimeAsync(15_000));
        await waitFor(() =>
          expect(screen.queryByText(/Prices unavailable/)).toBeNull(),
        );
      } finally {
        vi.useRealTimers();
      }
      const input = screen.getByLabelText(label);
      fireEvent.change(input, { target: { value: "1" } });
      fireEvent.click(
        within(input.closest("section") as HTMLElement).getByRole("button", {
          name: "Preview quote",
        }),
      );
      expect(
        await screen.findByRole("button", { name: approve }),
      ).toBeEnabled();
    },
  );

  it("shows a claim confirmed in this session as Claimed before the server indexes it", async () => {
    vi.mocked(api.getProject).mockResolvedValue({
      ...PROJECT,
      addresses: { ...PROJECT.addresses, incomeDistributor: DISTRIBUTOR },
    } as never);
    renderWithWallet(<Investor />, { connected: true });
    fireEvent.click(await screen.findByRole("button", { name: "Claim" }));
    fireEvent.click(
      await screen.findByRole("button", { name: "Confirm claim" }),
    );
    // the server still reports distribution 3 as Claimable on the reload after the receipt
    await waitFor(() => expect(api.listIncomeClaims).toHaveBeenCalledTimes(2));
    expect(await screen.findByText(/Distribution 3: .*Claimed/)).toBeVisible();
    expect(screen.queryByRole("button", { name: "Claim" })).toBeNull();
  });

  it("does not carry a session claim over to another wallet", async () => {
    vi.mocked(api.getProject).mockResolvedValue({
      ...PROJECT,
      addresses: { ...PROJECT.addresses, incomeDistributor: DISTRIBUTOR },
    } as never);
    renderWithWallet(<Investor />, { connected: true });
    fireEvent.click(await screen.findByRole("button", { name: "Claim" }));
    fireEvent.click(
      await screen.findByRole("button", { name: "Confirm claim" }),
    );
    expect(await screen.findByText(/Distribution 3: .*Claimed/)).toBeVisible();

    // wallet B has its own Claimable entry in the same distribution
    const other = "0x2222222222222222222222222222222222222222";
    act(() => wallet.emitAccountsChanged([other]));
    await waitFor(() =>
      expect(api.listIncomeClaims).toHaveBeenLastCalledWith(
        other,
        expect.anything(),
      ),
    );
    expect(
      await screen.findByRole("button", { name: "Claim" }),
    ).toBeInTheDocument();
  });

  it("disables Claim while the project is paused", async () => {
    vi.mocked(api.getProject).mockResolvedValue({
      ...PROJECT,
      paused: true,
      addresses: { ...PROJECT.addresses, incomeDistributor: DISTRIBUTOR },
    } as never);
    renderWithWallet(<Investor />, { connected: true });
    expect(await screen.findByRole("button", { name: "Claim" })).toBeDisabled();
    expect(screen.getByText(/claims resume when it is unpaused/)).toBeVisible();
  });
});
