import { decodeFunctionData, erc20Abi } from "viem";
import { expect, mockOnly, test } from "../fixtures/fixtures";
import { ADDRESSES } from "../fixtures/mock-api";
import { getMockSentTransactions } from "../fixtures/mock-wallet";
import { InvestorPage } from "../pages/InvestorPage";

const ALLOWED_ADDRESS = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
const BLOCKED_ADDRESS = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb";
const UNKNOWN_ADDRESS = "0xcccccccccccccccccccccccccccccccccccccccc";

test.describe("Investor - transfer compliance gate", () => {
  mockOnly();

  // Eligibility comes from the public, unauthenticated
  // GET /api/v1/compliance/allowed/{address}, which returns only
  // AllowedResult{allowed}. That boolean does not distinguish a Blocked address
  // from an unknown one.
  test("Transfer stays disabled for a Blocked recipient", async ({ page, api }) => {
    api.wallets.push({ address: BLOCKED_ADDRESS, status: "Blocked", ownershipVerified: false });

    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();

    await investor.fillTransferRecipient(BLOCKED_ADDRESS);
    await expect(investor.transferSection).toContainText("Recipient is ineligible - transfer disabled.");
    await expect(investor.transferButton).toBeDisabled();
  });

  test("Transfer stays disabled for a recipient not on the allowlist at all", async ({ page }) => {
    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();

    await investor.fillTransferRecipient(UNKNOWN_ADDRESS);
    await expect(investor.transferSection).toContainText("Recipient is ineligible - transfer disabled.");
    await expect(investor.transferButton).toBeDisabled();
  });

  test("Transfer is enabled once the recipient is Allowed, and submits", async ({ page, api }) => {
    api.wallets.push({ address: ALLOWED_ADDRESS, status: "Allowed", ownershipVerified: true });

    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();

    await investor.transfer({ recipient: ALLOWED_ADDRESS, amount: "1" });
    await expect(page.getByText(/Submitted:/)).toBeVisible();

    // transfer(recipient, amount) goes to the RWA token, not the quote token
    const sent = (await getMockSentTransactions(page)).at(-1);
    expect(sent?.to?.toLowerCase()).toBe(ADDRESSES.token.toLowerCase());
    const decoded = decodeFunctionData({ abi: erc20Abi, data: sent!.data as `0x${string}` });
    expect(decoded.functionName).toBe("transfer");
    // viem returns the checksummed address
    expect((decoded.args[0] as string).toLowerCase()).toBe(ALLOWED_ADDRESS);
    // 1 whole token at 18 decimals
    expect(decoded.args[1]).toBe(10n ** 18n);

    // The submitted hash appears in the "Submitted this session" list. The
    // server's transaction index has no entry for a plain transfer
    await expect(investor.historySection).toContainText("transfer");
    await expect(investor.historySection.getByRole("cell", { name: /^0x[0-9a-f]{64}$/ })).toBeVisible();
  });
});
