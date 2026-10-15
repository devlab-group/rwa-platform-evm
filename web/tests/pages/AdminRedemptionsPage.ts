import type { Page } from "@playwright/test";
import { sectionByHeading } from "../fixtures/helpers";

export class AdminRedemptionsPage {
  constructor(private readonly page: Page) {}

  async goto(): Promise<void> {
    await this.page.goto("/admin/redemptions");
  }

  /**
   * Connect through the header before funding or rejecting: the seeded session does not
   * connect a wallet. The mock admin has TREASURER_ROLE and REDEMPTION_MANAGER_ROLE for
   * these calls.
   */
  async connectWallet(): Promise<void> {
    await this.page.getByRole("button", { name: "Connect wallet" }).click();
    await this.page.getByRole("button", { name: "Disconnect" }).waitFor();
  }

  async filterByStatus(status: "" | "Pending" | "Funded" | "Completed" | "Rejected" | "Cancelled"): Promise<void> {
    await this.page.locator("#status-filter").selectOption(status);
  }

  row(id: string) {
    return this.page.locator("tr", { hasText: id });
  }

  async manage(id: string): Promise<void> {
    await this.row(id).getByRole("button", { name: "Manage" }).click();
  }

  // Set right before calling detail actions so detailSection can find the heading.
  currentDetailId = "";

  /** The open Redemption detail card, for asserting progress/success messages. */
  get detailSection() {
    return sectionByHeading(this.page, `Redemption ${this.currentDetailId}`);
  }

  /**
   * Funds the redemption from the connected wallet: approves the escrow for the
   * quote amount when the allowance is short, then calls fundRedemption. Assert
   * the sent transactions with getMockSentTransactions, or the success message
   * on detailSection.
   */
  async fundRedemption(id: string): Promise<void> {
    this.currentDetailId = id;
    await this.detailSection.getByRole("button", { name: "Fund redemption" }).click();
  }

  /** Rejects the redemption from the connected wallet with the encoded reason code */
  async rejectRedemption(id: string, reasonCode: string): Promise<void> {
    this.currentDetailId = id;
    await this.detailSection.locator("#reasonCode").fill(reasonCode);
    await this.detailSection.getByRole("button", { name: "Reject redemption" }).click();
  }
}
