# ClosedVPS

[![CI](https://github.com/tulayha/closedvps/actions/workflows/ci.yml/badge.svg)](https://github.com/tulayha/closedvps/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/tulayha/closedvps)](https://github.com/tulayha/closedvps/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/tulayha/closedvps)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

ClosedVPS buys Hetzner Cloud servers with a firewall that lets nothing in, and you SSH in over Tailscale. When
Hetzner is sold out it keeps trying, up to the prices you set, and tells you when it has them. It's a single
command that runs on your laptop or any machine that stays on, with no account and no server of ours involved.

## Quick start

```sh
curl -fsSL https://closedvps.com/install.sh | sh    # macOS, Linux
```

```powershell
irm https://closedvps.com/install.ps1 | iex         # Windows
```

```sh
closedvps create
```

The first run asks three things: your Hetzner API token (Hetzner Console, your project, Security, API tokens,
**Read & Write**), which it offers to keep in your system's password store; what the server is for (just a
server, Claude Code, Codex, or both); and, once you've seen the plan and said yes, a Tailscale auth key
(login.tailscale.com/admin/settings/keys, one-off). Everything else is a default it shows in the plan: the
first type Hetzner sells you in nbg1, fsn1 or hel1 for up to 10 a month, no backups, and your local user
name. A few minutes after the purchase it prints the command to log in, `ssh you@vps`.

The server asks for the tag `tag:closedvps` itself, which works once your tailnet policy has the two changes
in [Tailscale access](#tailscale-access). Until then it joins untagged, and ClosedVPS says how to fix that.

It also needs an SSH key on your computer (it offers to run `ssh-keygen` when there's none) and Tailscale
running there, so you can reach the server.

`closedvps setup` saves your own answers: what servers are for, locations, types, price caps, backups, your
user name and alerts. Its last step is optional and worth it if you buy often: a Tailscale OAuth client, so
ClosedVPS makes a one-off key for each server itself and checks your policy before buying.

```sh
closedvps setup                                   # save your defaults
closedvps types                                   # stock and prices, buys nothing
closedvps create -n --show-setup                  # the plan and the exact first-boot setup, buys nothing
closedvps create --name web --claude              # another server, with Claude Code on it
closedvps create --name agents --claude --codex   # with Claude Code and Codex
closedvps create --count 10 --type cx23:6         # ten servers at up to 6 a month each
CLOUDFLARE_TUNNEL_TOKEN=... closedvps create      # with a Cloudflare Tunnel for public sites
closedvps list                                    # what you have, what it costs, what can reach it
closedvps delete vps                              # gone, with its firewall
```

Any saved answer can be overridden for one run with a flag (`--location`, `--type`, `--max-price`,
`--max-total`, `--backups`, `--claude`, `--codex`); `closedvps create --help` lists them. Claude Code and
Codex need 4 GB, so with either ClosedVPS only buys types with 4 GB or more.

`closedvps upgrade` replaces it with the newest release, checked against the release's checksums the way the
install scripts check it. Once a day at most, a command you run in a terminal asks closedvps.com whether a
newer release is out and says so when it ends. The site counts each ask by version, operating system and
country, and keeps nothing else. Runs with `--json` or `--yes` never ask, and `CLOSEDVPS_NO_UPDATE_CHECK=1`
turns it off. In Docker, pull the image again.

## Prices and several servers

Every price is per month before VAT, with the IPv4 address included (and backups, when on), in your Hetzner account's
currency. `--max-price` caps each server. A type can carry its own cap in the list, so `--type cx23:6,cx33:12`
tries cx23 at up to 6 and then cx33 at up to 12. `--max-total` caps all the servers ClosedVPS made in that
Hetzner project together, counting the ones already there, and it's checked before every purchase. Prices are
read again on every check, so a price rise while it waits can't push a purchase over a cap.

`--count 10` buys ten servers named vps-1 to vps-10, each with its own firewall. Each check it buys as many as
are in stock and keeps waiting for the rest. A pasted Tailscale key has to be reusable, or only the first
server joins your tailnet; with a saved OAuth client each server gets its own one-off key. Hetzner limits how many servers an account can have; reaching that stops the run, and the
servers already bought stay.

## Waiting for stock

Since June 2026 Hetzner often shows the cheaper types as unavailable, and it blocks new accounts and some
existing ones, chosen at random, from creating servers. Every minute ClosedVPS asks Hetzner which of your
candidates are in stock and tries them in order. A refusal that waiting can fix means another round. One it
can't fix, such as a bad token or a full account limit, stops it with Hetzner's own message.

It keeps the computer from sleeping on idle while it waits, but closing a laptop lid still sleeps it. For waits
longer than a laptop stays open, run it on a machine that stays on (below).

`closedvps types` shows Hetzner's in-stock flag, which is the same for everyone. Your account can still be
refused, and only trying tells.

## Alerts

When it buys a server, when the server answers SSH, and when it stops without buying, this computer shows a
notification with a sound. That's all, unless you give it links to send the same alerts to:

| Where | What you give it |
| --- | --- |
| ntfy app | `ntfy` in `closedvps setup` makes a private topic, or `https://ntfy.sh/<topic>` |
| Discord | a channel webhook link (channel settings, Integrations, Webhooks) |
| Slack | an incoming webhook link |
| Telegram | `telegram` in `closedvps setup` connects a bot you make with @BotFather |
| Anything else | any https link, which gets each alert as JSON |

`closedvps setup` saves them in your system's password store and sends a test message. `CLOSEDVPS_NOTIFY`
sets them for one run, and `--notify none` turns them off. `--notify <links>` works too, but leaves the links
in your shell history. The "ready"
alert comes from the new server itself once it's on your tailnet, so it arrives even when the machine running
ClosedVPS has no Tailscale.

## Running it on a machine that stays on

`create --yes` runs without a terminal. Give it the secrets as environment variables, and the SSH public key
as `CLOSEDVPS_SSH_KEY` where there's no key file.

```sh
docker run -d --restart on-failure:3 --name closedvps \
  -e HCLOUD_TOKEN -e TS_OAUTH_CLIENT_ID -e TS_OAUTH_CLIENT_SECRET -e CLOSEDVPS_NOTIFY \
  -e CLOSEDVPS_SSH_KEY="$(cat ~/.ssh/id_ed25519.pub)" \
  ghcr.io/tulayha/closedvps create --yes --user you --type cx23:6
docker logs -f closedvps
```

A wait can take days, which a pasted `TS_AUTHKEY` may not outlast, so an OAuth client (as above, or saved
with `closedvps setup` where the machine has a password store) is the better fit here: the key is made at the
moment of buying. Without a terminal a taken name stops the run, so a restart after a purchase can't buy a
second server. On a Linux machine without Docker, `systemd-run` keeps it going in the background (with
`loginctl enable-linger` it also outlives your login):

```sh
systemd-run --user --unit=closedvps -E HCLOUD_TOKEN -E TS_AUTHKEY closedvps create --yes --type cx23:6
journalctl --user -u closedvps -f
```

The exit code says how it ended, and 3 always means a server is billing; `closedvps create --help` lists the
codes. `--json` prints one event per line on stdout and moves the text to stderr, for scripts and agents. The
[skill](skills/closedvps/SKILL.md) describes the events.

## What it creates

In your Hetzner project it adds your SSH public key if Hetzner doesn't have it yet, which also stops Hetzner
from emailing a root password. It creates a firewall named `closedvps-<name>` with no rules, which Hetzner
treats as "drop all inbound, allow all outbound", and then the server with that firewall attached in the same
API call. The server is labelled `managed-by=closedvps`, and backups are switched on right after if you chose
them.

On first boot, cloud-init on the server:

- creates your user with your SSH key and passwordless sudo, and limits SSH to keys, to that user, and to no
  root login;
- lets only root reach Hetzner's metadata service, which keeps serving this setup and its secrets for the
  server's life, so other users and Docker containers can't read them;
- installs all updates and turns on unattended upgrades, which reboot the server at 04:00 UTC when an update
  needs it;
- joins your tailnet under the server's name, after that first reboot if the kernel was updated;
- sets Docker, should you install it later, to publish container ports on 127.0.0.1, on Compose networks too;
- with `CLOUDFLARE_TUNNEL_TOKEN`, installs cloudflared from Cloudflare's apt repository and runs your tunnel;
- with `--claude`, installs Claude Code for your user, plus what its sandbox needs. Run `claude` on the
  server to log in.
- with `--codex`, installs OpenAI's Codex CLI for your user, plus the same sandbox pieces. Run
  `codex login --device-auth` on the server to log in from your laptop's browser, after turning on device
  code login in ChatGPT's security settings.

## Public websites

Create a tunnel in the Cloudflare dashboard (Networking, Tunnels, Create a tunnel), copy the token from the
install command it shows, and pass it as `CLOUDFLARE_TUNNEL_TOKEN`. Then on the tunnel's Routes tab add a
published application with the service URL `http://127.0.0.1:<port>`. The tunnel connects outward, so the
firewall stays closed.

## Security model

Nothing on the internet can reach the server. The Hetzner firewall drops every inbound packet, ping
included, before it gets there, and `closedvps list` shows whether each server's firewalls still let nothing
in.

These stay as they are, and you should know them:

- The Hetzner token can do anything in its project. ClosedVPS sends it only to `api.hetzner.cloud`. If you let
  it save the token, it goes in the macOS Keychain, Windows Credential Manager or the Linux Secret Service, or
  where none exists, a file only you can read in the ClosedVPS config folder. `closedvps forget` deletes it,
  the alert links and the Tailscale OAuth client.
  A separate Hetzner project for these servers keeps the token away from anything else.
- The Tailscale auth key, the tunnel token and the alert links reach the server in its cloud-init data.
  Hetzner keeps that for the server's life in its metadata service, which only root on the server can reach.
  A one-off auth key is spent once the server joins; a reusable one for `--count` should expire within a day.
  cloudflared needs the tunnel token on the server anyway.
- A server that joins untagged can open connections to every device on your tailnet and drops off it after
  180 days. Each server asks for `tag:closedvps`, which your policy allows once it has the changes in
  [Tailscale access](#tailscale-access), and ClosedVPS warns when a new server has no tag.
- A saved Tailscale OAuth client can make tagged keys and read your policy and device list. It can't reach
  your devices or change the policy. It's kept like the Hetzner token.
- Outbound traffic is allowed, and your user has passwordless sudo, as on most cloud images.
- Alert links can post to your chat, so they're kept like the token. Each server sends its own "ready" alert
  with them and then deletes its copy, though root can still read them from the metadata service. Alerts
  carry server names, types and prices (a plain webhook also gets the public IP), never a token. Anyone who knows an ntfy topic's name can
  read it, so ClosedVPS makes a random one.

Report vulnerabilities as described in [SECURITY.md](SECURITY.md).

## Tailscale access

A new tailnet lets every device reach every other one, so a server that Claude Code or a bad package takes
over could reach your laptop. Two changes in the tailnet policy (login.tailscale.com/admin/acls) keep your
devices able to reach the servers and stop the servers from opening connections to anything:

```jsonc
"tagOwners": { "tag:closedvps": ["autogroup:admin"] },
"grants": [
  // was "src": ["*"]; tagged servers aren't members, so they can only answer
  {"src": ["autogroup:member"], "dst": ["*"], "ip": ["*"]},
],
```

Each server asks for `tag:closedvps` when it joins, so a plain one-off key is enough. Replies to your
connections still get through, and tagged devices skip key expiry. An older tailnet whose policy has `acls` in place of `grants` changes the
`src` of its allow-everything rule the same way. A server that's already running gets the tag from the Machines page, its menu, Edit tags.
[closedvps.com/tailscale](https://closedvps.com/tailscale) explains each step.

## If Tailscale stops working

```sh
closedvps open-ssh vps            # one rule: TCP 22 from your current public IPv4 only
ssh you@<the server's public IP>  # fix it, e.g. sudo tailscale up
closedvps close-ssh vps           # the rule is gone again
```

`open-ssh` looks up your IP at `ipv4.icanhazip.com`; `--from <ip>` skips that.

If SSH itself is broken, use Hetzner's web console. A key-only server has no root password, so set one first
in the Hetzner Console under the server's Rescue tab (Reset root password), then log in as root through the
console's `>_` button. SSH never allows root, so that password only works there. If the reset fails, the same
tab can boot a rescue system with your SSH key; run `closedvps open-ssh` first so you can reach it.

## Servers you already have

`closedvps list` shows every server in the Hetzner project, made by ClosedVPS or not, with its price, whether
the internet can reach it and what's let in, and whether it's on your tailnet. `--json` adds the firewall
rules. A server with no firewall takes connections on every port it
listens on, Docker's published ports included. [Closing one by hand](https://closedvps.com/guide#close-a-server-you-already-have)
puts Tailscale on it before the firewall goes on, so you never lose your way in.

## Removing a server

`closedvps delete vps` deletes the server, its disk and backups, and its `closedvps-vps` firewall, after you
type its name. It only deletes servers ClosedVPS made, and it says when snapshots are left, since Hetzner keeps
billing those. Remove the machine from your Tailscale admin too. A firewall left from a stopped run goes the
same way: `closedvps list` names it, and `closedvps delete <name>` removes it.

## For coding agents

The [skill](skills/closedvps/SKILL.md) covers running ClosedVPS with no prompts. In Claude Code it installs
as a plugin:

```
/plugin install closedvps --marketplace tulayha/closedvps
```

Other agents can load it directly.

## Development

[CONTRIBUTING.md](CONTRIBUTING.md) has the checks and how the code is laid out. The install scripts are in
[install/](install/), and closedvps.com is on the [site branch](https://github.com/tulayha/closedvps/tree/site).

## License

[MIT](LICENSE)
