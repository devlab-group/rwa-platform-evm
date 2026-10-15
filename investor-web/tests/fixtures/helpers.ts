import type { Locator, Page } from "@playwright/test";

/**
 * Scope page-object queries to each card section's <h2>. Button labels repeat across
 * sections, including Buy and Redemption's "Preview quote". The markup is <section
 * class="card"><h2>Heading</h2>...</section>; see .card in src/styles/_base.scss.
 */
export function sectionByHeading(page: Page, heading: string): Locator {
  return page.locator("section.card", { has: page.getByRole("heading", { name: heading, exact: true }) });
}
