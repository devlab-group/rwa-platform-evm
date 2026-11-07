import {
  decodeFunctionData,
  encodeErrorResult,
  encodeFunctionData,
  erc20Abi,
} from "viem";
import { expect, mockOnly, test } from "../fixtures/fixtures";
import { AdminRedemptionsPage } from "../pages/AdminRedemptionsPage";
import { ADDRESSES } from "../fixtures/mock-api";
import { getMockSentTransactions, setMockCall } from "../fixtures/mock-wallet";
import {
  oracleStrategyErrorsAbi,
  redemptionEscrowAbi,
} from "../../src/lib/abis";

// A rwa-v4 request: quoteAmount stays 0 until funding picks the round that prices it
const FORWARD_REDEMPTION = {
  id: "8",
  beneficiary: "0x1234561234561234561234561234561234561234",
  rwaAmount: "500000000000000000",
  quoteAmount: "0",
  strategy: ADDRESSES.strategy,
  baseRound: "40",
  minQuoteOut: "400000",
  status: "Pending" as const,
  claimable: false,
  createdAt: Math.floor(Date.now() / 1000),
  timeoutAt: Math.floor(Date.now() / 1000) + 3600,
  beneficiaryAllowed: true,
  confirmations: 3,
};

const previewFundingCall = encodeFunctionData({
  abi: redemptionEscrowAbi,
  functionName: "previewFunding",
  args: [8n],
});

test.describe("Redemption - funding a forward-priced request", () => {
  mockOnly();

  test("waits for a price round, then approves and funds the exact previewed amount", async ({
    page,
    api,
  }) => {
    api.project.quoteDecimals = 6;
    api.redemptions.push(FORWARD_REDEMPTION);

    const redemptions = new AdminRedemptionsPage(page);
    await redemptions.goto();
    // The list never shows the placeholder zero as a price
    await expect(redemptions.row("8")).toContainText(
      "Priced at funding (holder minimum 0.4)",
    );
    await redemptions.connectWallet();

    // No round has landed past the dealing cut-off yet
    await setMockCall(page, previewFundingCall, {
      revert: encodeErrorResult({
        abi: oracleStrategyErrorsAbi,
        errorName: "RoundNotAvailable",
        args: [41n],
      }),
    });
    await redemptions.manage("8");
    redemptions.currentDetailId = "8";
    await expect(
      redemptions.detailSection.getByText(/Waiting for a price round/),
    ).toBeVisible();
    await expect(
      redemptions.detailSection.getByRole("button", {
        name: "Fund redemption",
      }),
    ).toBeDisabled();

    // The round lands: previewFunding now reports the quote funding will pull
    await setMockCall(page, previewFundingCall, {
      result: `0x${(475_123).toString(16).padStart(64, "0")}`,
    });
    await redemptions.detailSection
      .getByRole("button", { name: "Refresh price" })
      .click();
    await expect(
      redemptions.detailSection.getByText("Funding will pull 0.475123."),
    ).toBeVisible();

    await redemptions.fundRedemption("8");

    await expect(
      redemptions.detailSection.getByText("Redemption funded."),
    ).toBeVisible();
    const sent = await getMockSentTransactions(page);
    expect(sent).toHaveLength(2);
    const [approve, fund] = sent;
    // The approval is the previewed amount; the request itself records 0
    expect(approve.to?.toLowerCase()).toBe(ADDRESSES.quoteToken.toLowerCase());
    const approval = decodeFunctionData({
      abi: erc20Abi,
      data: approve.data as `0x${string}`,
    });
    expect((approval.args![0] as string).toLowerCase()).toBe(
      ADDRESSES.redemptionEscrow.toLowerCase(),
    );
    expect(approval.args![1]).toBe(475_123n);
    expect(fund.to?.toLowerCase()).toBe(
      ADDRESSES.redemptionEscrow.toLowerCase(),
    );
    expect(
      decodeFunctionData({
        abi: redemptionEscrowAbi,
        data: fund.data as `0x${string}`,
      }),
    ).toEqual({
      functionName: "fundRedemption",
      args: [8n],
    });
  });

  test("a quote below the holder's minimum cannot be funded and offers rejection", async ({
    page,
    api,
  }) => {
    api.project.quoteDecimals = 6;
    api.redemptions.push(FORWARD_REDEMPTION);

    const redemptions = new AdminRedemptionsPage(page);
    await redemptions.goto();
    await redemptions.connectWallet();
    await setMockCall(page, previewFundingCall, {
      revert: encodeErrorResult({
        abi: [
          {
            type: "error",
            name: "QuoteBelowMin",
            inputs: [
              { name: "quoted", type: "uint256" },
              { name: "minQuoteOut", type: "uint256" },
            ],
          },
        ],
        errorName: "QuoteBelowMin",
        args: [300_000n, 400_000n],
      }),
    });
    await redemptions.manage("8");
    redemptions.currentDetailId = "8";

    await expect(
      redemptions.detailSection.getByText(
        /0.3 is below the holder's minimum of 0.4/,
      ),
    ).toBeVisible();
    await expect(
      redemptions.detailSection.getByRole("button", {
        name: "Fund redemption",
      }),
    ).toBeDisabled();
    await expect(
      redemptions.detailSection.getByRole("button", {
        name: "Reject redemption",
      }),
    ).toBeVisible();
    expect(await getMockSentTransactions(page)).toHaveLength(0);
  });
});
