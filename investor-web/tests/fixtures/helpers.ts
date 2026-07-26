import type { Locator, Page } from "@playwright/test";

/**
 * Every screen is built from `<section class="card"><h2>Heading</h2>...</section>`
 * blocks (see src/styles/_base.scss `.card`). Several screens repeat the same
 * field label or button text across sections (e.g. Investor's Buy and
 * Redemption both have a "Preview quote" button), so page objects scope
 * queries to the owning section by its heading rather than searching the
 * whole page.
 */
export function sectionByHeading(page: Page, heading: string): Locator {
  return page.locator("section.card", { has: page.getByRole("heading", { name: heading, exact: true }) });
}
