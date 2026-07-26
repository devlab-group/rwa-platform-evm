import { chromium } from "@playwright/test";

const browser = await chromium.launch({ headless: true });
const page = await browser.newPage();
page.on("console", (msg) => console.log("BROWSER:", msg.text()));
await page.goto("http://localhost:4173/admin/setup", { waitUntil: "networkidle" });
const active = await page.evaluate(() => document.activeElement?.tagName);
console.log("Active element tag right after load:", active);
await browser.close();
