import { decodeFunctionData, erc20Abi } from "viem";
import { expect, mockOnly, test } from "../fixtures/fixtures";
import { ADDRESSES } from "../fixtures/mock-api";
import { getMockSentTransactions, MOCK_WALLET_ADDRESS } from "../fixtures/mock-wallet";
import { vaultAbi } from "../../src/lib/abis";
import { InvestorPage } from "../pages/InvestorPage";

test.describe("Investor — wallet, balance, and buy", () => {
  mockOnly();

  test("connects a wallet and verifies ownership", async ({ page }) => {
    const investor = new InvestorPage(page);
    await investor.goto();

    await investor.connectWallet();
    await expect(investor.walletSection).toContainText("0x1111…1111");

    await investor.verifyOwnership();
    await expect(page.getByText(/Ownership verified: Yes/)).toBeVisible();
  });

  test("reads the RWA token balance once a wallet is connected", async ({ page }) => {
    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();

    await investor.readBalance();
    await expect(investor.balanceSection).toContainText("1.5 gram");
  });

  test("previews a purchase, approves, then buys", async ({ page }) => {
    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();

    await investor.previewBuy("1000000000000000000");
    await expect(page.getByRole("region", { name: "Purchase preview" })).toBeVisible();

    const buyButton = investor.buySection.getByRole("button", { name: "2. Buy" });
    await expect(buyButton).toBeDisabled();

    await investor.approveBuy();
    await expect(page.getByText(/Approval submitted:/)).toBeVisible();
    await expect(buyButton).toBeEnabled();

    await investor.submitBuy();
    await expect(page.getByText(/Buy submitted:/)).toBeVisible();

    // The purchase is encoded in the browser (lib/abis.ts) and addressed to
    // this project's Vault — nothing here comes from the server.
    const sent = await getMockSentTransactions(page);
    const buy = sent.at(-1);
    expect(buy?.to?.toLowerCase()).toBe(ADDRESSES.vault.toLowerCase());
    const decoded = decodeFunctionData({
      abi: vaultAbi,
      data: buy!.data as `0x${string}`,
    });
    expect(decoded.functionName).toBe("buy");
    // The form takes whole units, so the 1e18 typed above is 1e18 tokens at 18
    // decimals — the quoted minimal-unit amount is what gets encoded.
    expect(decoded.args?.[0]).toBe(10n ** 36n);
    expect((decoded.args?.[2] as string).toLowerCase()).toBe(MOCK_WALLET_ADDRESS);
  });

  test("approval targets the quote token and the Vault as spender, not the RWA token", async ({
    page,
  }) => {
    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();

    await investor.previewBuy("1000000000000000000");
    await investor.approveBuy();
    await expect(page.getByText(/Approval submitted:/)).toBeVisible();

    const sent = await getMockSentTransactions(page);
    const approval = sent.at(-1);
    expect(approval?.to?.toLowerCase()).toBe(ADDRESSES.quoteToken.toLowerCase());
    expect(approval?.to?.toLowerCase()).not.toBe(ADDRESSES.token.toLowerCase());

    const decoded = decodeFunctionData({
      abi: erc20Abi,
      data: approval!.data as `0x${string}`,
    });
    expect(decoded.functionName).toBe("approve");
    expect((decoded.args[0] as string).toLowerCase()).toBe(ADDRESSES.vault.toLowerCase());

    // Buy only unlocks once the on-chain allowance is confirmed sufficient.
    const buyButton = investor.buySection.getByRole("button", { name: "2. Buy" });
    await expect(buyButton).toBeEnabled();
  });
});
