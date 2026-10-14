import type { Locator, Page } from "@playwright/test";

/**
 * Locates the `<section class="card">` whose `<h2>` is `heading`. Screens are
 * built from these cards (src/styles/_base.scss `.card`) and several repeat the
 * same label or button text in different sections, so page objects scope
 * queries to a card by its heading.
 */
export function sectionByHeading(page: Page, heading: string): Locator {
  return page.locator("section.card", { has: page.getByRole("heading", { name: heading, exact: true }) });
}
