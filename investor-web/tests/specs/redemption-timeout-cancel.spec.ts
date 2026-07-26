import { expect, mockOnly, test } from "../fixtures/fixtures";
import { InvestorPage } from "../pages/InvestorPage";

test.describe("Redemption — timeout and cancel", () => {
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

    // The connected wallet's requests load as an address-filtered list — no
    // lookup-by-id needed. This one is Pending and not yet timed out.
    const row = investor.redemptionRow("9002");
    await expect(row.locator(".badge[data-status='Pending']")).toBeVisible();
    await expect(row.getByRole("button", { name: "Cancel (timeout elapsed)" })).toHaveCount(0);
    await expect(row.getByRole("button", { name: "Claim (permissionless)" })).toHaveCount(0);
  });

  test("a Pending redemption past its timeout offers Cancel, and cancelling submits a tx", async ({ page, api }) => {
    // Numeric id: the web does BigInt(id) to build the uint256 cancelRedemption arg.
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

    // Loaded from the address-filtered list; Pending + timed out => Cancel.
    const cancelButton = investor
      .redemptionRow("9003")
      .getByRole("button", { name: "Cancel (timeout elapsed)" });
    await expect(cancelButton).toBeVisible();

    await investor.cancel("9003");
    await expect(page.getByText(/Cancel submitted:/)).toBeVisible();
  });
});
