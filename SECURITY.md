# Security

closedvps holds a token that can create and delete servers in your Hetzner project, and it writes the setup
of a server meant to be unreachable. Bugs in either matter, so please report them privately.

## Reporting

Use GitHub's private vulnerability reporting: the Security tab of this repository, "Report a vulnerability".
Please include the version (`closedvps version`), what you ran and what happened. You'll get an answer within
a week.

Out of scope: the security of Hetzner, Tailscale or Cloudflare themselves, and what you install on the server
afterwards.

## Supported versions

Only the latest release gets fixes.

## Verifying a download

Every release archive comes with `checksums.txt`, which the install scripts check, and an SBOM listing what's
inside it. The archives, SBOMs, checksums and the Docker image carry signed build provenance from the release
workflow:

```sh
gh attestation verify closedvps_linux_amd64.tar.gz -R tulayha/closedvps
gh attestation verify oci://ghcr.io/tulayha/closedvps:<version> -R tulayha/closedvps
```

## Threat model

What closedvps protects is the new server, and the secrets it handles on the way there. This is what each
part trusts, what could go wrong, and what stands in the way.

**The Hetzner token** can do anything in its project. It's read from `HCLOUD_TOKEN` or the system's password
store (a 0600 file where there is none), sent only to `api.hetzner.cloud` by the official hcloud-go client,
and never logged or put on a server. Someone who can run code as you on your computer can read it, as they
could read any of your credentials. A separate Hetzner project for these servers limits what a leak reaches.

**The server's setup** (cloud-init user data) is built from what you type: name, user, SSH key, tokens and
alert links. If any of it could break out of its YAML string or shell context, it would run on the server as
root. `cloudinit.Config.Validate` allows names, users and tokens only from fixed character sets, parses the SSH
key, refuses control characters in alert links, and every value is written as a quoted YAML string. Alert
requests reach curl as config files (`curl -K`), never through a shell. A fuzz test renders random input and
checks that the result has only closedvps's own keys, with each value where it belongs.

**Secrets on the server.** The Tailscale auth key, the tunnel token and the alert links are in the user data,
which Hetzner serves from its metadata service (169.254.169.254) for the server's whole life. The setup blocks
that address for every user but root and for all forwarded traffic, so other users and containers can't read
them; root can. The Tailscale key file is deleted once the server joins, and the alert files once sent. A
one-off auth key is useless after the join; a reusable one (for `--count`) should expire within a day.

**Reaching the server.** The Hetzner firewall has no inbound rules and is attached in the same API call that
creates the server, so there's no moment when the server is open. closedvps refuses to reuse a firewall that
has inbound rules or is attached elsewhere, warns when another firewall on the server lets traffic in, and
`closedvps list` checks again. SSH listens only for your key and your user, with root login off, and is
reached over Tailscale; every device on your tailnet can reach it unless your Tailscale access rules say
otherwise. `open-ssh` adds one rule for one IP until `close-ssh` removes it.

**Spending money.** A bug in buying costs real money. Each purchase is checked against `--max-price`, the
type's own cap and `--max-total` with prices read fresh each time, and backups counted for servers bought in
the same run. A create whose reply is lost is looked up by name, so it's neither bought twice nor missed.
Any run that bought something ends with exit code 3 when it didn't finish, Ctrl+C included, so a script
knows servers are billing.

**Alerts** go to links you chose. They carry server names, types and prices, and a plain webhook also gets
the public IP. Anyone who knows an ntfy topic's name can read it, so closedvps makes a random one.

**Claude Code** (`--claude`) needs unprivileged user namespaces for its sandbox, which Ubuntu 24.04 blocks.
The setup lets `/usr/bin/bwrap` alone use them through an AppArmor profile for that one program, the way
Ubuntu's notes on the restriction suggest.

**The release** is built by GoReleaser in GitHub Actions after every CI check passes, with actions pinned to
commits, and published with provenance as above. Dependabot keeps dependencies current, govulncheck and
CodeQL look for known and new vulnerabilities, gitleaks checks the history for secrets, and zizmor checks the
workflows.
