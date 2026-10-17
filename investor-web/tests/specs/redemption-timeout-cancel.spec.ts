import { decodeFunctionData } from "viem";
import { expect, mockOnly, test } from "../fixtures/fixtures";
import { ADDRESSES } from "../fixtures/mock-api";
import { getMockSentTransactions } from "../fixtures/mock-wallet";
import { redemptionEscrowAbi } from "../../src/lib/abis";
import { InvestorPage } from "../pages/InvestorPage";

test.describe("Redemption - timeout and cancel", () => {
  mockOnly();

  test("a still-pending, not-yet-timed-out redemption offers neither Claim nor Cancel", async ({ page, api }) => {
    api.redemptions.push({
      id: "9002",
      beneficiary: "0x1111111111111111111111111111111111111111",
      rwaAmount: "500000000000000000",
      quoteAmount: "475000",
      status: "Pending",
      claimable: false,
      createdAt: Math.floor(Date.now() / 1000),
      timeoutAt: Math.floor(Date.now() / 1000) + 3600, // still an hour out
      beneficiaryAllowed: true,
      confirmations: 0,
    });

    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();

    // The connected wallet's requests load as an address-filtered list. This one
    // is Pending and not yet timed out
    const row = investor.redemptionRow("9002");
    await expect(row.getByText("Pending", { exact: true })).toBeVisible();
    await expect(row.getByRole("button", { name: "Cancel (timeout elapsed)" })).toHaveCount(0);
    await expect(row.getByRole("button", { name: "Claim (permissionless)" })).toHaveCount(0);
  });

  test("a Pending redemption past its timeout offers Cancel, and cancelling submits a tx", async ({ page, api }) => {
    // Numeric id: the app calls BigInt(id) to build the uint256 cancelRedemption argument
    api.redemptions.push({
      id: "9003",
      beneficiary: "0x1111111111111111111111111111111111111111",
      rwaAmount: "500000000000000000",
      quoteAmount: "475000",
      status: "Pending",
      claimable: false,
      createdAt: Math.floor(Date.now() / 1000) - 7200,
      timeoutAt: Math.floor(Date.now() / 1000) - 3600, // an hour ago
      beneficiaryAllowed: true,
      confirmations: 50,
    });

    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();

    // Loaded from the address-filtered list; Pending and past its timeout shows Cancel
    const cancelButton = investor
      .redemptionRow("9003")
      .getByRole("button", { name: "Cancel (timeout elapsed)" });
    await expect(cancelButton).toBeVisible();

    await investor.cancel("9003");
    await expect(page.getByText(/Cancel submitted:/)).toBeVisible();

    // The wallet receives cancelRedemption(9003) for the escrow, not a claim
    const cancel = (await getMockSentTransactions(page)).at(-1);
    expect(cancel?.to?.toLowerCase()).toBe(ADDRESSES.redemptionEscrow.toLowerCase());
    const decoded = decodeFunctionData({
      abi: redemptionEscrowAbi,
      data: cancel!.data as `0x${string}`,
    });
    expect(decoded.functionName).toBe("cancelRedemption");
    expect(decoded.args).toEqual([9003n]);
  });

  test("requesting a redemption sends requestRedemption(rwaAmount, minQuoteOut, deadline) to the escrow", async ({
    page,
  }) => {
    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();

    await investor.setRedeemSlippage("100");
    await investor.previewRedeem("1");
    await investor.approveRedeem();
    await expect(page.getByText(/Approval submitted:/)).toBeVisible();
    const requestButton = investor.redemptionSection.getByRole("button", {
      name: "2. Request redemption",
    });
    await expect(requestButton).toBeEnabled();
    const before = Math.floor(Date.now() / 1000);
    await investor.submitRequest();
    await expect(page.getByText(/Request submitted:/)).toBeVisible();

    const request = (await getMockSentTransactions(page)).at(-1);
    expect(request?.to?.toLowerCase()).toBe(ADDRESSES.redemptionEscrow.toLowerCase());
    const decoded = decodeFunctionData({
      abi: redemptionEscrowAbi,
      data: request!.data as `0x${string}`,
    });
    expect(decoded.functionName).toBe("requestRedemption");
    const [rwaAmount, minQuoteOut, deadline] = decoded.args as [bigint, bigint, bigint];
    // 1 whole token at 18 decimals; the mock escrow quotes 950000 and 100 bps
    // slippage leaves a 940500 floor
    expect(rwaAmount).toBe(10n ** 18n);
    expect(minQuoteOut).toBe(940_500n);
    // Ten-minute deadline from submit time
    expect(Number(deadline)).toBeGreaterThanOrEqual(before + 600);
    expect(Number(deadline)).toBeLessThanOrEqual(before + 600 + 60);
  });
});
