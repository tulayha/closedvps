// Draws public/og.png, the 1200x630 link preview, from the built home page's headline and diagram (run npm run build first), so the image
// changes when they do. Needs a Chrome or Chromium: CHROME=/path/to/chrome node scripts/og.mjs
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";

const here = new URL("..", import.meta.url).pathname;
const require = createRequire(process.env.PLAYWRIGHT_CORE ?? import.meta.url);
const { chromium } = require("playwright-core");

const index = readFileSync(`${here}dist/index.html`, "utf8");
const pick = (re) => index.match(re)[0];
const brand = pick(/<a class="brand"[\s\S]*?<\/a>/);
const title = pick(/<h1>[\s\S]*?<\/h1>/);
const map = pick(/<div class="map"[\s\S]*?\n<\/div>(?=\n<p class="mapnote">)/);

const html = `<!doctype html><html><head><meta charset="utf-8">
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Geist:wght@400;500;600&family=Geist+Mono:wght@400;500&family=Newsreader:ital,opsz,wght@0,6..72,500;1,6..72,500&display=swap">
<style>${readFileSync(`${here}public/site.css`, "utf8")}
body { width: 1200px; height: 630px; overflow: hidden; display: flex; flex-direction: column; align-items: center; justify-content: center; padding: 0 64px 8px; }
.brand { display: flex; gap: 10px; align-items: center; font-weight: 600; font-size: 24px; text-decoration: none; }
.brand svg.i { width: 26px; height: 26px; }
h1 { margin: 26px 0 0; max-width: none; text-align: center; font-size: 76px; }
.map { margin-top: 44px; width: 860px; zoom: 1.12; }
</style></head><body>${brand}${title}${map}</body></html>`;

const browser = await chromium.launch({ executablePath: process.env.CHROME ?? "/usr/bin/google-chrome" });
const page = await browser.newPage({ viewport: { width: 1200, height: 630 }, colorScheme: "light" });
await page.setContent(html, { waitUntil: "networkidle" });
await page.evaluate(() => document.fonts.ready);
await page.screenshot({ path: `${here}public/og.png` });
await browser.close();
