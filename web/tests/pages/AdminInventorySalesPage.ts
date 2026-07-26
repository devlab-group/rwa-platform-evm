import type { Page } from "@playwright/test";
import { sectionByHeading } from "../fixtures/helpers";

export class AdminInventorySalesPage {
  constructor(private readonly page: Page) {}

  async goto(): Promise<void> {
    await this.page.goto("/admin/inventory-sales");
  }

  inventorySection() {
    return sectionByHeading(this.page, "Inventory");
  }
}
