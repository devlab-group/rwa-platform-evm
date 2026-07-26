import { expect, mockOnly, test } from "../fixtures/fixtures";
import { InvestorPage } from "../pages/InvestorPage";

const ALLOWED_ADDRESS = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
const BLOCKED_ADDRESS = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb";
const UNKNOWN_ADDRESS = "0xcccccccccccccccccccccccccccccccccccccccc";

test.describe("Investor — transfer compliance gate", () => {
  mockOnly();

  // Eligibility comes from the public, unauthenticated
  // GET /api/v1/compliance/allowed/{address} (AllowedResult{allowed}): an
  // investor session can never reach the operator-only global wallet list,
  // and that boolean-only shape deliberately doesn't distinguish Blocked from
  // unknown/never-seen.
  test("Transfer stays disabled for a Blocked recipient", async ({ page, api }) => {
    api.wallets.push({ address: BLOCKED_ADDRESS, status: "Blocked", ownershipVerified: false });

    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();

    await investor.fillTransferRecipient(BLOCKED_ADDRESS);
    await expect(investor.transferSection).toContainText("not eligible to receive tokens — transfer disabled");
    await expect(investor.transferButton).toBeDisabled();
  });

  test("Transfer stays disabled for a recipient not on the allowlist at all", async ({ page }) => {
    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();

    await investor.fillTransferRecipient(UNKNOWN_ADDRESS);
    await expect(investor.transferSection).toContainText("not eligible to receive tokens — transfer disabled");
    await expect(investor.transferButton).toBeDisabled();
  });

  test("Transfer is enabled once the recipient is Allowed, and submits", async ({ page, api }) => {
    api.wallets.push({ address: ALLOWED_ADDRESS, status: "Allowed", ownershipVerified: true });

    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();

    await investor.transfer({ recipient: ALLOWED_ADDRESS, amount: "1000000000000000000" });
    await expect(page.getByText(/Submitted:/)).toBeVisible();

    // The submitted hash lands in the investor's own local history —
    // transaction history no longer reads the operator-only
    // GET /api/v1/transactions, which doesn't track wallet-submitted txs
    // anyway.
    await expect(investor.historySection).toContainText("transfer");
    await expect(investor.historySection.locator("td.mono")).toBeVisible();
  });
});
