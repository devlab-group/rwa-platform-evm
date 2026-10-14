// An investor's own KYC status comes from the X-Wallet-Session minted at
// challenge-verify, not from the admin-only wallet list (see
// lib/walletSession.ts).
import { expect, mockOnly, test } from "../fixtures/fixtures";
import { MOCK_WALLET_ADDRESS } from "../fixtures/mock-wallet";
import { InvestorPage } from "../pages/InvestorPage";

test.describe("Investor - KYC via wallet session", () => {
  mockOnly();

  test("KYC status is unavailable until the ownership challenge is verified", async ({
    page,
  }) => {
    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();

    await expect(investor.kycSection).toContainText(
      "Sign an ownership challenge",
    );
  });

  test("verifying the challenge establishes a session and loads KYC status through it", async ({
    page,
    api,
  }) => {
    api.wallets.push({
      address: MOCK_WALLET_ADDRESS,
      status: "Allowed",
      ownershipVerified: false,
    });

    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();

    // Registered before the verify click so a fast request can't be missed
    const requestPromise = page.waitForRequest((r) =>
      r.url().includes("/api/v1/me/wallet-status"),
    );
    await investor.verifyOwnership();
    const request = await requestPromise;

    // The only credential the app sends is this session token (see
    // src/lib/walletSession.ts)
    expect(request.headers()["x-wallet-session"]).toBeTruthy();
    expect(request.headers()["authorization"]).toBeFalsy();
    await expect(investor.kycSection).toContainText("Allowed");
  });

  test("a returning investor with a live session does not need to re-verify", async ({
    page,
    api,
  }) => {
    api.wallets.push({
      address: MOCK_WALLET_ADDRESS,
      status: "Allowed",
      ownershipVerified: false,
    });

    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();
    await investor.verifyOwnership();
    await expect(investor.kycSection).toContainText("Allowed");

    // Simulate a fresh page load with the same wallet reconnecting: the session
    // stored in localStorage is reused
    await investor.goto();
    await investor.connectWallet();
    await expect(investor.kycSection).toContainText("Allowed");
  });
});
