# closedvps.com

The website for [ClosedVPS](https://github.com/tulayha/closedvps). This branch holds only the site; the CLI is on
`main`.

The site is built with [Astro](https://astro.build) into static files, served as Cloudflare Workers static assets.

- `src/site.js` lists every page once: title, description, dates, and its line in llms.txt. The page tags,
  schema, sitemap and llms.txt all read it. Bump a page's `modified` date when what it says changes; articles show it as their "Updated" line.
- `src/layouts/Layout.astro` is the head, header and footer every page shares.
- `src/body/*.html` is each page's content, and `src/pages/*.astro` puts it in the layout.
- `src/terminal.js` builds the example run on the home page from `testdata/example-run.txt` in the latest
  release, so it shows what the released CLI prints. Set `CLOSEDVPS_REF` to use another tag or branch.
- `public/` is copied as is: the stylesheet, icon, link preview image and robots.txt.

The build also writes the sitemap (`@astrojs/sitemap`), a Markdown copy of every page and `llms-full.txt`
(`astro-llms-md`), and `llms.txt` (`src/pages/llms.txt.js`).

`worker.js` handles only `/install.sh` and `/install.ps1`: it fetches them from the latest GitHub release and
counts each download in Analytics Engine. The scripts themselves live on `main` in `install/`.

`npm run build` builds into `dist/`, and `npm run dev` builds and serves it with the Worker. Cloudflare Workers
Builds runs `npm run build` and then `npx wrangler deploy` for this branch. `scripts/og.mjs` redraws
`public/og.png` from the built home page.
