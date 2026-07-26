import type { Page } from "@playwright/test";
import { sectionByHeading } from "../fixtures/helpers";

export class AdminSetupPage {
  constructor(private readonly page: Page) {}

  async goto(): Promise<void> {
    await this.page.goto("/admin/setup");
  }

  private get profileSection() {
    return sectionByHeading(this.page, "Asset Profile");
  }

  private get deploySection() {
    return sectionByHeading(this.page, "Deployment");
  }

  async validateProfile(): Promise<void> {
    await this.profileSection.getByRole("button", { name: "Validate profile" }).click();
  }

  async createProfile(): Promise<void> {
    await this.profileSection.getByRole("button", { name: "Create & persist profile" }).click();
  }

  /**
   * Fills the discrete profile fields then validates + creates in one call —
   * the common path for deploy-focused specs. projectId (from the server config)
   * and profileVersion (pinned 1.0) aren't entered here; read the projectId back
   * with `generatedProjectId()`.
   */
  async createProfileFor(fields: {
    tokenUnit: string;
    tokenDecimals: number;
    assetType?: string;
    recordIdLabel?: string;
    assetSchema?: object;
  }): Promise<void> {
    await this.page.locator("#assetType").fill(fields.assetType ?? "commodity");
    await this.page.locator("#tokenUnit").fill(fields.tokenUnit);
    await this.page.locator("#tokenDecimals").fill(String(fields.tokenDecimals));
    await this.page.locator("#recordIdLabel").fill(fields.recordIdLabel ?? "Serial number");
    await this.page
      .locator("#assetSchema")
      .fill(JSON.stringify(fields.assetSchema ?? {}, null, 2));
    await this.validateProfile();
    await this.createProfile();
  }

  createdProfileStatus() {
    return this.profileSection.getByRole("status");
  }

  /** The deployment's project ID (from the server config), read from the create
   * form (before create) or the persisted-profile summary (after create) — both
   * show a "Project ID" row. */
  async generatedProjectId(): Promise<string> {
    const row = this.profileSection
      .locator(".tx-preview__row")
      .filter({ hasText: "Project ID" });
    return ((await row.locator("dd").first().textContent()) ?? "").trim();
  }

  async fillDeployForm(fields: {
    name: string;
    symbol: string;
    quoteToken: string;
    purchasePricePerWholeToken?: string;
    redemptionPricePerWholeToken?: string;
    admin: string;
    auditor: string;
    complianceOperator?: string;
    pricer?: string;
    treasurer?: string;
    redemptionManager?: string;
    treasury?: string;
  }): Promise<void> {
    await this.page.locator("#name").fill(fields.name);
    await this.page.locator("#symbol").fill(fields.symbol);
    await this.page.locator("#quoteToken").fill(fields.quoteToken);
    await this.page.locator("#purchasePrice").fill(fields.purchasePricePerWholeToken ?? "1000000");
    await this.page.locator("#redemptionPrice").fill(fields.redemptionPricePerWholeToken ?? "950000");
    await this.page.locator("#admin").fill(fields.admin);
    await this.page.locator("#auditor").fill(fields.auditor);
    await this.page.locator("#complianceOperator").fill(fields.complianceOperator ?? fields.admin);
    await this.page.locator("#pricer").fill(fields.pricer ?? fields.admin);
    await this.page.locator("#treasurer").fill(fields.treasurer ?? fields.admin);
    await this.page.locator("#redemptionManager").fill(fields.redemptionManager ?? fields.admin);
    await this.page.locator("#treasury").fill(fields.treasury ?? fields.admin);
  }

  async reviewDeployment(): Promise<void> {
    await this.deploySection.getByRole("button", { name: "Review deployment" }).click();
  }

  async confirmDeploy(): Promise<void> {
    await this.deploySection.getByRole("button", { name: "Confirm deploy" }).click();
  }

  deployedConfirmation() {
    return this.page.getByText(/Deployment broadcast for project/);
  }

  currentProjectSection() {
    return sectionByHeading(this.page, "Current project");
  }
}
