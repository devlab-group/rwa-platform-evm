import { expect, mockOnly, test } from "../fixtures/fixtures";
import { AdminRedemptionsPage } from "../pages/AdminRedemptionsPage";
import { ADDRESSES } from "../fixtures/mock-api";
import { getMockSentTransactions } from "../fixtures/mock-wallet";

test.describe("Redemption — admin funding", () => {
  mockOnly();

  test("admin funds a pending redemption from the connected wallet (approve → fund)", async ({
    page,
    api,
  }) => {
    // Numeric id: the web does BigInt(id) to build the uint256 fundRedemption arg.
    api.redemptions.push({
      id: "1",
      beneficiary: "0x1234561234561234561234561234561234561234",
      rwaAmount: "500000000000000000",
      quoteAmount: "475000",
      status: "Pending",
      claimable: false,
      createdAt: Math.floor(Date.now() / 1000),
      timeoutAt: Math.floor(Date.now() / 1000) + 3600,
      beneficiaryAllowed: true,
      confirmations: 3,
    });

    const redemptions = new AdminRedemptionsPage(page);
    await redemptions.goto();
    await expect(redemptions.row("1")).toBeVisible();

    await redemptions.connectWallet();
    await redemptions.manage("1");
    await redemptions.fundRedemption("1");

    // Broadcast directly from the wallet: an approve on the quote token, then
    // fundRedemption on the escrow. The mock wallet starts with zero allowance,
    // so the approve path runs first.
    await expect(redemptions.detailSection.getByText("Redemption funded.")).toBeVisible();
    const sent = await getMockSentTransactions(page);
    const targets = sent.map((t) => t.to?.toLowerCase());
    expect(targets).toContain(ADDRESSES.quoteToken.toLowerCase()); // approve
    expect(targets).toContain(ADDRESSES.redemptionEscrow.toLowerCase()); // fundRedemption
  });
});
