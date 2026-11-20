// Serves the install scripts from the latest closedvps release, so they always match the binaries they download,
// and answers closedvps's update check (/latest). Each is counted in Analytics Engine with nothing that identifies
// a person: install scripts by script, country and kind of client; update checks by country, check or upgrade,
// version and platform.
const repo = "tulayha/closedvps";
const scripts = { "/install.sh": "install.sh", "/install.ps1": "install.ps1" };

export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    if (url.pathname === "/latest") return latest(request, env, ctx, url);
    const name = scripts[url.pathname];
    if (!name) return env.ASSETS.fetch(request);
    const res = await fetch(`https://github.com/${repo}/releases/latest/download/${name}`, {
      cf: { cacheTtl: 300, cacheEverything: true },
    });
    if (!res.ok) return failed(`couldn't fetch ${name} from GitHub (${res.status})`);
    if (request.method === "GET") {
      env.INSTALLS.writeDataPoint({
        indexes: [url.pathname],
        blobs: [url.pathname, request.cf?.country ?? "", clientKind(request.headers.get("user-agent") ?? "")],
        doubles: [1],
      });
    }
    return new Response(request.method === "HEAD" ? null : res.body, {
      headers: { "content-type": "text/plain; charset=utf-8", "cache-control": "public, max-age=300" },
    });
  },
};

// latest answers in the shape of GitHub's latest-release API (tag_name, html_url, assets), which is what closedvps
// reads. It's built from github.com's redirect and the release's checksums.txt, because GitHub's API rate-limits
// requests from Cloudflare, and kept in Cloudflare's cache for 5 minutes.
async function latest(request, env, ctx, url) {
  // closedvps sends "closedvps/<version> (<os>/<arch>)" and ?for=check or ?for=upgrade.
  const [, version = "", platform = ""] = (request.headers.get("user-agent") ?? "").match(/^closedvps\/(\S+) \((\S+)\)/) ?? [];
  env.INSTALLS.writeDataPoint({
    indexes: ["/latest"],
    blobs: ["/latest", request.cf?.country ?? "", url.searchParams.get("for") ?? "", version, platform],
    doubles: [1],
  });
  const key = new Request(`${url.origin}/latest`);
  const cached = await caches.default.match(key);
  if (cached) return cached;

  const tagged = await fetch(`https://github.com/${repo}/releases/latest`, { redirect: "manual" });
  const tag = (tagged.headers.get("location") ?? "").split("/").pop();
  if (!/^v\d+\.\d+\.\d+/.test(tag)) return failed(`GitHub didn't say which release is newest (${tagged.status})`);
  const download = `https://github.com/${repo}/releases/download/${tag}/`;
  const sums = await fetch(download + "checksums.txt");
  if (!sums.ok) return failed(`couldn't fetch checksums.txt from GitHub (${sums.status})`);
  const names = (await sums.text()).split("\n").map((l) => l.split("  ")[1]).filter(Boolean);
  const body = {
    tag_name: tag,
    html_url: `https://github.com/${repo}/releases/tag/${tag}`,
    assets: ["checksums.txt", ...names].map((name) => ({ name, browser_download_url: download + name })),
  };
  const res = Response.json(body, { headers: { "cache-control": "public, max-age=300" } });
  ctx.waitUntil(caches.default.put(key, res.clone()));
  return res;
}

function failed(why) {
  return new Response(`closedvps: ${why}, try again in a minute\n`, {
    status: 502,
    headers: { "content-type": "text/plain; charset=utf-8" },
  });
}

function clientKind(ua) {
  ua = ua.toLowerCase();
  if (ua.startsWith("curl/")) return "curl";
  if (ua.includes("powershell")) return "powershell";
  if (ua.startsWith("wget/")) return "wget";
  if (ua.includes("mozilla/")) return "browser";
  return "other";
}
