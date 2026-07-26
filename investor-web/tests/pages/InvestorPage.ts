import type { Page } from "@playwright/test";
import { sectionByHeading } from "../fixtures/helpers";

export class InvestorPage {
  constructor(private readonly page: Page) {}

  async goto(): Promise<void> {
    await this.page.goto("/");
  }

  // --- Wallet --------------------------------------------------------------
  get walletSection() {
    return sectionByHeading(this.page, "Wallet");
  }

  async connectWallet(): Promise<void> {
    await this.walletSection.getByRole("button", { name: "Connect wallet" }).click();
  }

  async verifyOwnership(): Promise<void> {
    await this.walletSection.getByRole("button", { name: "Generate ownership challenge" }).click();
    await this.walletSection.getByRole("button", { name: "Sign with wallet" }).click();
    await this.walletSection.getByRole("button", { name: "Submit signature for verification" }).click();
  }

  get chainMismatchBanner() {
    return this.walletSection.getByRole("alert");
  }

  async switchChain(): Promise<void> {
    await this.walletSection.getByRole("button", { name: /^Switch wallet to chain/ }).click();
  }

  // --- KYC -------------------------------------------------------------------
  get kycSection() {
    return sectionByHeading(this.page, "KYC status");
  }

  // --- Balance ---------------------------------------------------------------
  get balanceSection() {
    return sectionByHeading(this.page, "Balance");
  }

  async readBalance(): Promise<void> {
    await this.balanceSection.getByRole("button", { name: "Read balance" }).click();
  }

  // --- Buy ---------------------------------------------------------------------
  get buySection() {
    return sectionByHeading(this.page, "Buy");
  }

  get buyPreviewButton() {
    return this.buySection.getByRole("button", { name: "Preview quote" });
  }

  async fillBuyAmount(tokenAmount: string): Promise<void> {
    await this.page.locator("#buy-token-amount").fill(tokenAmount);
  }

  async previewBuy(tokenAmount: string): Promise<void> {
    await this.fillBuyAmount(tokenAmount);
    await this.buyPreviewButton.click();
  }

  async approveBuy(): Promise<void> {
    await this.buySection.getByRole("button", { name: "1. Approve quote token spend" }).click();
  }

  async submitBuy(): Promise<void> {
    await this.buySection.getByRole("button", { name: "2. Buy" }).click();
  }

  // --- Redemption ----------------------------------------------------------------
  get redemptionSection() {
    return sectionByHeading(this.page, "Redemption");
  }

  async previewRedeem(rwaAmount: string): Promise<void> {
    await this.page.locator("#redeem-amount").fill(rwaAmount);
    await this.redemptionSection.getByRole("button", { name: "Preview quote" }).click();
  }

  async approveRedeem(): Promise<void> {
    await this.redemptionSection.getByRole("button", { name: "1. Approve RWA spend" }).click();
  }

  async submitRequest(): Promise<void> {
    await this.redemptionSection.getByRole("button", { name: "2. Request redemption" }).click();
  }

  /** A row in the connected wallet's server-fetched redemptions list, by id. */
  redemptionRow(id: string) {
    return this.redemptionSection.locator("tr", { hasText: id });
  }

  async claim(id?: string): Promise<void> {
    const scope = id ? this.redemptionRow(id) : this.redemptionSection;
    await scope.getByRole("button", { name: "Claim (permissionless)" }).click();
  }

  async cancel(id?: string): Promise<void> {
    const scope = id ? this.redemptionRow(id) : this.redemptionSection;
    await scope.getByRole("button", { name: "Cancel (timeout elapsed)" }).click();
  }

  // --- Transfer -------------------------------------------------------------------
  get transferSection() {
    return sectionByHeading(this.page, "Transfer");
  }

  get transferButton() {
    return this.transferSection.getByRole("button", { name: "Transfer" });
  }

  async fillTransferRecipient(recipient: string): Promise<void> {
    await this.page.locator("#transfer-recipient").fill(recipient);
  }

  async transfer(fields: { recipient: string; amount: string }): Promise<void> {
    await this.fillTransferRecipient(fields.recipient);
    await this.page.locator("#transfer-amount").fill(fields.amount);
    await this.transferButton.click();
  }

  // --- History -----------------------------------------------------------------------
  get historySection() {
    return sectionByHeading(this.page, "Transaction history");
  }
}
