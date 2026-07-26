import { expect, mockOnly, test } from "../fixtures/fixtures";
import { AdminCompliancePage } from "../pages/AdminCompliancePage";

const INVESTOR_ADDRESS = "0x1234561234561234561234561234561234561234";

test.describe("Compliance — allowlist and block", () => {
  mockOnly();

  test("allows a wallet and the table reflects it", async ({ page, api }) => {
    const compliance = new AdminCompliancePage(page);
    await compliance.goto();

    await compliance.setStatus({ address: INVESTOR_ADDRESS, status: "Allowed" });

    await expect(compliance.walletRow(INVESTOR_ADDRESS)).toBeVisible();
    await expect(compliance.walletRow(INVESTOR_ADDRESS).locator(".badge")).toHaveText("Allowed");
    expect(api.wallets.find((w) => w.address === INVESTOR_ADDRESS)?.status).toBe("Allowed");
  });

  test("blocks a wallet and the table reflects it", async ({ page, api }) => {
    const compliance = new AdminCompliancePage(page);
    await compliance.goto();

    await compliance.setStatus({ address: INVESTOR_ADDRESS, status: "Blocked" });

    await expect(compliance.walletRow(INVESTOR_ADDRESS).locator(".badge")).toHaveText("Blocked");
    expect(api.wallets.find((w) => w.address === INVESTOR_ADDRESS)?.status).toBe("Blocked");
  });

  test("shows webhook history and audit log entries", async ({ page, api }) => {
    api.webhooks.push({
      id: "wh-1",
      address: INVESTOR_ADDRESS,
      provider: "acme-kyc",
      outcome: "Allowed",
      receivedAt: new Date().toISOString(),
      applied: true,
    });
    api.auditLogs.push({
      id: "log-1",
      category: "compliance",
      actor: "0xadminadminadminadminadminadminadminadmin",
      action: "SetStatus",
      target: INVESTOR_ADDRESS,
      createdAt: new Date().toISOString(),
    });

    const compliance = new AdminCompliancePage(page);
    await compliance.goto();

    await expect(compliance.webhookRow(INVESTOR_ADDRESS)).toBeVisible();
    await expect(compliance.webhookRow(INVESTOR_ADDRESS)).toContainText("acme-kyc");
    await expect(compliance.auditLogRows()).toHaveCount(1);
    await expect(compliance.auditLogRows().first()).toContainText("SetStatus");
  });
});
