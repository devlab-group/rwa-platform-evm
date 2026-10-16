import type { Page } from "@playwright/test";
import { sectionByHeading } from "../fixtures/helpers";

export class AdminIncomePage {
  constructor(private readonly page: Page) {}

  async goto(): Promise<void> {
    await this.page.goto("/admin/income");
  }

  async connectWallet(): Promise<void> {
    await this.page.getByRole("button", { name: "Connect wallet" }).click();
    await this.page.getByRole("button", { name: "Disconnect" }).waitFor();
  }

  prepareSection() {
    return sectionByHeading(this.page, "Prepare a distribution");
  }

  sendSection() {
    return sectionByHeading(this.page, "Send distribution on-chain");
  }

  extendSection() {
    return sectionByHeading(this.page, "Extend claim deadline");
  }

  distributionsSection() {
    return sectionByHeading(this.page, "Distributions");
  }

  /** Fills the record block and total; the deadline defaults to a valid value */
  async prepare(recordBlock: string, total: string): Promise<void> {
    const section = this.prepareSection();
    await section.getByLabel("Record block").fill(recordBlock);
    await section.getByLabel(/Total to distribute/).fill(total);
    await section.getByRole("button", { name: "Prepare distribution" }).click();
  }

  row(label: string) {
    return this.distributionsSection().locator("tbody tr", { hasText: label });
  }
}
