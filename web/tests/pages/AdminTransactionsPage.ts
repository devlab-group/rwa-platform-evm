import type { Page } from "@playwright/test";

export class AdminTransactionsPage {
  constructor(private readonly page: Page) {}

  async goto(): Promise<void> {
    await this.page.goto("/admin/transactions");
  }

  row(txHash: string) {
    return this.page.locator("tr", { hasText: txHash });
  }

  badge(txHash: string) {
    return this.row(txHash).locator(".badge");
  }
}
