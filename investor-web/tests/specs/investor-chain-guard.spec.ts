// Every wallet read and transaction is bound to the project's chain. A wrong
// initial network, or a switch after connect and before submission, blocks every
// wallet action instead of signing on whatever chain the wallet is on.
import { expect, mockOnly, test } from "../fixtures/fixtures";
import { CHAIN_ID } from "../fixtures/mock-api";
import { installMockWallet, mockWalletSwitchChain } from "../fixtures/mock-wallet";
import { InvestorPage } from "../pages/InvestorPage";

const WRONG_CHAIN_ID = 1;
const RECIPIENT = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";

test.describe("Investor - wallet chain binding", () => {
  mockOnly();

  test("a wrong initial network blocks reads and every wallet action", async ({ page }) => {
    // Install the mock wallet again reporting the wrong chain; this init
    // script runs after the fixture's default one and overrides it
    await installMockWallet(page, { chainId: WRONG_CHAIN_ID });

    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();

    await expect(investor.chainMismatchBanner).toContainText(
      `chain ${WRONG_CHAIN_ID}`,
    );
    await expect(investor.chainMismatchBanner).toContainText(
      `chain ${CHAIN_ID}`,
    );

    // Balance read is blocked.
    await expect(investor.balanceSection.getByRole("button", { name: "Read balance" })).toBeDisabled();

    // The buy quote is an on-chain read from the Vault, so it is blocked too and
    // there is no quote to approve against on the wrong chain
    await investor.fillBuyAmount("1000000000000000000");
    await expect(investor.buyPreviewButton).toBeDisabled();
    await expect(
      investor.buySection.getByRole("button", { name: "1. Approve quote token spend" }),
    ).toHaveCount(0);

    // Transfer is blocked too
    await investor.fillTransferRecipient(RECIPIENT);
    await expect(investor.transferButton).toBeDisabled();
  });

  test("switching chain after connect and before submission blocks the action and offers a switch prompt", async ({
    page,
  }) => {
    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();
    await expect(investor.chainMismatchBanner).toHaveCount(0);

    await investor.previewBuy("1000000000000000000");
    const approveButton = investor.buySection.getByRole("button", {
      name: "1. Approve quote token spend",
    });
    await expect(approveButton).toBeEnabled();

    await mockWalletSwitchChain(page, WRONG_CHAIN_ID);

    await expect(investor.chainMismatchBanner).toBeVisible();
    await expect(approveButton).toBeDisabled();
    await expect(investor.walletSection).toContainText(String(WRONG_CHAIN_ID));

    // The switch button moves the wallet back to the project's chain
    await investor.switchChain();
    await expect(investor.chainMismatchBanner).toHaveCount(0);
    await expect(approveButton).toBeEnabled();
  });

  test("a wallet-side account/disconnect change is reflected reactively", async ({ page }) => {
    const investor = new InvestorPage(page);
    await investor.goto();
    await investor.connectWallet();
    await expect(investor.walletSection).toContainText("0x1111…1111");

    await page.evaluate(() => {
      (
        window as unknown as { ethereum: { _emit: (event: string, payload: unknown) => void } }
      ).ethereum._emit("accountsChanged", []);
    });

    await expect(investor.walletSection.getByRole("button", { name: "Connect wallet" })).toBeVisible();
  });
});
