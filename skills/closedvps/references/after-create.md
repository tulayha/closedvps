# After a server is ready

The server is meant to hold nothing that reaches the user's other machines, so everything here keeps their
laptop's keys, credentials and Claude Code config off it.

## Logging in and long sessions

`ssh <user>@<name>` works from any device on their tailnet. Updates can restart the server at 04:00 UTC and a
dropped connection ends whatever ran in it, so long work goes in tmux:

```sh
ssh -t <user>@<name> tmux new -As main
```

## Signing in to the agents

The user does this themselves, since it opens a page in their browser:

- Claude Code: run `claude` on the server and open the link it prints.
- Codex: `codex login --device-auth` and enter the code it prints at the link.

Started as `claude --remote-control` inside tmux, a Claude Code session on the server can also be driven from
the Claude app on their phone or at claude.ai/code.

Don't copy `~/.claude`, `~/.codex`, hooks or credentials over from their laptop: hooks and absolute paths
from another machine break there, and the credentials would sit on a server an agent runs on.

## Getting a repo onto it

Forwarding their SSH agent or copying their GitHub key would let anything on the server push to every repo
they can. For one repo, a deploy key is enough:

```sh
ssh <user>@<name> 'ssh-keygen -t ed25519 -N "" -f ~/.ssh/id_ed25519 && cat ~/.ssh/id_ed25519.pub'
```

They add that key under the repo's Settings, Deploy keys (with write access if the agent should push), then
`git clone git@github.com:<owner>/<repo>.git` on the server. For many repos, `sudo apt install gh` and
`gh auth login` there with the device code, which grants what their account can do.

## Reaching what runs on it

Their own devices reach any port on the server over the tailnet, as `http://<name>:<port>`, once the app
listens on all addresses (`--host 0.0.0.0` or the framework's equivalent). Nothing is needed in Hetzner's
firewall for that. For the public internet, the server needs to have been created with
`CLOUDFLARE_TUNNEL_TOKEN`, and the user adds a public hostname for that port to the tunnel in Cloudflare.

## When the setup looks unfinished

```sh
ssh <user>@<name> cloud-init status --long
ssh <user>@<name> sudo tail -n 50 /var/log/cloud-init-output.log
```

If the server never joined the tailnet, there's no SSH over Tailscale to run these with: `closedvps open-ssh
<name>`, ssh to its public IPv4 from `closedvps list`, look, then `closedvps close-ssh <name>`.
