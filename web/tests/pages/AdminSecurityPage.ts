import type { Page } from "@playwright/test";
import { sectionByHeading } from "../fixtures/helpers";

export class AdminSecurityPage {
  constructor(private readonly page: Page) {}

  async goto(): Promise<void> {
    await this.page.goto("/admin/security");
  }

  statusSection() {
    return sectionByHeading(this.page, "Status");
  }

  pausedBadge() {
    return this.statusSection().locator(".badge", { hasText: /^(Paused|Active)$/ });
  }

  roleHoldersSection() {
    return sectionByHeading(this.page, "Role holders");
  }
}
