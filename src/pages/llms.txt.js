// /llms.txt in the shape llmstxt.org asks for: H1, summary, notes, then link lists. Pages link to their Markdown
// copies (astro-llms-md writes them); GitHub files link to their raw Markdown.
import { site, pages, markdown } from "../site.js";

const raw = "https://raw.githubusercontent.com/tulayha/closedvps/main";

export function GET() {
  const text = `# ClosedVPS

> Free, open-source CLI that buys a Hetzner Cloud server even while Hetzner is sold out. It retries every minute from the user's own computer or any machine that stays on (there's a Docker image), within price caps per server type and for all servers together, buys several with --count, and alerts the user on the desktop and optionally through ntfy, Discord, Slack, Telegram or a webhook. The server comes up behind a Hetzner firewall with no inbound rules, with SSH only over Tailscale, automatic security updates, Hetzner Backups when asked for, and optionally Claude Code and a Cloudflare Tunnel for websites. macOS, Linux, Windows. MIT.

Install: \`curl -fsSL ${site}/install.sh | sh\` (macOS, Linux) or \`irm ${site}/install.ps1 | iex\` (Windows). Then \`closedvps create\`, and \`closedvps create --dry-run\` to see the whole setup before paying. \`closedvps upgrade\` updates it.

## Docs

${pages.map((p) => `- [${p.name}](${site}${markdown(p.path)}): ${p.llms}`).join("\n")}

## Source

- [README](${raw}/README.md): quick start, everything it creates, security model, getting back in
- [AGENTS.md](${raw}/AGENTS.md): running it from a coding agent without prompts
- [Agent skill](${raw}/skills/closedvps/SKILL.md): instructions an agent loads to run ClosedVPS for a user, including how to watch a long run (--json events, exit codes). In Claude Code: \`/plugin install closedvps --marketplace tulayha/closedvps\`

## Optional

- [Every page in one file](${site}/llms-full.txt): all the pages above as Markdown
`;
  return new Response(text, { headers: { "content-type": "text/plain; charset=utf-8" } });
}
