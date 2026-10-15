import { readFile } from "node:fs/promises";
import { expect, mockOnly, test } from "../fixtures/fixtures";
import { AdminSecurityPage } from "../pages/AdminSecurityPage";

const SAFE = "0x9a7F3dfDEeBb51eAbBbA3c8eCCf8cF2a983A1C72";

test.describe("Safe batch export", () => {
  mockOnly();

  // Run the real batch download in the built console
  test("a role held by a Safe saves a Transaction Builder batch instead of broadcasting", async ({
    page,
    api,
  }) => {
    api.project.roles = { ...api.project.roles, PAUSER_ROLE: [SAFE] };
    await new AdminSecurityPage(page).goto();

    await page.getByLabel(/Safe holding PAUSER_ROLE/).selectOption(SAFE);
    const downloading = page.waitForEvent("download");
    await page
      .getByRole("button", { name: "Pause trading", exact: true })
      .click();
    const download = await downloading;

    expect(download.suggestedFilename()).toMatch(
      /^safe-batch-chain\d+-pause-trading-\d+\.json$/,
    );
    const batch = JSON.parse(await readFile(await download.path(), "utf8"));
    expect(batch.meta.createdFromSafeAddress).toBe(SAFE);
    expect(batch.meta.checksum).toMatch(/^0x[0-9a-f]{64}$/);
    // pause() selector, sent to the token
    expect(batch.transactions).toEqual([
      { to: api.project.addresses?.token, value: "0", data: "0x8456cb59" },
    ]);
    await expect(page.getByText(/^Saved safe-batch-chain/)).toBeVisible();
  });
});
