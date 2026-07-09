# Deployment

The app ships as a single Go binary in a Docker image, deployed with
[Kamal 2](https://kamal-deploy.org) to the Hetzner box `204.168.131.104`.
kamal-proxy on the server owns ports 80/443 and the Let's Encrypt certificate
for `bot.chic.md`; the app listens on `:8080` behind it and is health-checked
at `/healthz`. Images live in a private GHCR package
(`ghcr.io/mihaildolghintev/chic`). SQLite data survives deploys on the
`chic_data` named volume mounted at `/data`.

Config: [config/deploy.yml](config/deploy.yml).

## Release flow (the normal path)

Merging to `main` runs CI only ([ci.yml](.github/workflows/ci.yml)) — it never
deploys. Production deploys happen when a **GitHub Release is published**:

```sh
# from a green main
gh release create v0.2.0 --generate-notes
```

(or via UI: Releases → Draft a new release → new tag → Publish. Drafts don't
deploy; publishing does, pre-releases included.)

Publishing the release triggers [deploy.yml](.github/workflows/deploy.yml),
which checks out the release tag, builds the image on the runner (native
amd64), pushes it to GHCR and runs `kamal deploy` over SSH. The `VERSION`
build arg comes from `git describe --tags`, so the image version equals the
release tag.

The release trigger does not re-check CI — publish releases from a green
`main` only.

Watch progress in the Actions tab (workflow "Deploy"). The workflow also has a
`workflow_dispatch` button for redeploying `main` manually without a new
release.

## Secrets

Kamal reads secrets from the environment; [.kamal/secrets](.kamal/secrets) is
committed and holds no values — it only maps env vars through. The real values
live in **two places that must be kept in sync**:

- **GitHub → repo Settings → Secrets and variables → Actions** (used by CI
  deploys)
- **`.envrc`** — gitignored, laptop-only (template with per-variable docs:
  [.envrc.example](.envrc.example))

| Secret | Purpose |
|---|---|
| `DEPLOY_SSH_KEY` | Private half of the CI-only SSH keypair (GitHub only, not in `.envrc`). Public half sits in `authorized_keys` of `deploy@204.168.131.104`. |
| `KAMAL_REGISTRY_PASSWORD` | GitHub PAT (classic), `read:packages` + `write:packages` — pushes the image and logs the server into GHCR for pulls. |
| `TELEGRAM_BOT_TOKEN` | From @BotFather. |
| `TELEGRAM_WEBHOOK_SECRET` | `openssl rand -hex 32`. The app registers it with Telegram via `SetWebhook` on every start, so rotating it only requires a redeploy. |
| `ALLOWED_USER_IDS` | Comma-separated Telegram user ids. |
| `MOYSKLAD_TOKEN` | MoySklad access token. |
| `DEEPSEEK_API_KEY` | platform.deepseek.com — the agent's text model. |
| `OPENAI_API_KEY` | Optional: enables photo understanding (vision). Absent secret deploys as an empty value and vision stays off. |

If a value changes in one place, change it in the other — a laptop deploy with
a stale `.envrc` will silently push the old value back to production.

## Manual deploy from the laptop

Still works, mostly useful as a fallback. Requires the env vars loaded:

```sh
# with direnv (direnv allow once):
kamal deploy

# without:
bash -c 'source .envrc && kamal deploy'
```

SSH access uses your personal key (1Password agent) — the `deploy` user on the
server trusts both it and the CI key.

## Rollback

Kamal keeps previous containers on the server:

```sh
kamal app containers        # list what's available
kamal rollback <version>    # version = image tag, e.g. the git sha/tag
```

Run from the laptop (env loaded), or re-publish an older release / run the
Deploy workflow manually from the tag. Rollback only swaps containers — it
does not undo schema changes in the SQLite files on `chic_data`.

## First-time server setup

Only needed for a fresh box: `kamal setup` (installs Docker, boots
kamal-proxy, then deploys). The current box is already set up.

## Troubleshooting

- `Permission denied (publickey)` in the Deploy workflow — `DEPLOY_SSH_KEY`
  secret is wrong/incomplete (it must include the `-----BEGIN/END OPENSSH
  PRIVATE KEY-----` lines) or the public half is missing from the server.
- `Net::SSH::HostKeyMismatch` in the Deploy workflow — the server's ed25519
  host key is pinned in [deploy.yml](.github/workflows/deploy.yml) ("Trust
  server host key" step). If the box was reinstalled, refresh the pinned line
  with `ssh-keyscan -t ed25519 204.168.131.104`; if it wasn't, treat the
  mismatch as a real warning and investigate before trusting the new key.
- Missing env var error from kamal — a secret wasn't set (GitHub secret names
  must match [.kamal/secrets](.kamal/secrets) exactly).
- Two deploys collided — the workflow serializes via a concurrency group and
  kamal holds a lock on the server; if a run was killed mid-deploy, clear a
  stale lock with `kamal lock release`.
- App misbehaving after deploy: `kamal app logs -f`, `kamal app exec sh`,
  `kamal proxy logs`.
