# closedvps.com

The website for [ClosedVPS](https://github.com/tulayha/closedvps). This branch holds only the site; the CLI is on
`main`.

Plain HTML and one stylesheet in `public/`, served as Cloudflare Workers static assets. `src/worker.js` handles
only `/install.sh` and `/install.ps1`: it fetches them from the latest GitHub release and counts each download in
Analytics Engine. The scripts themselves live on `main` in `install/`.

`npm run build` rewrites the example terminal on the home page from `testdata/example-run.txt` in the latest
release (set `CLOSEDVPS_REF` to use another tag or branch). Cloudflare Workers Builds runs it, then
`npx wrangler deploy`, for every push to this branch. `npm run dev` serves the site locally.

`scripts/og.mjs` draws `public/og.png`, the link preview, from the home page's headline and diagram.
