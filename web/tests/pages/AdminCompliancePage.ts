import type { Page } from "@playwright/test";
import { sectionByHeading } from "../fixtures/helpers";

export class AdminCompliancePage {
  constructor(private readonly page: Page) {}

  async goto(): Promise<void> {
    await this.page.goto("/admin/compliance");
  }

  private get walletsSection() {
    return sectionByHeading(this.page, "Wallets");
  }

  private get setStatusSection() {
    return sectionByHeading(this.page, "Manual allow / block");
  }

  async setStatus(fields: { address: string; status: "Allowed" | "Blocked" | "Unknown" }): Promise<void> {
    await this.page.locator("#wallet-address").fill(fields.address);
    await this.page.locator("#wallet-status").selectOption(fields.status);
    // Hot-key action — goes through the two-step ConfirmStep gate.
    await this.setStatusSection.getByRole("button", { name: "Review status change" }).click();
    await this.setStatusSection.getByRole("button", { name: "Confirm set status" }).click();
  }

  // shortenAddress() renders "0x" + 4 hex chars + "…" + last 4 (src/lib/format.ts);
  // matching the first 6 chars of the raw address is enough to find the row.
  walletRow(address: string) {
    return this.walletsSection.locator("tr", { hasText: address.slice(0, 6) });
  }

  webhookRow(address: string) {
    return sectionByHeading(this.page, "Webhook history").locator("tr", { hasText: address.slice(0, 6) });
  }

  auditLogRows() {
    return sectionByHeading(this.page, "Audit log").locator("tbody tr");
  }
}
