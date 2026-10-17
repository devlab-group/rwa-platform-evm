import { createHash } from "node:crypto";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { decodeFunctionData } from "viem";
import { Redemptions } from "./Redemptions";
import { api } from "../../lib/client";
import { exportSafeBatch } from "../../lib/safeBatch";
import {
  readErc20Allowance,
  sendErc20Approve,
  sendFundRedemption,
  sendRejectRedemption,
  waitForTxReceipt,
} from "../../lib/wallet";
import { redemptionEscrowAbi } from "../../lib/abis";
import { installFakeWallet, renderWithWallet } from "../../test/walletHarness";

vi.mock("../../lib/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/client")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      getProject: vi.fn(),
      listRedemptions: vi.fn(),
      getRedemption: vi.fn(),
    },
  };
});

// Spy on the batch export without downloading a browser file
vi.mock("../../lib/safeBatch", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/safeBatch")>();
  return { ...actual, exportSafeBatch: vi.fn(() => "Saved batch.") };
});

// Stub chain reads and broadcasts; the real calldata encoders stay in place
vi.mock("../../lib/wallet", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/wallet")>();
  return {
    ...actual,
    readErc20Allowance: vi.fn(),
    sendErc20Approve: vi.fn(),
    sendFundRedemption: vi.fn(),
    sendRejectRedemption: vi.fn(),
    waitForTxReceipt: vi.fn(),
  };
});

const ADMIN = "0x1111111111111111111111111111111111111111";
const SAFE = "0x00000000000000000000000000000000000000f1";
const ESCROW = "0x0000000000000000000000000000000000000050";
const QUOTE = "0x0000000000000000000000000000000000000070";

const PROJECT = {
  chainId: 31337,
  decimals: 18,
  quoteDecimals: 6,
  finalityConfirmations: 12,
  addresses: {
    token: "0x0000000000000000000000000000000000000010",
    quoteToken: QUOTE,
    redemptionEscrow: ESCROW,
  },
  roles: {
    TREASURER_ROLE: [ADMIN],
    REDEMPTION_MANAGER_ROLE: [ADMIN],
  },
};

const REDEMPTION = {
  id: "7",
  beneficiary: "0x00000000000000000000000000000000000000b1",
  rwaAmount: "500000000000000000",
  quoteAmount: "475000",
  status: "Pending" as const,
  claimable: false,
  createdAt: 1_800_000_000,
  timeoutAt: 1_800_003_600,
  beneficiaryAllowed: true,
  confirmations: 3,
};

let wallet: ReturnType<typeof installFakeWallet>;

beforeEach(() => {
  vi.resetAllMocks();
  wallet = installFakeWallet({ account: ADMIN, chainId: 31337 });
  vi.mocked(api.getProject).mockResolvedValue(PROJECT);
  vi.mocked(api.listRedemptions).mockResolvedValue({ items: [REDEMPTION] });
  vi.mocked(api.getRedemption).mockResolvedValue(REDEMPTION);
  vi.mocked(waitForTxReceipt).mockResolvedValue(undefined);
});

afterEach(() => {
  wallet.uninstall();
});

async function openDetail(project = PROJECT, redemption = REDEMPTION) {
  vi.mocked(api.getProject).mockResolvedValue(project);
  vi.mocked(api.getRedemption).mockResolvedValue(redemption);
  vi.mocked(api.listRedemptions).mockResolvedValue({ items: [redemption] });
  renderWithWallet(<Redemptions />, { connected: true });
  fireEvent.click(await screen.findByRole("button", { name: "Manage" }));
  await screen.findByRole("heading", { name: `Redemption ${redemption.id}` });
}

