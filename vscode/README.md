# Magertron for VS Code

Hand an MCP server you have built to the team that governs it.

## What it does

One command — **Magertron: Submit MCP Server for Review** — takes a `.mcpb`
bundle or a `server.json` manifest and submits it to your platform.

⚠ **Submitting is not deploying.** It records that your server exists so your
platform team can review it. Nothing is deployed, routed or metered until they
decide it should be.

## What it sends

The manifest, with anything credential-shaped removed before it leaves your
machine. Environment variable **names** travel; **values** do not — so the
platform knows a secret is needed without ever seeing one.

## Setup

Two settings, both from your platform administrator:

* `magertron.serverUrl` — your platform's address
* `magertron.token` — a service-account token issued to you

⚠ If you already use `mcpctl` in a terminal, leave the token empty. The
extension will use the credential from your existing `mcpctl login` and take
only the URL from settings.

## What happens next

Your submission appears in the platform's review queue, attributed to the
account whose token you used. An administrator reviews it, decides whether it
should be governed, and — if so — deploys it with a namespace and credentials
you do not choose and never send.
