// Every indexed page, once: the layout's tags and schema, the sitemap and llms.txt all read it.
// `modified` is the date of the last change to what the page says (Google only trusts lastmod that does this),
// so bump it with a content edit and leave it for a typo or styling change.
export const site = "https://closedvps.com";

export const org = { "@type": "Organization", name: "ClosedVPS", url: `${site}/`, sameAs: ["https://github.com/tulayha/closedvps"] };

export const pages = [
  {
    path: "/",
    title: "ClosedVPS: a Hetzner VPS even when it's sold out, no open ports",
    name: "ClosedVPS",
    description:
      "Hetzner CX23 not available? This free CLI checks every minute, buys within your price cap and sets the VPS up with no open ports and SSH over Tailscale.",
    llms: "what it does, what you need, the stock retry, what the server gets, recovery when Tailscale breaks",
    published: "2026-11-19",
    modified: "2026-11-20",
  },
  {
    path: "/why",
    type: "Article",
    title: "Why every port is closed: CVE-2026-98432, NIST, CISA and CIS",
    name: "Why ClosedVPS closes every port",
    description:
      "How CVE-2026-98432 reached servers with open ports in October 2026, and the NIST, CISA and CIS text behind each part of the ClosedVPS setup, quoted.",
    llms: "the October 2026 Linux bug (CVE-2026-98432) and, for each part of the setup, the NIST, CISA, CIS or vendor text that asks for it, quoted, plus where it falls short of CIS",
    published: "2026-11-20",
    modified: "2026-11-20",
  },
  {
    path: "/guide",
    type: "TechArticle",
    title: "Harden a Hetzner VPS with Tailscale: no open ports, step by step",
    name: "How to lock down a Hetzner VPS by hand",
    description:
      "Hetzner VPS hardening by hand: a cloud firewall with no inbound rules, SSH only over Tailscale, no passwords, automatic security updates and Cloudflare Tunnel, in the order that won't lock you out.",
    llms: "the firewall, Tailscale, SSH and update steps in the Hetzner Console, without ClosedVPS",
    published: "2026-11-18",
    modified: "2026-11-20",
  },
  {
    path: "/tailscale",
    type: "TechArticle",
    title: "Tailscale ACL for a VPS: tag it so it can't reach your laptop",
    name: "Tailscale for a VPS",
    description:
      "A new tailnet lets every device reach every other one. Tag your VPS and change one rule so your devices reach it and it can't open connections back.",
    llms: "the tailnet policy change (tagOwners and src autogroup:member), the auth key, an optional OAuth client for closedvps setup, tagging a running server, and what changes",
    published: "2026-11-20",
    modified: "2026-11-20",
  },
  {
    path: "/claude-code",
    type: "TechArticle",
    title: "Run Claude Code on a Hetzner VPS with no open ports",
    name: "Claude Code on a Hetzner VPS",
    description:
      "A Hetzner VPS for Claude Code with nothing open to the internet: SSH over Tailscale, the sandbox working on Ubuntu 24.04, and Remote Control from your phone.",
    llms: "picking a server, `--claude`, logging in, Remote Control from a phone, opening dev servers over Tailscale, and the manual steps",
    published: "2026-11-20",
    modified: "2026-11-20",
  },
  {
    path: "/codex",
    type: "TechArticle",
    title: "Run OpenAI Codex CLI on a Hetzner VPS with no open ports",
    name: "Codex on a Hetzner VPS",
    description:
      "A Hetzner VPS for OpenAI's Codex CLI with nothing open to the internet: SSH over Tailscale, the bubblewrap sandbox working on Ubuntu 24.04, and device-code login.",
    llms: "picking a server, `--codex`, device-code login, the bubblewrap sandbox on Ubuntu 24.04, and the manual steps",
    published: "2026-11-20",
    modified: "2026-11-20",
  },
  {
    path: "/sold-out",
    type: "TechArticle",
    title: "Hetzner CX23 not available? How to get one anyway",
    name: "When Hetzner is sold out",
    description:
      "Why Hetzner shows CX23, CAX11 and its other cost-optimized servers as unavailable, what the in-stock plans cost instead, and how to buy one the minute it's back.",
    llms: "why CX and CAX types show unavailable since June 2026, what in-stock plans cost, and buying one when it's back",
    published: "2026-11-20",
    modified: "2026-11-20",
  },
];

export const page = (path) => pages.find((p) => p.path === path);

// The Markdown copy astro-llms-md writes for each page.
export const markdown = (path) => (path === "/" ? "/index.md" : `${path}.md`);

// The visible "Updated" line under an article's h1. Google wants the date people see to match dateModified.
export const dated = (path, body) => {
  const { modified } = page(path);
  const shown = new Date(modified).toLocaleDateString("en-GB", { day: "numeric", month: "long", year: "numeric", timeZone: "UTC" });
  return body.replace("</h1>", `</h1>\n  <p class="updated">Updated <time datetime="${modified}">${shown}</time></p>`);
};
