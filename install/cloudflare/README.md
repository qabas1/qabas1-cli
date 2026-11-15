# install.qabas.one (Cloudflare Worker)

Serves the `qabas` bootstrap script from `qabas1-cli` releases or `main`.

## Deploy

```bash
cd install/cloudflare
npm install -g wrangler   # or npx wrangler
npx wrangler deploy
npx wrangler secret put GITHUB_TOKEN   # required while qabas1-cli is private
```

Attach custom domain **install.qabas.one** in the Cloudflare Worker dashboard.

## Routes

| Path | Response |
|------|----------|
| `/` | Help text |
| `/install.sh` | `install/install.sh` (Linux bootstrap; prefers `.deb`) |
| `/install.ps1` | `install/install.ps1` (Windows bootstrap; prefers MSI) |
| `/qabasonectl` | **410 Gone** — legacy path removed |

## Environment

| Variable | Default | Purpose |
|----------|---------|---------|
| `REPO` | `qabasone/qabas1-cli` | GitHub repo |
| `INSTALL_REF` | `main` | Branch for raw fallback |
| `GITHUB_TOKEN` | (secret) | PAT with `contents:read` for private repos |
