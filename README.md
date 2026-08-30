# core-system-launcher

An interactive CLI that stands up a full [NYCU SDC Core System](https://github.com/NYCU-SDC)
on your own machine.

## Requirements

- **Docker** with Compose v2 (`docker compose`, not `docker-compose`)
- **git** — used to fetch the upstream sources and apply patches
- Network access to GitHub, Docker Hub, the Go module proxy and the npm registry

No Go, Node or database toolchain needed on your side: everything is compiled inside
containers. `doctor` checks all of the above before anything is downloaded.

## What it does

Core System spans three repos —
[api](https://github.com/NYCU-SDC/core-system-api),
[backend](https://github.com/NYCU-SDC/core-system-backend) and
[frontend](https://github.com/NYCU-SDC/core-system-frontend).
Standing it up yourself means aligning versions across them, wiring OAuth, seeding the
database, and working around a handful of issues that have not landed upstream. This CLI
wraps all of that into one command.

1. Checks your environment (Docker, docker compose, ports)
2. Fetches backend and frontend at the commits pinned in `versions.lock`, then applies the patches under `patches/`
3. Builds everything inside containers and starts the stack
4. Walks you through Google OAuth, or uses a no-OAuth trial mode so you can see the UI right away
5. Seeds two sample registration forms as drafts

## Quick start

Download a binary from [Releases](https://github.com/NYCU-SDC/core-system-launcher/releases),
or build it yourself:

```bash
go build -o core-system-launcher .
./core-system-launcher
```

The first run asks three things — the public port, the admin email, and how you want to
log in. Later runs just start the stack.

## Commands

| Command | Description |
|---|---|
| `up` | Build and start. The first run walks through setup. Pass `--rebuild` to force a rebuild |
| `down` | Stop the stack, keeping all data |
| `logs [service]` | Follow logs, optionally for `backend`, `frontend` or `postgres` |
| `status` | Show the state of each service |
| `doctor` | Diagnose environment problems |
| `reset` | Drop the database, sources and config, then start over (asks for confirmation) |

Set `CORE_SYSTEM_LAUNCHER_HOME` to move the working directory away from `~/.core-system-launcher`.

## Architecture

A single port is exposed. The frontend gateway serves the static build and reverse-proxies
`/api` to the backend.

```
http://localhost:<port>  ->  frontend (Fastify gateway)
                               |- /        -> static build
                               |- /api/*   -> backend:8080
                                                |- postgres:5432
```

**Same-origin is mandatory.** The frontend uses `@nycu-sdc/core-system-sdk`, an orval fetch
client generated without a baseUrl, so every request goes to a relative `/api/...` path.
Splitting the origins would mean extra CORS and cookie handling, so that option is
deliberately not offered.

## Google OAuth

The redirect URI is bound to the port you pick. The CLI prints the exact line to paste into
the [Google Cloud Console](https://console.cloud.google.com/apis/credentials):

```
http://localhost:<port>/api/auth/login/oauth/google/callback
```

Create the client as a **Web application**. If your consent screen is External and still in
Testing, add the account you plan to sign in with to the Test users list, or Google will
block the sign-in with `access_blocked`.

Changing the port means changing both sides, otherwise you get `redirect_uri_mismatch`.

### Trial mode

If you would rather not set up OAuth first, pick trial mode. It uses the internal login
endpoint the backend exposes when `DEV=true`, and the CLI hands you a snippet to paste into
your browser console.

Trial mode is for looking around only. Do not use it for anything real.

## Bundled sample forms

After the first start, two SDC registration forms are seeded as **drafts**:

| Form | Contents |
|---|---|
| SDC 註冊表單 - 2025 | 15 sections, 44 questions, 4 sign-up branches |
| SDC 註冊表單 - 2026 | 18 sections, 64 questions, 6 sign-up branches |

They exercise what Core System does beyond Google Forms: condition nodes that decide whether
a program's screening questions appear at all, ProseMirror rich-text descriptions, ranking
questions and hyperlink fields.

Seeding goes through the API rather than raw SQL, because a form spans four levels of related
records (section, question, choice, workflow) and writing those directly means owning UUID
and foreign-key handling that breaks whenever the schema moves. Forms whose titles already
exist are skipped, so `up` stays idempotent. Nothing is ever published for you.

### Refreshing the samples

`seed/forms.json` is exported from a running system. To update it, run this against that
system after adjusting `BASE`, `UID` and `WANT` at the top of the file:

```bash
python3 seed/export.py
```

The export contains no UUIDs or timestamps — nodes reference each other by array index — so
it replays cleanly into a fresh deployment. The traversal order is deterministic, so
re-exporting only produces a diff when the forms actually changed.

## What gets patched

The launcher never commits anything back to the three upstream repos. Every change lives as
a patch under `patches/`, applied after checking out the pinned commit, so what was changed
stays visible and reviewable.

| Patch | Problem it fixes |
|---|---|
| `frontend/001-preview-section-descriptionHtml` | Section descriptions never render in the admin preview — the field is dropped when building the section list |
| `frontend/002-workflow-unanswered-condition` | Everything after the first unanswered condition node is hidden from the structure bar |
| `frontend/003-admin-auth-refresh-interval` | Admin pages never refresh the access token, logging you out every 15 minutes |
| `frontend/004-auth-refresh-hardening` | Refresh interval far too short, concurrent refreshes racing the backend's token rotation, and no retry once the token has expired |
| `frontend/005-sectionedit-sync-questions-ref` | Question edits get overwritten by their pre-edit values on save |

To follow upstream, swap the commits in `versions.lock` and run `up --rebuild`. If a patch no
longer applies, the CLI names the one that conflicted. CI verifies every patch against the
pinned commits on each push, so breakage surfaces there rather than on someone's laptop.

## Where things live

```
~/.core-system-launcher/
├── config.json      your settings, including the OAuth secret (mode 0600)
├── src/             upstream sources, checked out and patched
│   ├── backend/
│   └── frontend/
└── deploy/          generated compose.yaml and setup.yaml
```

Your own project directories are never touched. `reset` removes this whole tree.

## Known limitations

- The initial organization slug is fixed to `SDC`. The backend hard-codes that string
  (case-sensitively) as the default org, and the frontend falls back to it too, so changing
  it silently breaks default role assignment.
- Only `http://localhost` is supported. Serving a real domain with TLS means putting your own
  reverse proxy in front.
- Upstream's `require_onboarding` is currently hard-coded to `false`, so the onboarding flow
  is skipped entirely.
- Question descriptions cannot embed images: the API has no image field, and the frontend's
  TipTap editor ships without the Image extension.
