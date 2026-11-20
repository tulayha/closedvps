import { renameSync } from "node:fs";
import { defineConfig } from "astro/config";
import sitemap from "@astrojs/sitemap";
import llms from "astro-llms-md";
import { site, pages } from "./src/site.js";

const bare = (url) => url.replace(/\/$/, "");
const modified = new Map(pages.map((p) => [bare(site + p.path), p.modified]));

export default defineConfig({
  site,
  // why.html served at /why, as before the move to Astro
  build: { format: "file" },
  trailingSlash: "never",
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
    // astro-llms-md names the home page's copy home.md; llmstxt.org names it index.md.
    {
      name: "home-md",
      hooks: {
        "astro:build:done": ({ dir }) => renameSync(new URL("home.md", dir), new URL("index.md", dir)),
      },
    },
  ],
});
