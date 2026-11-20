---
name: closedvps
description: Buy and set up Hetzner Cloud servers with every inbound port closed (SSH over Tailscale, optional Cloudflare Tunnel, Claude Code, Codex) using the closedvps CLI, and delete them. Use when the user wants a new Hetzner VPS or several, a safe or locked-down server, a server reachable only over Tailscale, a server for Claude Code or Codex, to keep trying to buy while Hetzner is sold out, or to get rid of a closedvps server.
---

# closedvps

Buying a server costs the user money: agree on type, location, per-server cap, total cap and count with them,
and show `closedvps create -n` (dry run) output before the real run. Deleting one destroys its disk: ask the
user every time, naming the server.

Install if `closedvps version` fails:

- macOS, Linux: `curl -fsSL https://closedvps.com/install.sh | sh`
- Windows: `irm https://closedvps.com/install.ps1 | iex`

`closedvps upgrade` updates a copy the install scripts put there. Your runs never get the daily "newer
release" note, since it only shows in a terminal and never with `--json` or `--yes`.

Ask the user to export these in the shell you use (don't put them in files or the chat):

- `HCLOUD_TOKEN`: Hetzner, Read & Write. Not needed if they saved one.
- Tailscale, one of these. If the user ran `closedvps setup` and saved a Tailscale OAuth client, nothing: it
  makes a one-off key per server and checks their policy first. Or `TS_OAUTH_CLIENT_ID` and
  `TS_OAUTH_CLIENT_SECRET` for the same. Otherwise `TS_AUTHKEY`: one-off for one server, reusable with a
  one-day expiry for `--count`, pre-approved when the tailnet approves devices. The server asks for
  `tag:closedvps` itself, which works once their policy defines it (closedvps.com/tailscale).
- Optionally `CLOUDFLARE_TUNNEL_TOKEN`, and `CLOSEDVPS_NOTIFY` (alert links, if none are saved).

Without a terminal `create` never asks anything. It uses the user's saved answers from `closedvps setup`, or
the defaults (nbg1, fsn1, hel1; any shared type cheapest first; 10 a month per server; no total cap; no
backups; nothing installed beyond Tailscale). Pass agreed choices as flags. A taken name stops the run
(`name_taken`) rather than picking another, so a rerun never buys twice.

Commands:

- `closedvps types`: prices and stock, read-only.
- `closedvps create -n --json [options]`: the plan, buys nothing. `--show-setup` adds the exact cloud-init.
- `closedvps create --yes --json [--count <n>] --type <t1:cap,t2> --location <l1,l2> --max-price <n> [--max-total <n>] [--backups] [--claude] [--codex]`:
  buys the first in-stock matches, retrying every minute until all are bought. With `--count` above 1 the
  servers are named `<name>-1` to `<name>-<n>`. `--claude` and `--codex` only buy types with 4 GB or more.
- `closedvps list --json`: every server in the Hetzner project, made by closedvps or not, with its price,
  whether the internet can reach it (`internet`, rules in `firewall`), whether it's on the tailnet when that
  can be seen, and `leftover_firewalls`, closedvps firewalls with no server.
- `closedvps delete <name> --confirm <name> --json`: deletes a server closedvps made, with its firewall, or a
  leftover firewall. For several names, `--confirm` takes how many. It never deletes a server closedvps
  didn't make.
- `closedvps open-ssh <name>` / `closedvps close-ssh <name>`: temporary SSH from the caller's IP when
  Tailscale is broken. Always close it afterwards.

`closedvps <command> --help` lists all options.

## Watching a long run

A run can wait for days, longer than a background command or a single watch lasts. Start it on its own,
writing events to a file, then watch the file:

```sh
nohup closedvps create --yes --json ... > closedvps-events.jsonl 2> closedvps.log &
```

Each line of `closedvps-events.jsonl` is one JSON event:

- `plan`: `servers` (the names), `setup` (what first boot installs), `tailscale` (`key`, or `checked` and
  `open` with an OAuth client: `open` means their policy lets `tag:closedvps` reach their devices), `dry_run`,
  and `candidates` with `in_stock`.
- `waiting`: every check, with `bought` and `count`.
- `bought`: server, type, location, price, ipv4.
- `ready`: `ssh`, `tagged` (false means it joined without `tag:closedvps` and can reach their devices until
  they tag it), and `next`, the commands for the user to run.
- `unreachable`: a server that didn't join or doesn't answer SSH, with `reason`.
- `stopped`: the last event of any run that didn't finish, with `code`, `reason`, and `billing`, the servers
  bought that are running and costing money.
- `deleted`: from `delete`, with `server`, `firewall` and `price`.

Watch for `bought`, `ready`, `unreachable` and `stopped`, and also for the process exiting, so a crash isn't
read as still waiting. In Claude Code, a Monitor on
`tail -f closedvps-events.jsonl | grep --line-buffered -E '"event":"(bought|ready|unreachable|stopped)"'`
does that; re-arm it when it expires.

Stop codes in `stopped`:

- `needs_key`: a secret or SSH key is missing; `reason` names the variable.
- `needs_yes`: no `--yes` without a terminal.
- `declined`: the user answered no in the terminal.
- `bad_token`: Hetzner or Tailscale refused the credentials, or the Hetzner token is read-only.
- `no_tag`: their Tailscale policy doesn't define `tag:closedvps` (OAuth client only).
- `too_small`: every type asked for is under 4 GB with `--claude` or `--codex`.
- `over_cap`: nothing fits the price caps, or `--max-total` is reached.
- `name_taken`: a server with that name exists.
- `usage`: a bad flag value.
- `interrupted`: stopped with Ctrl+C or a signal.
- `partial`: some servers bought, then it stopped.
- `untagged`, `unreachable`: bought, but a server joined untagged or didn't come up.
- `failed`: anything else; read `reason`.

How `create` ends, as `closedvps create --help` prints it:

```
Exit codes:
  0    every server bought and set up; when Tailscale runs on this computer (and without --no-wait),
       each one also answered SSH over your tailnet
  1    nothing bought
  3    something bought and billing, but not everything finished
  130  stopped with Ctrl+C before anything was bought
```

On a 3, tell the user which servers are billing (`billing` in `stopped`) and what to do: `closedvps delete
<name>`, or for `untagged`, the fix closedvps printed.

When a server is ready, give the user the `next` commands. Signing in to Claude Code or Codex happens on the
server, in their browser, so they do it themselves.

The user gets alerts on their own (desktop, and their alert links), so don't send them duplicates; tell them
what happened when it matters, especially a 3.
