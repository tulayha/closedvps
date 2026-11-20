// The example terminal on the home page, built from testdata/example-run.txt in the latest closedvps release: the
// output of a whole create that the CLI's tests record, so the page shows what the released CLI prints.
// CLOSEDVPS_REF picks another tag or branch.
const repo = "tulayha/closedvps";
const get = async (url) => {
  const res = await fetch(url, { headers: { "user-agent": "closedvps-site-build" } });
  if (!res.ok) throw new Error(`${url}: ${res.status}`);
  return res;
};

export async function terminal() {
  // github.com/<repo>/releases/latest redirects to .../releases/tag/<tag>. GitHub's API would say the same, but it
  // rate-limits Cloudflare's shared build machines.
  const tag = process.env.CLOSEDVPS_REF ?? (await get(`https://github.com/${repo}/releases/latest`)).url.split("/").pop();
  const run = (await (await get(`https://raw.githubusercontent.com/${repo}/${tag}/testdata/example-run.txt`)).text()).trimEnd().split("\n");

  const span = (cls) => (t) => `<span class="${cls}">${t}</span>`;
  const D = span("dim"), OK = span("ok"), HL = span("hl"), W = span("warn"), B = span("b");
  const esc = (t) => t.replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;");

  // The plan's rows other than the server are left out to keep the terminal short; everything else is shown as
  // printed. Lines start with gh's symbols: ✓ done, ! warning, - anything else.
  const skip = /^  (Firewall|Backups|Tailscale|Alerts|Total|Installs) /;

  const story = [["cmd", "closedvps create"]];
  let ip = "";
  for (const raw of run) {
    if (skip.test(raw) || (raw === "" && story.length === 1)) continue;
    const line = esc(raw);
    let m;
    if ((m = line.match(/^(  Server +)(\S+)(.*)$/))) {
      story.push(["out", `${m[1]}${B(m[2])}${m[3]}`]);
    } else if (/^  TYPE /.test(line) || /^  Before VAT/.test(line)) {
      story.push(["out", D(line)]);
    } else if ((m = line.match(/^(  )(\S+)(.*?)(\d+\.\d\d)( +)(.*)$/))) {
      story.push(["out", `${m[1]}${B(m[2])}${m[3]}${HL(m[4])}${m[5]}${m[6] === "-" ? D(m[6]) : m[6]}`]);
    } else if ((m = line.match(/^\? (.*\(y\/N\)) (y)$/))) {
      story.push(["out", `${HL("?")} ${m[1]} ${B(m[2])}`]);
    } else if ((m = line.match(/^\? (.*?) *$/))) {
      story.push(["out", `${HL("?")} ${m[1]} ${D("********")}`]);
    } else if ((m = line.match(/^- (\d\d:\d\d) (still waiting.*)$/))) {
      story.push(["wait", `${D("-")} ${D(m[1])} ${D(m[2])}`]);
    } else if ((m = line.match(/^✓ (Bought .* for )(\S+ \S+)( \(\S+, )(\S+)\)$/))) {
      ip = m[4];
      story.push(["ok", `${OK("✓")} ${m[1]}${HL(m[2])}${m[3]}${m[4]})`]);
    } else if ((m = line.match(/^- (Waiting for .* to join your tailnet)/))) {
      story.push(["dots", `${D("-")} ${m[1]}`]);
    } else if ((m = line.match(/^✓ (.*)$/))) {
      story.push(["ok", `${OK("✓")} ${m[1]}`]);
    } else if ((m = line.match(/^! (.*)$/))) {
      story.push(["out", `${W("!")} ${m[1]}`]);
    } else if ((m = line.match(/^(  )(\S.*?)( {4}.*)$/))) {
      story.push(["out", `${m[1]}${B(m[2])}${D(m[3])}`]);
    } else if (line === "Next:") {
      story.push(["out", B(line)]);
    } else if ((m = line.match(/^(  )(\S.*)$/))) {
      story.push(["out", `${m[1]}${B(m[2])}`]);
    } else {
      story.push(["out", line]);
    }
  }
  story.push(
    ["cmd", `nmap -Pn ${ip}`],
    ["out", `All 1000 scanned ports on ${ip} are in ignored states.`],
    ["out", `Not shown: ${OK("1000 filtered tcp ports")} (no-response)`],
    ["cmd", "ssh dev@vps"],
    ["prompt", `${OK("dev@vps")}:${HL("~")}$ `],
  );

  const lineHTML = ([k, t]) =>
    k === "cmd" ? `${D("$")} ${B(t)}` : k === "dots" ? `${t}............` : k === "prompt" ? `${t}<span class="cursor"></span>` : t;
  return { story, lines: story.map((s) => `<span class="ln">${lineHTML(s)}\n</span>`).join("") };
}
