import type { Page } from "@playwright/test";
import { sectionByHeading } from "../fixtures/helpers";

export class AdminInventorySalesPage {
  constructor(private readonly page: Page) {}

  async goto(): Promise<void> {
    await this.page.goto("/admin/inventory-sales");
  }

  /** Connect through the header: the seeded session does not connect a wallet */
  async connectWallet(): Promise<void> {
    await this.page.getByRole("button", { name: "Connect wallet" }).click();
    await this.page.getByRole("button", { name: "Disconnect" }).waitFor();
  }

  inventorySection() {
    return sectionByHeading(this.page, "Inventory");
  }

  /** The rwa-v4 purchase orders table card */
  ordersSection() {
    return sectionByHeading(this.page, "Purchase orders");
  }

  orderRow(id: string) {
    return this.ordersSection().locator("tr", {
      has: this.page.getByRole("cell", { name: id, exact: true }),
    });
  }

  async manageOrder(id: string): Promise<void> {
    await this.orderRow(id).getByRole("button", { name: "Manage" }).click();
  }

  /** The open Order detail card */
  orderDetail(id: string) {
    return sectionByHeading(this.page, `Order ${id}`);
  }
}