describe("Redemptions role gating", () => {
  it("shows both actions to a wallet holding TREASURER_ROLE and REDEMPTION_MANAGER_ROLE", async () => {
    await openDetail();
    expect(await screen.findByRole("button", { name: "Fund redemption" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Reject redemption" })).toBeDisabled();
  });

  it("hides reject from a treasurer who is not a redemption manager", async () => {
    await openDetail({ ...PROJECT, roles: { TREASURER_ROLE: [ADMIN] } } as typeof PROJECT);
    expect(await screen.findByRole("button", { name: "Fund redemption" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reject redemption" })).toBeNull();
    expect(screen.getByText("REDEMPTION_MANAGER_ROLE")).toBeInTheDocument();
  });

  it("hides fund from a redemption manager who is not a treasurer", async () => {
    await openDetail({ ...PROJECT, roles: { REDEMPTION_MANAGER_ROLE: [ADMIN] } } as typeof PROJECT);
    expect(await screen.findByLabelText("Rejection reason code")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Fund redemption" })).toBeNull();
    expect(screen.getByText("TREASURER_ROLE")).toBeInTheDocument();
  });

  it("offers a Safe export only after a Safe holding the role is chosen", async () => {
    await openDetail({ ...PROJECT, roles: { TREASURER_ROLE: [SAFE], REDEMPTION_MANAGER_ROLE: [SAFE] } } as typeof PROJECT);
    expect(await screen.findAllByText(/Connect a wallet holding/)).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "Save funding batch" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Fund redemption" })).toBeNull();

    fireEvent.change(screen.getAllByLabelText(/Save a Safe/)[0], { target: { value: SAFE } });
    expect(await screen.findByRole("button", { name: "Save funding batch" })).toBeInTheDocument();
  });
});

describe("Redemptions funding from the wallet", () => {
  it("approves the exact quote amount to the escrow, then funds, in that order", async () => {
    vi.mocked(readErc20Allowance).mockResolvedValue(0n);
    vi.mocked(sendErc20Approve).mockResolvedValue("0xaaa1");
    vi.mocked(sendFundRedemption).mockResolvedValue("0xbbb2");
    await openDetail();

    fireEvent.click(await screen.findByRole("button", { name: "Fund redemption" }));

    expect(await screen.findByText("Redemption funded.")).toBeInTheDocument();
    expect(readErc20Allowance).toHaveBeenCalledWith(31337, QUOTE, ADMIN, ESCROW);
    expect(sendErc20Approve).toHaveBeenCalledWith(31337, ADMIN, QUOTE, ESCROW, 475000n);
    expect(sendFundRedemption).toHaveBeenCalledWith(31337, ADMIN, ESCROW, 7n);
    // The approval must be mined before the funding is sent
    expect(vi.mocked(waitForTxReceipt).mock.calls.map(([h]) => h)).toEqual(["0xaaa1", "0xbbb2"]);
    const order = [
      vi.mocked(sendErc20Approve).mock.invocationCallOrder[0],
      vi.mocked(waitForTxReceipt).mock.invocationCallOrder[0],
      vi.mocked(sendFundRedemption).mock.invocationCallOrder[0],
    ];
    expect(order).toEqual([...order].sort((a, b) => a - b));
    // The list and the detail are refreshed after funding
    await waitFor(() => expect(api.listRedemptions).toHaveBeenCalledTimes(2));
    expect(api.getRedemption).toHaveBeenCalledTimes(2);
  });

  it("skips the approval when the allowance already covers the quote amount", async () => {
    vi.mocked(readErc20Allowance).mockResolvedValue(475000n);
    vi.mocked(sendFundRedemption).mockResolvedValue("0xbbb2");
    await openDetail();

    fireEvent.click(await screen.findByRole("button", { name: "Fund redemption" }));

    expect(await screen.findByText("Redemption funded.")).toBeInTheDocument();
    expect(sendErc20Approve).not.toHaveBeenCalled();
    expect(sendFundRedemption).toHaveBeenCalledTimes(1);
  });

  it("approves when the allowance is one unit short", async () => {
    vi.mocked(readErc20Allowance).mockResolvedValue(474999n);
    vi.mocked(sendErc20Approve).mockResolvedValue("0xaaa1");
    vi.mocked(sendFundRedemption).mockResolvedValue("0xbbb2");
    await openDetail();

    fireEvent.click(await screen.findByRole("button", { name: "Fund redemption" }));

    expect(await screen.findByText("Redemption funded.")).toBeInTheDocument();
    expect(sendErc20Approve).toHaveBeenCalledTimes(1);
  });

  it("does not fund when the approval is rejected, and shows the error", async () => {
    vi.mocked(readErc20Allowance).mockResolvedValue(0n);
    vi.mocked(sendErc20Approve).mockRejectedValue(new Error("User rejected the request."));
    await openDetail();

    fireEvent.click(await screen.findByRole("button", { name: "Fund redemption" }));

    expect(await screen.findByText("User rejected the request.")).toBeInTheDocument();
    expect(sendFundRedemption).not.toHaveBeenCalled();
    expect(screen.queryByText("Redemption funded.")).toBeNull();
    // The button is usable again for a retry
    expect(screen.getByRole("button", { name: "Fund redemption" })).toBeEnabled();
  });

  it("does not report success when the funding transaction reverts on-chain", async () => {
    vi.mocked(readErc20Allowance).mockResolvedValue(475000n);
    vi.mocked(sendFundRedemption).mockResolvedValue("0xbbb2");
    vi.mocked(waitForTxReceipt).mockRejectedValue(new Error("Transaction reverted on-chain."));
    await openDetail();

    fireEvent.click(await screen.findByRole("button", { name: "Fund redemption" }));

    expect(await screen.findByText("Transaction reverted on-chain.")).toBeInTheDocument();
    expect(screen.queryByText("Redemption funded.")).toBeNull();
    expect(api.listRedemptions).toHaveBeenCalledTimes(1);
  });

  it("blocks funding for a beneficiary who is not Allowed and sends nothing", async () => {
    await openDetail(PROJECT, { ...REDEMPTION, beneficiaryAllowed: false });

    expect(await screen.findByRole("button", { name: "Fund redemption" })).toBeDisabled();
    expect(screen.getByText(/Restore the beneficiary/)).toBeInTheDocument();
    expect(readErc20Allowance).not.toHaveBeenCalled();
    expect(sendFundRedemption).not.toHaveBeenCalled();
  });

  it("reports a missing escrow address instead of sending", async () => {
    await openDetail({ ...PROJECT, addresses: { ...PROJECT.addresses, redemptionEscrow: undefined } } as unknown as typeof PROJECT);

    fireEvent.click(await screen.findByRole("button", { name: "Fund redemption" }));

    expect(await screen.findByText(/Escrow, quote-token address or quote amount is missing/)).toBeInTheDocument();
    expect(sendFundRedemption).not.toHaveBeenCalled();
  });
});

describe("Redemptions funding as a Safe batch", () => {
  const AS_SAFE = { ...PROJECT, roles: { TREASURER_ROLE: [SAFE], REDEMPTION_MANAGER_ROLE: [SAFE] } };

  it("exports approve, fund and claim by default, and nothing is broadcast", async () => {
    await openDetail(AS_SAFE as typeof PROJECT);
    fireEvent.change(screen.getAllByLabelText(/Save a Safe/)[0], { target: { value: SAFE } });
    fireEvent.click(await screen.findByRole("button", { name: "Save funding batch" }));

    expect(await screen.findByText("Saved batch.")).toBeInTheDocument();
    const [chainId, safe, name, calls] = vi.mocked(exportSafeBatch).mock.calls[0];
    expect([chainId, safe, name]).toEqual([31337, SAFE, "Fund and claim redemption"]);
    expect(calls.map((c) => c.to.toLowerCase())).toEqual([QUOTE, ESCROW, ESCROW]);
    expect(calls.slice(1).map((c) => decodeFunctionData({ abi: redemptionEscrowAbi, data: c.data }))).toEqual([
      { functionName: "fundRedemption", args: [7n] },
      { functionName: "claimRedemption", args: [7n] },
    ]);
    expect(sendFundRedemption).not.toHaveBeenCalled();
    expect(sendErc20Approve).not.toHaveBeenCalled();
  });

  it("drops the claim call when the atomic-claim box is unchecked", async () => {
    await openDetail(AS_SAFE as typeof PROJECT);
    fireEvent.change(screen.getAllByLabelText(/Save a Safe/)[0], { target: { value: SAFE } });
    fireEvent.click(await screen.findByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Save funding batch" }));

    await screen.findByText("Saved batch.");
    const [, , name, calls] = vi.mocked(exportSafeBatch).mock.calls[0];
    expect(name).toBe("Fund redemption");
    expect(calls).toHaveLength(2);
  });
});

describe("Redemptions rejection", () => {
  const sha256 = (s: string) => `0x${createHash("sha256").update(s).digest("hex")}`;

  async function reject(reason: string, button = "Reject redemption") {
    fireEvent.change(await screen.findByLabelText("Rejection reason code"), { target: { value: reason } });
    fireEvent.click(screen.getByRole("button", { name: button }));
  }

  it("keeps reject disabled until a reason is typed", async () => {
    await openDetail();
    expect(await screen.findByRole("button", { name: "Reject redemption" })).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Rejection reason code"), { target: { value: "x" } });
    expect(screen.getByRole("button", { name: "Reject redemption" })).toBeEnabled();
  });

  it("hashes a text reason with SHA-256 and rejects the redemption id", async () => {
    vi.mocked(sendRejectRedemption).mockResolvedValue("0xccc3");
    await openDetail();

    await reject("KYC_EXPIRED");

    expect(await screen.findByText("Redemption rejected.")).toBeInTheDocument();
    expect(sendRejectRedemption).toHaveBeenCalledWith(31337, ADMIN, ESCROW, 7n, sha256("KYC_EXPIRED"));
    expect(waitForTxReceipt).toHaveBeenCalledWith("0xccc3");
    await waitFor(() => expect(api.listRedemptions).toHaveBeenCalledTimes(2));
  });

  it("passes an exact 0x bytes32 reason through unchanged", async () => {
    const reason = `0x${"ab".repeat(32)}`;
    vi.mocked(sendRejectRedemption).mockResolvedValue("0xccc3");
    await openDetail();

    await reject(reason);

    await screen.findByText("Redemption rejected.");
    expect(sendRejectRedemption).toHaveBeenCalledWith(31337, ADMIN, ESCROW, 7n, reason);
  });

  it("shows the wallet error and no success when the rejection fails", async () => {
    vi.mocked(sendRejectRedemption).mockRejectedValue(new Error("User rejected the request."));
    await openDetail();

    await reject("KYC_EXPIRED");

    expect(await screen.findByText("User rejected the request.")).toBeInTheDocument();
    expect(screen.queryByText("Redemption rejected.")).toBeNull();
    expect(api.listRedemptions).toHaveBeenCalledTimes(1);
  });

  it("exports a rejectRedemption(id, reason) call for a Safe and broadcasts nothing", async () => {
    await openDetail({ ...PROJECT, roles: { TREASURER_ROLE: [SAFE], REDEMPTION_MANAGER_ROLE: [SAFE] } } as typeof PROJECT);
    fireEvent.change(screen.getAllByLabelText(/Save a Safe/)[1], { target: { value: SAFE } });
    await reject("KYC_EXPIRED", "Save rejection batch");

    await screen.findByText("Saved batch.");
    const [chainId, safe, name, calls] = vi.mocked(exportSafeBatch).mock.calls[0];
    expect([chainId, safe, name]).toEqual([31337, SAFE, "Reject redemption"]);
    expect(calls).toHaveLength(1);
    expect(calls[0].to.toLowerCase()).toBe(ESCROW);
    expect(decodeFunctionData({ abi: redemptionEscrowAbi, data: calls[0].data })).toEqual({
      functionName: "rejectRedemption",
      args: [7n, sha256("KYC_EXPIRED")],
    });
    expect(sendRejectRedemption).not.toHaveBeenCalled();
  });
});

describe("Redemptions list", () => {
  it("shows the load error and retries", async () => {
    vi.mocked(api.listRedemptions).mockRejectedValueOnce(new Error("indexer unavailable"));
    renderWithWallet(<Redemptions />);
    expect(await screen.findByText("indexer unavailable")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("7")).toBeInTheDocument();
  });

  it("filters by status through the API", async () => {
    render(<Redemptions />);
    await screen.findByText("7");
    fireEvent.change(screen.getByLabelText("Filter by status"), { target: { value: "Funded" } });
    await waitFor(() =>
      expect(api.listRedemptions).toHaveBeenLastCalledWith(expect.objectContaining({ status: "Funded" })),
    );
  });
});
