import { expect, mockOnly, test } from "../fixtures/fixtures";
import { AdminRedemptionsPage } from "../pages/AdminRedemptionsPage";
import { AdminSecurityPage } from "../pages/AdminSecurityPage";
import { AdminTransactionsPage } from "../pages/AdminTransactionsPage";
import type { components } from "../../src/lib/api-types";

/** An affirmative "this WILL be paid" claim — never allowed anywhere on a Pending/Funded view. */
const AFFIRMATIVE_GUARANTEE = /\bis\s+guaranteed\b|\bguaranteed\s+payment\b|\bpayment\s+is\s+guaranteed\b/i;

type TransactionChainStatus = NonNullable<components["schemas"]["Transaction"]["status"]>;

const BENEFICIARY = "0x1234561234561234561234561234561234561234";

test.describe("Distinct UI states", () => {
  mockOnly();

  test("redemption list shows Pending/Funded/Claimable/Completed/Rejected/Cancelled distinctly", async ({
    page,
    api,
  }) => {
    const now = Math.floor(Date.now() / 1000);
    const base = {
      beneficiary: BENEFICIARY,
      rwaAmount: "100000000000000000",
      quoteAmount: "95000",
      createdAt: now,
      timeoutAt: now + 3600,
      beneficiaryAllowed: true,
    };
    api.redemptions.push(
      { ...base, id: "r-pending", status: "Pending", claimable: false, confirmations: 0 },
      { ...base, id: "r-funded", status: "Funded", claimable: false, confirmations: 2 },
      { ...base, id: "r-claimable", status: "Funded", claimable: true, confirmations: 20 },
      { ...base, id: "r-completed", status: "Completed", claimable: false, confirmations: 30 },
      { ...base, id: "r-rejected", status: "Rejected", claimable: false, confirmations: 30 },
      { ...base, id: "r-cancelled", status: "Cancelled", claimable: false, confirmations: 30 },
    );

    const redemptions = new AdminRedemptionsPage(page);
    await redemptions.goto();

    const expectations: Array<[string, string]> = [
      ["r-pending", "Pending"],
      ["r-funded", "Funded"],
      ["r-claimable", "Claimable"],
      ["r-completed", "Completed"],
      ["r-rejected", "Rejected"],
      ["r-cancelled", "Cancelled"],
    ];
    for (const [id, expectedStatus] of expectations) {
      await expect(redemptions.row(id).locator(".badge[data-status]")).toHaveAttribute(
        "data-status",
        expectedStatus,
      );
    }

    // Funded (unconfirmed) and Claimable (confirmed) must never share a label —
    // that's the whole point of splitting them out.
    await expect(redemptions.row("r-funded").locator(".badge")).not.toHaveText("Claimable");
    await expect(redemptions.row("r-claimable").locator(".badge")).toHaveText("Claimable");
  });

  test("transactions list shows unconfirmed/mined/confirmed/replaced/reverted/reorged distinctly", async ({
    page,
    api,
  }) => {
    const kinds: Array<{ txHash: string; status: TransactionChainStatus; labelSubstring: string }> = [
      { txHash: "tx-pending", status: "pending", labelSubstring: "Unconfirmed" },
      { txHash: "tx-mined", status: "mined", labelSubstring: "Mined" },
      { txHash: "tx-confirmed", status: "confirmed", labelSubstring: "Confirmed" },
      { txHash: "tx-replaced", status: "replaced", labelSubstring: "Replaced" },
      { txHash: "tx-reverted", status: "reverted", labelSubstring: "Reverted" },
      { txHash: "tx-reorged", status: "reorged", labelSubstring: "Reorged" },
    ];
    for (const { txHash, status } of kinds) {
      api.transactions.push({ txHash, kind: "mint", status, blockNumber: 100 });
    }

    const transactions = new AdminTransactionsPage(page);
    await transactions.goto();

    for (const { txHash, labelSubstring } of kinds) {
      await expect(transactions.badge(txHash)).toContainText(labelSubstring);
    }

    // Reverted and reorged are visually distinct (danger vs warning tone) —
    // never collapsed into a single "failed" bucket.
    const revertedClass = await transactions.badge("tx-reverted").getAttribute("class");
    const reorgedClass = await transactions.badge("tx-reorged").getAttribute("class");
    expect(revertedClass).not.toBe(reorgedClass);

    // Beyond the per-row badge, a page-level alert calls out that something
    // needs attention — a reader shouldn't have to notice one red badge in
    // a long table to know a reorg happened. The banner covers every
    // operator-attention state, not just reverted/reorged; here reverted +
    // reorged are the two flagged rows.
    await expect(page.getByRole("alert")).toContainText("2 transactions need operator attention");
  });

  test("transactions list has no attention banner when nothing reverted or reorged", async ({
    page,
    api,
  }) => {
    api.transactions.push(
      { txHash: "tx-ok-1", kind: "mint", status: "confirmed", blockNumber: 100 },
      { txHash: "tx-ok-2", kind: "burn", status: "pending", blockNumber: 101 },
    );

    const transactions = new AdminTransactionsPage(page);
    await transactions.goto();

    await expect(page.getByRole("alert")).toHaveCount(0);
  });

  test("a Pending redemption is never described as guaranteed", async ({ page, api }) => {
    const now = Math.floor(Date.now() / 1000);
    // Numeric id: funding does BigInt(id) to build the uint256 arg.
    api.redemptions.push({
      id: "7",
      beneficiary: BENEFICIARY,
      rwaAmount: "100000000000000000",
      quoteAmount: "95000",
      status: "Pending",
      claimable: false,
      createdAt: now,
      timeoutAt: now + 3600,
      beneficiaryAllowed: true,
      confirmations: 0,
    });

    // Admin: the list header carries the disclaimer, and nothing on the admin
    // funding path — including after the wallet broadcast completes — may make
    // an affirmative guarantee claim.
    const redemptions = new AdminRedemptionsPage(page);
    await redemptions.goto();
    await expect(page.getByText(/never guaranteed until claimed/)).toBeVisible();
    await expect(page.locator("body")).not.toContainText(AFFIRMATIVE_GUARANTEE);

    await redemptions.connectWallet();
    await redemptions.manage("7");
    await redemptions.fundRedemption("7");
    await expect(redemptions.detailSection.getByText("Redemption funded.")).toBeVisible();
    await expect(page.locator("body")).not.toContainText(AFFIRMATIVE_GUARANTEE);

  });

  test("Security shows Paused distinctly from Active", async ({ page, api }) => {
    const security = new AdminSecurityPage(page);

    api.project.paused = false;
    await security.goto();
    await expect(security.pausedBadge()).toHaveText("Active");

    api.project.paused = true;
    await security.goto();
    await expect(security.pausedBadge()).toHaveText("Paused");
  });

  // The security snapshot must never be presented as guaranteed-current.
  // `securityAsOfBlock`/`securityAsOfTime`/`securityStale` may not be present
  // in api/openapi.yaml's Project schema yet, so they're injected here as loose
  // extra fields on the mock's Project — exactly how an older/newer server
  // mismatch would show up against the real client.
  test("Security surfaces the source block, as-of time, and a stale warning when the server provides them", async ({
    page,
    api,
  }) => {
    Object.assign(api.project, {
      securityAsOfBlock: 123456,
      securityAsOfTime: new Date(Date.now() - 5 * 60_000).toISOString(),
      securityStale: true,
    });

    const security = new AdminSecurityPage(page);
    await security.goto();

    const notice = page.getByRole("alert").filter({ hasText: "as of" });
    await expect(notice).toContainText("Possibly stale");
    await expect(notice).toContainText("block 123456");
    await expect(notice).toContainText("5m ago");
  });

  test("Security omits the staleness banner against an older server that hasn't shipped the fields", async ({
    page,
    api,
  }) => {
    // Explicitly absent — simulates an older server response without these fields.
    expect((api.project as unknown as Record<string, unknown>).securityAsOfBlock).toBeUndefined();

    const security = new AdminSecurityPage(page);
    await security.goto();

    await expect(security.statusSection().getByText(/as of/i)).toHaveCount(0);
    await expect(page.getByText(/Possibly stale/)).toHaveCount(0);
  });
});
