---
name: closedvps
description: Buys and sets up Hetzner Cloud servers with every inbound port closed (SSH over Tailscale, optional Cloudflare Tunnel, Claude Code, Codex) using the closedvps CLI, lists them and deletes them. Use when the user wants a new Hetzner VPS, cloud server or remote dev box, a locked-down server reachable only over Tailscale, a server to run Claude Code or Codex on, to keep trying to buy while Hetzner is sold out, to check which of their Hetzner servers the internet can reach, to get SSH back when Tailscale is broken, or to get rid of a closedvps server.
---

# closedvps

Buying a server costs the user money and deleting one destroys its disk. Every buy goes through the checklist
below, and every delete is asked for by name, each time.

## Before the first run

If `closedvps version` fails, install it:

- macOS, Linux: `curl -fsSL https://closedvps.com/install.sh | sh`
- Windows: `irm https://closedvps.com/install.ps1 | iex`

`closedvps upgrade` updates a copy the install scripts put there.

Your shell only sees environment variables that were set before your session started, and secrets never go
in the chat, in AskUserQuestion or in files. So the simplest setup is for the user to run `closedvps setup`
once in their own terminal: it asks for the Hetzner token (Read & Write) and saves it in their password
store, and it can save a Tailscale OAuth client, which makes a one-off key per server and checks their policy
first. Both then work from your shell. Check with `closedvps create -n --json`: a `stopped` event with
`needs_key` names what's missing.

Without saved ones, the user exports these in the terminal they start you from:

- `HCLOUD_TOKEN`
- `TS_OAUTH_CLIENT_ID` and `TS_OAUTH_CLIENT_SECRET`, or `TS_AUTHKEY`: a one-off key for one server, reusable
  with a one-day expiry for `--count`, pre-approved when the tailnet approves devices. The server asks for
  `tag:closedvps` itself, which works once their policy defines it (closedvps.com/tailscale).
- Optionally `CLOUDFLARE_TUNNEL_TOKEN`, and `CLOSEDVPS_NOTIFY` (alert links, if none are saved).

## Buying servers

Copy this checklist and tick it off:

```
- [ ] 1. closedvps types --json: what's in stock and what it costs
- [ ] 2. agree with the user on what it's for (--claude, --codex), types, locations, per-server cap, total cap, count
- [ ] 3. closedvps create -n --json <agreed flags>: show the user the plan event
- [ ] 4. the user says yes to that plan
- [ ] 5. start the real run in the background (below) and watch it
- [ ] 6. on ready, give the user the next commands; on a stop, say what's billing
```

In step 2 ask with AskUserQuestion, one decision per question, with prices from step 1. Without a terminal,
`create` asks nothing and uses the answers saved by `closedvps setup`, or the defaults (nbg1, fsn1, hel1; any
shared type cheapest first; 10 a month per server; no total cap; no backups; nothing installed beyond
Tailscale). Pass every agreed choice as a flag so the plan says what will happen. `--claude` and `--codex` only
buy types with 4 GB or more. `--show-setup` with `-n` adds the exact cloud-init when the user wants to see it.

In step 3, read the price off `candidates`: the first `in_stock` one is what gets bought now. If none is in
stock the real run waits and retries every minute, which can take days, so say so.

Run step 5 on its own, writing events to a file:

```sh
nohup closedvps create --yes --json <the same flags> > closedvps-events.jsonl 2> closedvps.log &
```

A taken name stops the run (`name_taken`) rather than picking another, so running the same command again never
buys twice. With `--count` above 1 the servers are named `<name>-1` to `<name>-<n>`.

Watch for `bought`, `ready`, `unreachable` and `stopped`, and also for the process exiting, so a crash isn't
read as still waiting. In Claude Code, a Monitor on
`tail -f closedvps-events.jsonl | grep --line-buffered -E '"event":"(bought|ready|unreachable|stopped)"'`
does that; re-arm it when it expires. [references/events.md](references/events.md) has every event, stop code
and exit code.

When a server is ready, give the user the `next` commands from the `ready` event. Signing in to Claude Code or
Codex happens on the server in their browser, so they do it themselves. For anything after that (tmux, cloning
a repo, reaching a dev server) read [references/after-create.md](references/after-create.md).

The user gets alerts on their own (desktop, and their alert links), so don't send duplicates. Tell them what
happened when it matters, most of all when something is billing and not finished.

## Other commands

- `closedvps list --json`: every server in the Hetzner project, made by closedvps or not, with its price,
  whether the internet can reach it (`internet`, rules in `firewall`), whether it's on the tailnet when that
  can be seen, and `leftover_firewalls`, closedvps firewalls with no server.
- `closedvps delete <name> --confirm <name> --json`: deletes a server closedvps made, with its disk, backups
  and firewall, or a leftover firewall. For several names, `--confirm` takes how many. It never deletes a
  server closedvps didn't make. Ask the user first, naming each server, and remind them to remove it from
  their Tailscale admin too.
- `closedvps open-ssh <name>` / `closedvps close-ssh <name>`: temporary SSH from the caller's IP when
  Tailscale is broken. Always close it afterwards. The README's "If Tailscale stops working" has the rest,
  down to Hetzner's web console.

`closedvps <command> --help` lists all options. Your runs never get the daily "newer release" note, since it
only shows in a terminal and never with `--json` or `--yes`.
