// Every wallet read/transaction has to be bound to the project's configured
// chain. A wrong initial network — or a network switch after connect but
// before submission — should block every wallet action rather than silently
// signing against whatever chain the wallet happens to be on.
import { expect, mockOnly, test } from "../fixtures/fixtures";
import { CHAIN_ID } from "../fixtures/mock-api";
import { installMockWallet, mockWalletSwitchChain } from "../fixtures/mock-wallet";
import { InvestorPage } from "../pages/InvestorPage";

const WRONG_CHAIN_ID = 1;
const RECIPIENT = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";

test.describe("Investor — wallet chain binding", () => {
  mockOnly();

  test("a wrong initial network blocks reads and every wallet action", async ({ page }) => {
    // Re-install the mock wallet reporting the wrong chain, overriding the
    // fixture's default (correct-chain) install added just before this.
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

    // The buy quote is itself an on-chain read from the Vault, so it is
    // blocked too — there is no quote to approve against on the wrong chain.
    await investor.fillBuyAmount("1000000000000000000");
    await expect(investor.buyPreviewButton).toBeDisabled();
    await expect(
      investor.buySection.getByRole("button", { name: "1. Approve quote token spend" }),
    ).toHaveCount(0);

    // Transfer is blocked even for an otherwise-allowed recipient.
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

    // The offered switch prompt reconnects to the project's chain.
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
