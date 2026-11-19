// Counts install-script downloads in Analytics Engine, then serves the script.
// Stored per download: which script, the country and the kind of client. Nothing that identifies a person.
export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const res = await env.ASSETS.fetch(request);
    if (res.ok && request.method === "GET") {
      env.INSTALLS.writeDataPoint({
        indexes: [url.pathname],
        blobs: [url.pathname, request.cf?.country ?? "", clientKind(request.headers.get("user-agent") ?? "")],
        doubles: [1],
      });
    }
    const out = new Response(res.body, res);
    out.headers.set("content-type", "text/plain; charset=utf-8");
    out.headers.set("cache-control", "public, max-age=300");
    return out;
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
