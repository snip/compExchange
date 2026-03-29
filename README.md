# CompExchange — Gliding Race Viewer

[![License: GPL v3](https://img.shields.io/badge/License-GPLv3-blue.svg)](LICENSE)

A real-time gliding competition viewer that combines a [GlideAndSeek](https://glideandseek.com) live-tracking map with a Discord channel overlay, so spectators can follow the race and the commentary in one place.

## Features

- Full-screen GlideAndSeek iframe as the background
- Live Discord chat overlay at the bottom with a gradient fade
- Pinned messages panel on the left (slide-out, lockable)
- Competition title bar at the top linking to the official website
- New messages and newly pinned messages flash gold
- Clickable hyperlinks in messages (including URLs with query strings)
- Resize the chat overlay by dragging the handle
- Font size controls (A− / Aa / A+)
- Collapse / expand the chat overlay
- Multiple competitions on a single server — each gets its own URL prefix
- Competitions are loaded on demand and unloaded automatically when idle

---

## Requirements

- [Go](https://go.dev/) 1.21 or later
- A Discord bot token (see [Bot setup](#discord-bot-setup))

---

## Quick start

```bash
# 1. Clone the repository
git clone https://github.com/yourname/compExchange.git
cd compExchange

# 2. Install Go dependencies
go mod download

# 3. Create a competition config
cp comps/.env.example comps/my-comp.env
# Edit comps/my-comp.env with your values (see Configuration)

# 4. Run
go run .
# → http://localhost:8080/
```

If only one competition is configured, the root URL redirects to it automatically.

---

## Configuration

Each competition is configured by a single file in the `comps/` directory.
The **filename stem** (everything before `.env`) becomes the URL path segment.

```
comps/
  tour-de-france.env   →  http://localhost:8080/tour-de-france/
  nationals-2026.env   →  http://localhost:8080/nationals-2026/
  .env.example         ←  template (committed, safe to share)
```

### Config file reference

| Variable | Required | Description |
|---|---|---|
| `BOT_TOKEN` | yes | Discord bot token |
| `CHANNEL_ID` | yes | Discord channel ID(s) to monitor (comma-separated for multiple) |
| `GLIDE_URL` | yes | Full GlideAndSeek URL to embed (build it on glideandseek.com) |
| `COMP_NAME` | no | Human-readable name shown in the menu and title bar (falls back to filename stem) |
| `COMP_WEBSITE` | no | Official competition website — the title bar becomes a link to it |
| `BUFFER_SIZE` | no | Number of recent messages pre-loaded for new viewers (default: `20`) |
| `PUBLISHED` | no | Set to `true` to list this competition on the root index page (default: `false`) |

### Global settings (OS environment variables)

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | HTTP port the server listens on |

### Example

```ini
BOT_TOKEN=MTQ4NzE1...
CHANNEL_ID=8309422...19790346
GLIDE_URL=https://glideandseek.com/?embed=compexchange&taskUrl=https://example.com/task.tsk
COMP_NAME=Championnat de France Club 2026
COMP_WEBSITE=https://www.soaringspot.com/en_gb/my-comp/
BUFFER_SIZE=30
PUBLISHED=true
```

---

## Discord bot setup

1. Go to [https://discord.com/developers/applications](https://discord.com/developers/applications) and create a new application.
2. Under **Bot**, click **Add Bot** and copy the token into `BOT_TOKEN`.
3. Under **Bot → Privileged Gateway Intents**, enable:
   - **Message Content Intent**
   - **Server Members Intent**
4. Under **OAuth2 → URL Generator**, select scopes `bot` and permissions:
   - `View Channels`
   - `Read Message History`
5. Open the generated URL in your browser to invite the bot to your server.
6. In your Discord server, right-click the channel you want to monitor → **Copy Channel ID** (Developer Mode must be on in Discord settings) and paste it into `CHANNEL_ID`.

---

## Multiple competitions

Add one `.env` file per competition to the `comps/` directory.
The server detects them at runtime — no restart needed:

- Visiting `/{name}/` loads that competition on demand (the bot connects and starts streaming).
- Visiting `/` shows a menu listing only competitions with `PUBLISHED=true`. If exactly one is published, it redirects there directly. Competitions without `PUBLISHED=true` are still fully accessible via their direct URL — they are simply unlisted.
- If a config file is deleted, the competition stays available until all viewers leave, then it is unloaded automatically after 5 minutes.

---

## URL structure

| Path | Description |
|---|---|
| `/` | Root — redirects if one comp, shows menu if many |
| `/{name}/` | Competition viewer (GlideAndSeek + Discord overlay) |
| `/{name}/events` | Server-Sent Events stream (chat messages + pin signals) |
| `/{name}/pinned` | JSON array of current pinned messages |
| `/{name}/config` | JSON config served to the frontend |

---

## CI/CD

A GitHub Actions workflow (`.github/workflows/docker-publish.yml`) builds and pushes the Docker image on every push to `main`, targeting both **Docker Hub** and the **GitHub Container Registry** (`ghcr.io`).

Images are built for `linux/amd64` and `linux/arm64` (suitable for cloud VMs and Raspberry Pi alike).

Two repository secrets are required in **Settings → Secrets → Actions**:

| Secret | Value |
|---|---|
| `DOCKER_USERNAME` | Your Docker Hub username |
| `DOCKER_PASSWORD` | A Docker Hub access token (not your password) |

`GITHUB_TOKEN` is provided automatically by GitHub — no configuration needed.

Dependabot is configured to open weekly PRs for updates to Go modules, Docker base images, and the Actions themselves.

## License

CompExchange is free software released under the [GNU General Public License v3.0](LICENSE).
You are free to use, modify, and distribute it under the terms of that licence.

## Project structure

```
compExchange/
├── main.go            # HTTP server, registry, competition lifecycle
├── hub.go             # SSE hub — fan-out, message buffer, client management
├── discord.go         # Discord bot — session, message/pin handlers
├── frontend/
│   ├── index.html     # Single-page viewer application
│   └── style.css      # Styles
├── comps/
│   └── .env.example   # Competition config template
├── .gitignore
└── go.mod
```

---

## Building a binary

```bash
go build -o compexchange .
./compexchange
```

Set `PORT` to change the listening port:

```bash
PORT=9090 ./compexchange
```

---

## Docker

### Using Docker Compose (recommended)

```bash
# Build and start
docker compose up -d

# View logs
docker compose logs -f

# Stop
docker compose down
```

The `comps/` directory is mounted as a volume — add or remove `.env` files there
without rebuilding the image. The server picks them up on the next request.

To expose on a different host port, set `PORT` in your shell before starting:

```bash
PORT=9090 docker compose up -d
```

### Using Docker directly

```bash
# Build
docker build -t compexchange .

# Run
docker run -d \
  --name compexchange \
  -p 8080:8080 \
  -v "$(pwd)/comps:/app/comps" \
  --restart unless-stopped \
  compexchange
```
