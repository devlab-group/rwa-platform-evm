import type { Locator, Page } from "@playwright/test";

/**
 * The screen is built from `<section class="card"><h2>Heading</h2>...</section>`
 * blocks (see src/styles/_base.scss `.card`). Sections repeat the same button
 * text (Buy and Redemption both have a "Preview quote" button), so page objects
 * scope queries to the owning section by its heading instead of searching the
 * whole page.
 */
export function sectionByHeading(page: Page, heading: string): Locator {
  return page.locator("section.card", { has: page.getByRole("heading", { name: heading, exact: true }) });
}
