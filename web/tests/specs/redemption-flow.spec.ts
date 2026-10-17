import { decodeFunctionData, erc20Abi, sha256, toBytes } from "viem";
import { expect, mockOnly, test } from "../fixtures/fixtures";
import { AdminRedemptionsPage } from "../pages/AdminRedemptionsPage";
import { ADDRESSES } from "../fixtures/mock-api";
import { getMockSentTransactions } from "../fixtures/mock-wallet";
import { redemptionEscrowAbi } from "../../src/lib/abis";

function pendingRedemption(id: string) {
  return {
    id,
    beneficiary: "0x1234561234561234561234561234561234561234",
    rwaAmount: "500000000000000000",
    quoteAmount: "475000",
    status: "Pending" as const,
    claimable: false,
    createdAt: Math.floor(Date.now() / 1000),
    timeoutAt: Math.floor(Date.now() / 1000) + 3600,
    beneficiaryAllowed: true,
    confirmations: 3,
  };
}

test.describe("Redemption - admin funding", () => {
  mockOnly();

  test("admin funds a pending redemption from the connected wallet (approve → fund)", async ({
    page,
    api,
  }) => {
    // Numeric id: the console calls BigInt(id) to build the uint256 fundRedemption argument
    api.redemptions.push(pendingRedemption("1"));

    const redemptions = new AdminRedemptionsPage(page);
    await redemptions.goto();
    await expect(redemptions.row("1")).toBeVisible();

    await redemptions.connectWallet();
    await redemptions.manage("1");
    await redemptions.fundRedemption("1");

    // The wallet broadcasts an approve on the quote token, then fundRedemption
    // on the escrow; the mock wallet starts with zero allowance, so the approve
    // comes first
    await expect(redemptions.detailSection.getByText("Redemption funded.")).toBeVisible();
    const sent = await getMockSentTransactions(page);
    expect(sent).toHaveLength(2);
    const [approve, fund] = sent;
    // 1: approve(escrow, exactly the snapshotted quote amount) on the quote token
    expect(approve.to?.toLowerCase()).toBe(ADDRESSES.quoteToken.toLowerCase());
    const approval = decodeFunctionData({ abi: erc20Abi, data: approve.data as `0x${string}` });
    expect(approval.functionName).toBe("approve");
    expect((approval.args![0] as string).toLowerCase()).toBe(ADDRESSES.redemptionEscrow.toLowerCase());
    expect(approval.args![1]).toBe(475000n);
    // 2: fundRedemption(1) on the escrow, with no claim
    expect(fund.to?.toLowerCase()).toBe(ADDRESSES.redemptionEscrow.toLowerCase());
    expect(decodeFunctionData({ abi: redemptionEscrowAbi, data: fund.data as `0x${string}` })).toEqual({
      functionName: "fundRedemption",
      args: [1n],
    });
  });

  test("admin rejects a pending redemption with a hashed reason code", async ({ page, api }) => {
    api.redemptions.push(pendingRedemption("2"));

    const redemptions = new AdminRedemptionsPage(page);
    await redemptions.goto();
    await redemptions.connectWallet();
    await redemptions.manage("2");
    await redemptions.rejectRedemption("2", "KYC_EXPIRED");

    await expect(redemptions.detailSection.getByText("Redemption rejected.")).toBeVisible();
    const sent = await getMockSentTransactions(page);
    expect(sent).toHaveLength(1);
    expect(sent[0].to?.toLowerCase()).toBe(ADDRESSES.redemptionEscrow.toLowerCase());
    expect(decodeFunctionData({ abi: redemptionEscrowAbi, data: sent[0].data as `0x${string}` })).toEqual({
      functionName: "rejectRedemption",
      args: [2n, sha256(toBytes("KYC_EXPIRED"))],
    });
  });
});
