// An investor's own KYC status must come from the subject-scoped session
// minted at challenge-verify (X-Wallet-Session), never from the operator-only
// global wallet list — see lib/walletSession.ts.
import { expect, mockOnly, test } from "../fixtures/fixtures";
import { MOCK_WALLET_ADDRESS } from "../fixtures/mock-wallet";
import { InvestorPage } from "../pages/InvestorPage";

test.describe("Investor — KYC via subject-scoped session", () => {
  mockOnly();

  test("KYC status is unavailable until the ownership challenge is verified", async ({ page }) => {
    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();

    await expect(investor.kycSection).toContainText("Generate and sign an ownership challenge");
  });

  test("verifying the challenge establishes a session and loads KYC status through it", async ({
    page,
    api,
  }) => {
    api.wallets.push({ address: MOCK_WALLET_ADDRESS, status: "Allowed", ownershipVerified: false });

    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();

    // Registered before the verify click so it can't miss a request that
    // fires faster than this test gets around to waiting for it.
    const requestPromise = page.waitForRequest((r) => r.url().includes("/api/v1/me/wallet-status"));
    await investor.verifyOwnership();
    const request = await requestPromise;

    // The only credential the app sends is this subject-scoped session token
    // (see src/lib/walletSession.ts).
    expect(request.headers()["x-wallet-session"]).toBeTruthy();
    expect(request.headers()["x-api-key"]).toBeFalsy();
    expect(request.headers()["authorization"]).toBeFalsy();
    await expect(investor.kycSection).toContainText("Allowed");
  });

  test("a returning investor with a live session doesn't need to re-verify", async ({ page, api }) => {
    api.wallets.push({ address: MOCK_WALLET_ADDRESS, status: "Allowed", ownershipVerified: false });

    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();
    await investor.verifyOwnership();
    await expect(investor.kycSection).toContainText("Allowed");

    // Simulate a fresh page load with the same wallet reconnecting — the
    // session persisted in localStorage should be reused automatically.
    await investor.goto();
    await investor.connectWallet();
    await expect(investor.kycSection).toContainText("Allowed");
  });
});
