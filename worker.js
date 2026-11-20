// Serves the install scripts from the latest closedvps release, so they always match the binaries they download,
// and counts each download in Analytics Engine: which script, the country and the kind of client. Nothing that
// identifies a person.
const scripts = { "/install.sh": "install.sh", "/install.ps1": "install.ps1" };

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const name = scripts[url.pathname];
    if (!name) return env.ASSETS.fetch(request);
    const res = await fetch(`https://github.com/tulayha/closedvps/releases/latest/download/${name}`, {
      cf: { cacheTtl: 300, cacheEverything: true },
    });
    if (!res.ok) {
      return new Response(`closedvps: couldn't fetch ${name} from GitHub (${res.status}), try again in a minute\n`, {
        status: 502,
        headers: { "content-type": "text/plain; charset=utf-8" },
      });
    }
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

function clientKind(ua) {
  ua = ua.toLowerCase();
  if (ua.startsWith("curl/")) return "curl";
  if (ua.includes("powershell")) return "powershell";
  if (ua.startsWith("wget/")) return "wget";
  if (ua.includes("mozilla/")) return "browser";
  return "other";
}
