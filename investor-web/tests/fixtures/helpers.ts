import type { Locator, Page } from "@playwright/test";
import type { MockState } from "./mock-api";
import { MOCK_WALLET_ADDRESS, setMockForwardPriced } from "./mock-wallet";

/**
 * Scope page-object queries to each card section's <h2>. Button labels repeat across
 * sections, including Buy and Redemption's "Preview quote". The markup is <section
 * class="card"><h2>Heading</h2>...</section>; see .card in src/styles/_base.scss.
 */
export function sectionByHeading(page: Page, heading: string): Locator {
  return page.locator("section.card", { has: page.getByRole("heading", { name: heading, exact: true }) });
}

/**
 * Turns the mocked project into an rwa-v4 oracle project whose orders are priced at a later
 * round, on the server and on the mock chain. Call before goto.
 */
export async function makeForward(api: MockState, page: Page): Promise<void> {
  await setMockForwardPriced(page, true);
  api.project.strategyKind = "Oracle";
  api.project.forwardPriced = true;
  api.project.quoteDecimals = 6;
  api.project.oracle = {
    feed: `0x${"12".repeat(20)}`,
    maxAge: 3600,
    purchaseSpreadBps: 0,
    redemptionSpreadBps: 0,
    feedDecimals: 8,
    dealingCutoff: 7200,
    available: true,
    updatedAt: Math.floor(Date.now() / 1000),
  };
}

/** Lists the mock wallet as an Allowed investor in the compliance registry */
export function allowTheWallet(api: MockState): void {
  api.wallets.push({
    address: MOCK_WALLET_ADDRESS,
    status: "Allowed",
    validUntil: 4102444800,
    ownershipVerified: true,
  });
}
