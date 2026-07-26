import type { Page } from "@playwright/test";
import { sectionByHeading } from "../fixtures/helpers";

export class AdminAssetsPage {
  constructor(private readonly page: Page) {}

  async goto(): Promise<void> {
    await this.page.goto("/admin/assets");
  }

  private get createSection() {
    return sectionByHeading(this.page, "Create record");
  }

  private get signatureSection() {
    return sectionByHeading(this.page, "Auditor signature upload & mint");
  }

  private get recordsSection() {
    return sectionByHeading(this.page, "Records");
  }

  async createRecord(fields: { recordId: string; amount: string }): Promise<void> {
    await this.page.locator("#recordId").fill(fields.recordId);
    await this.page.locator("#amount").fill(fields.amount);
    await this.createSection.getByRole("button", { name: "Create record" }).click();
  }

  /**
   * Uploads the auditor's signed-result.json (built from the given fields) and
   * relays. The form no longer takes the signature fields individually — they
   * are read from the uploaded file (shared/schemas/signed-result.schema.json).
   */
  async uploadSignature(fields: {
    recordId: string;
    auditor: string;
    primaryType: string;
    typedDataDigest: string;
    signature: string;
    signedAt?: string;
    formatVersion?: string;
  }): Promise<void> {
    await this.page.locator("#sigRecordId").fill(fields.recordId);
    const signedResult = {
      formatVersion: fields.formatVersion ?? "1.0",
      auditor: fields.auditor,
      primaryType: fields.primaryType,
      typedDataDigest: fields.typedDataDigest,
      signature: fields.signature,
      signedAt: fields.signedAt ?? "2026-01-01T00:00:00Z",
    };
    await this.page.locator("#signedResultFile").setInputFiles({
      name: "signed-result.json",
      mimeType: "application/json",
      buffer: Buffer.from(JSON.stringify(signedResult)),
    });
    await this.signatureSection.getByRole("button", { name: "Upload signature & mint" }).click();
  }

  /** Uploads a raw file body to the signed-result input (for malformed-file specs). */
  async uploadSignedResultFile(name: string, contents: string): Promise<void> {
    await this.page.locator("#signedResultFile").setInputFiles({
      name,
      mimeType: "application/json",
      buffer: Buffer.from(contents),
    });
  }

  recordRow(recordId: string) {
    return this.recordsSection.locator("tr", { hasText: recordId });
  }
}
