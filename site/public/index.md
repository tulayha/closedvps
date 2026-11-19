# ClosedVPS: a Hetzner server with every port closed

ClosedVPS buys a Hetzner Cloud server from your own computer. When Hetzner is sold out, it checks every minute
and buys the first server on your list that comes into stock, up to the price you set. The server comes up
behind a firewall that lets nothing in, and you get in over Tailscale. It's free and MIT licensed, for macOS,
Linux and Windows: https://github.com/tulayha/closedvps

## Install

macOS and Linux:

```sh
curl -fsSL https://closedvps.com/install.sh | sh
closedvps create
```

Windows (PowerShell):

```powershell
irm https://closedvps.com/install.ps1 | iex
closedvps create
```

You need a Hetzner API token with Read & Write, a Tailscale auth key and an SSH key on this computer. The first
run asks for the token and offers to keep it in your system's password store (macOS Keychain, Windows
Credential Manager or the Linux Secret Service), and `closedvps forget` deletes it. Then it asks where and what
to buy, your price caps, backups, your user name and where alerts go, and remembers the answers. The Tailscale key
is asked for on every run and never saved. `closedvps create --dry-run` prints the whole first-boot setup
before you pay for anything.

## When Hetzner is sold out

Since June 2026 Hetzner often shows its cheaper types as unavailable, and it refuses new servers on new
accounts and on some older ones chosen at random
([a write-up of what changed](https://www.vincentschmalbach.com/hetzner-cheap-cloud-unavailable-price-increases/)).
ClosedVPS takes any refusal that waiting can fix as another round. One it can't fix, such as a bad token or a
full account limit, stops it with Hetzner's own message. While it waits it keeps your computer from sleeping on
idle, but closing a laptop lid still stops it.

Your computer shows a notification when it buys a server, when the server answers SSH over your tailnet, and
if it stops without buying. Give it an ntfy, Discord, Slack or Telegram link, or any webhook, and the alerts go
there too; the "ready" one comes from the new server itself. For waits longer than a laptop stays open, run it
on any machine that stays on, in Docker if you like.

An example run, 19 Nov 2026:

```
$ closedvps create
Buys the first of these Hetzner has in stock (per month with IPv4, before VAT):
  cx23   nbg1   2 cores    4 GB x86  5.99 EUR/month
  cax11  hel1   2 cores    4 GB arm  6.49 EUR/month
Buy it? [y/N] y
Firewall closedvps-vps ready, no inbound rules
09:14:02  nothing on your list is in stock (check 1)
Trying again every minute until one is bought. Ctrl+C stops.
... 2 hours later ...
11:43:02  cax11 hel1   vps bought, 6.49 EUR/month
vps is running at 203.0.113.1, unreachable from the internet.
First-boot setup takes 3 to 5 minutes.
Waiting for vps to join your tailnet............ online
Ready: ssh dev@vps
In the Tailscale admin, turn off key expiry for vps, or it drops off your tailnet after 180 days.
$ nmap -Pn 203.0.113.1
All 1000 scanned ports on 203.0.113.1 are in ignored states.
Not shown: 1000 filtered tcp ports (no-response)
$ ssh dev@vps
```

## What you get: locked down before it boots

The Hetzner firewall has no inbound rules and is attached in the same API call that creates the server, so it
never boots open. Scanned from the internet, every port shows as filtered. If another firewall in your project
lets traffic in, ClosedVPS warns you, and `closedvps list` checks again any time.

SSH takes keys only, for one user named after your account on your computer, and only over Tailscale. Root
can't log in. Security updates install daily, with a reboot at 04:00 UTC when one needs it. Hetzner Backups keep
7 daily copies for 20% of the server price, when you turn them on with `--backups`.

Pass `CLOUDFLARE_TUNNEL_TOKEN` and the server runs your Cloudflare Tunnel, for sites you want public. If you
install Docker later, ports you publish bind to 127.0.0.1; to reach one from your laptop, publish it on the
server's Tailscale IP (`-p 100.x.y.z:3000:3000`). `--claude` adds Claude Code, which you log in to with your own
account.

## If Tailscale stops working

`closedvps open-ssh vps` opens port 22 to your current IPv4 address, so you can SSH to the server's public IP and
fix Tailscale. `closedvps close-ssh vps` shuts it again.

To get rid of a server, delete it and its `closedvps-vps` firewall in the Hetzner Console, and remove it from
your Tailscale admin.

## Doing it by hand

[How to lock down a Hetzner VPS by hand](https://closedvps.com/guide) does the firewall, Tailscale, SSH and
update steps in the Hetzner Console. It logs in as root and doesn't wait for stock.
