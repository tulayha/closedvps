// Builds the example terminal on the home page from testdata/example-run.txt, the output of a whole create
// that `go test` records. It rewrites the static lines and the animated story in public/index.html, and the
// plain copy in public/index.md. TestSiteShowsRealOutput checks every closedvps line is real.
import { readFileSync, writeFileSync } from "node:fs";

const here = new URL("..", import.meta.url).pathname;
const run = readFileSync(`${here}../testdata/example-run.txt`, "utf8").trimEnd().split("\n");

const span = (cls) => (t) => `<span class="${cls}">${t}</span>`;
const D = span("dim"), OK = span("ok"), HL = span("hl"), W = span("warn"), B = span("b");
const esc = (t) => t.replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;");

// The plan's summary lines are left out to keep the terminal short; everything else is shown as printed.
const skip = /^(Servers|Firewall .*, no inbound rules$|Setup|Backups  on|Alerts)/;

const story = [["cmd", "closedvps create"]];
let ip = "";
for (const raw of run) {
  if (skip.test(raw) && !raw.includes(" ready, ")) continue;
  const line = esc(raw);
  let m;
  if ((m = line.match(/^(  )(\S+)(.*?)(\d+\.\d\d \w+\/month)$/))) {
    story.push(["out", `${m[1]}${B(m[2])}${m[3]}${HL(m[4])}`]);
  } else if ((m = line.match(/^(Buy it\? \[y\/N\] )(y)$/))) {
    story.push(["out", `${m[1]}${B(m[2])}`]);
  } else if ((m = line.match(/^(.*ready, )(no inbound rules)$/))) {
    story.push(["out", `${m[1]}${OK(m[2])}`]);
  } else if ((m = line.match(/^(\d\d:\d\d:\d\d)  (nothing on your list is in stock) (\(check \d+\))$/))) {
    story.push(["out", `${D(m[1])}  ${W(m[2])} ${D(m[3])}`]);
  } else if (line.startsWith("Trying again")) {
    story.push(["out", D(line)]);
    story.push(["wait", D("... 2 hours later ...")]);
  } else if ((m = line.match(/^(\d\d:\d\d:\d\d)  (.* bought), (.*)$/))) {
    story.push(["out", `${D(m[1])}  ${OK(m[2])}, ${HL(m[3])}`]);
  } else if ((m = line.match(/^(.* is running at )(\S+), (unreachable from the internet)\.$/))) {
    ip = m[2];
    story.push(["out", `${m[1]}${m[2]}, ${OK(m[3])}.`]);
  } else if ((m = line.match(/^(Waiting for .*?)\.+ online$/))) {
    story.push(["dots", m[1]]);
  } else if (line.startsWith("Ready: ")) {
    story.push(["ok", `<span class="b ok">${line}</span>`]);
  } else if (line.startsWith("In the Tailscale admin")) {
    story.push(["out", W(line)]);
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
  k === "cmd" ? `${D("$")} ${B(t)}` : k === "dots" ? `${t}............ ${OK("online")}` : k === "prompt" ? `${t}<span class="cursor"></span>` : t;
const plain = ([k, t]) => {
  const text = lineHTML([k, t]).replace(/<[^>]+>/g, "").replaceAll("&lt;", "<").replaceAll("&gt;", ">").replaceAll("&amp;", "&");
  return k === "prompt" ? null : text;
};

const indexHTML = `${here}public/index.html`;
let page = readFileSync(indexHTML, "utf8");
page = page.replace(/(<pre id="story"[^>]*>)[\s\S]*?(<\/pre>)/, `$1${story.map((s) => `<span class="ln">${lineHTML(s)}</span>`).join("")}$2`);
page = page.replace(/const story = \[.*\];/, () => `const story = ${JSON.stringify(story)};`);
writeFileSync(indexHTML, page);

const indexMD = `${here}public/index.md`;
const md = readFileSync(indexMD, "utf8").replace(
  /```\n\$ closedvps create\n[\s\S]*?```/,
  () => "```\n" + story.map(plain).filter((l) => l !== null).join("\n") + "\n```",
);
writeFileSync(indexMD, md);
