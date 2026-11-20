import { renameSync, writeFileSync } from "node:fs";
import { defineConfig, fontProviders } from "astro/config";
import sitemap from "@astrojs/sitemap";
import llms from "astro-llms-md";
import { site, pages, markdown } from "./src/site.js";

const bare = (url) => url.replace(/\/$/, "");
const modified = new Map(pages.map((p) => [bare(site + p.path), p.modified]));

export default defineConfig({
  site,
  // why.html served at /why, as before the move to Astro
  build: { format: "file" },
  trailingSlash: "never",
  // Served from the site, with stand-in fonts sized to match so the text barely moves when they arrive. Astro has
  // sizes for Arial, Times New Roman, Roboto and the Mac and Windows UI fonts, which is why Geist's list ends in
  // system-ui; Android has no serif it knows, so the headings there can still move a little on a first visit.
  fonts: [
    { provider: fontProviders.google(), name: "Geist", cssVariable: "--font-geist", weights: [400, 500, 600], styles: ["normal"], subsets: ["latin"], fallbacks: ["sans-serif", "system-ui"] },
    { provider: fontProviders.google(), name: "Geist Mono", cssVariable: "--font-geist-mono", weights: [400, 500], styles: ["normal"], subsets: ["latin"], fallbacks: ["ui-monospace", "Menlo", "monospace"] },
    {
      provider: fontProviders.google(),
      name: "Newsreader",
      cssVariable: "--font-newsreader",
      weights: [500],
      styles: ["normal", "italic"],
      subsets: ["latin"],
      fallbacks: ["Georgia", "serif"],
      options: { experimental: { variableAxis: { opsz: [["6", "72"]] } } },
    },
  ],
  integrations: [
    sitemap({
      filter: (url) => modified.has(bare(url)),
      serialize: (item) => ({ ...item, lastmod: modified.get(bare(item.url)) }),
    }),
    // Markdown copies of every page and llms-full.txt; llms.txt is src/pages/llms.txt.js.
    // Left out of the copies: navigation, tab buttons, the drawn diagram and the alert mock-ups' app labels.
    llms({
      generateLlmsTxt: false,
      excludeSelectors: ["nav", "footer", "[aria-hidden='true']", ".toc", ".tabs", ".steps", ".bar-top", ".map", ".facts", ".note .meta"],
    }),
    // astro-llms-md names the home page's copy home.md; llmstxt.org names it index.md. Each Markdown copy names
    // its page as canonical and llms-full.txt stays out of search, so search engines see one copy of each page.
    {
      name: "md-copies",
      hooks: {
        "astro:build:done": ({ dir }) => {
          renameSync(new URL("home.md", dir), new URL("index.md", dir));
          const headers = pages.map((p) => `${markdown(p.path)}\n  Link: <${site}${p.path}>; rel="canonical"\n`);
          writeFileSync(new URL("_headers", dir), [...headers, "/llms-full.txt\n  X-Robots-Tag: noindex\n"].join(""));
        },
      },
    },
  ],
});
