// axe-core scans for WCAG 2.0/2.1 A/AA violations of any impact: labels, roles,
// contrast, landmarks and headings. Signed-out markup is checked in mock and live
// modes; seeded wallet states (KYC, buy preview, redemptions, transfer eligibility
// and chain mismatch) run only with mocks.
import AxeBuilder from "@axe-core/playwright";
import type { Page } from "@playwright/test";
import { expect, mockOnly, test } from "../fixtures/fixtures";
import { MOCK_WALLET_ADDRESS, mockWalletSwitchChain } from "../fixtures/mock-wallet";
import { InvestorPage } from "../pages/InvestorPage";

async function expectNoAxeViolations(page: Page) {
  const results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
    .analyze();
  expect(
    results.violations,
    results.violations
      .map((v) => `${v.id} [${v.impact}]: ${v.help} (${v.nodes.length} node(s))`)
      .join("\n"),
  ).toEqual([]);
}

test("/ signed out has no axe violations", async ({ page }) => {
  await page.goto("/");
  // The shell is lazy-loaded; scan only once the page has rendered
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  await expectNoAxeViolations(page);
});

test.describe("connected states", () => {
  mockOnly();

  test("after connecting, verifying ownership and reading the balance", async ({ page, api }) => {
    api.wallets.push({ address: MOCK_WALLET_ADDRESS, status: "Allowed", ownershipVerified: false });
    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();
    await investor.verifyOwnership();
    await expect(investor.kycSection).toContainText("Allowed");
    await investor.readBalance();
    await expect(investor.balanceSection).toContainText("1.5 gram");
    await expectNoAxeViolations(page);
  });

  test("after a buy preview and approval", async ({ page }) => {
    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();
    await investor.previewBuy("1");
    await investor.approveBuy();
    await expect(page.getByText(/Approval submitted:/)).toBeVisible();
    await expect(investor.buySection.getByRole("button", { name: "2. Buy" })).toBeEnabled();
    await expectNoAxeViolations(page);
  });

  test("with the redemption table showing Pending, timed-out and claimable requests", async ({
    page,
    api,
  }) => {
    const now = Math.floor(Date.now() / 1000);
    const base = {
      beneficiary: MOCK_WALLET_ADDRESS,
      rwaAmount: "500000000000000000",
      quoteAmount: "475000",
      createdAt: now - 7200,
      beneficiaryAllowed: true,
      confirmations: 50,
    };
    api.redemptions.push(
      { ...base, id: "1", status: "Pending", claimable: false, timeoutAt: now + 3600 },
      { ...base, id: "2", status: "Pending", claimable: false, timeoutAt: now - 3600 },
      { ...base, id: "3", status: "Funded", claimable: true, timeoutAt: now + 3600 },
    );
    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();
    await expect(investor.redemptionRow("3").getByRole("button", { name: "Claim (permissionless)" })).toBeVisible();
    await expect(investor.redemptionRow("2").getByRole("button", { name: "Cancel (timeout elapsed)" })).toBeVisible();
    await expectNoAxeViolations(page);
  });

  test("with the transfer eligibility alert showing", async ({ page }) => {
    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();
    await investor.fillTransferRecipient("0xcccccccccccccccccccccccccccccccccccccccc");
    await expect(investor.transferSection).toContainText("Recipient is ineligible");
    await expectNoAxeViolations(page);
  });

  test("with the wrong-network banner showing", async ({ page }) => {
    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();
    await mockWalletSwitchChain(page, 1);
    await expect(investor.chainMismatchBanner).toBeVisible();
    await expectNoAxeViolations(page);
  });
});

test("skip link moves focus to main content", async ({ page }) => {
  await page.goto("/");
  // The shell is lazy-loaded; Tab only once the skip link exists
  const skipLink = page.getByRole("link", { name: "Skip to main content" });
  await expect(skipLink).toBeVisible();
  // Tab once from the top of the page: the skip link is the first focusable
  // element and must be focusable
  await page.keyboard.press("Tab");
  await expect(skipLink).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.locator("#main-content")).toBeFocused();
});
