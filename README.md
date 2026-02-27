# mutemath

GitHub notification spam cleaner. Automatically identifies team-only review request notifications and mutes them, keeping your inbox useful.

## Problem

If your org uses CODEOWNERS with team-based review requests, you get flooded with notifications for PRs that aren't relevant to you personally. mutemath fixes this by:

1. Scanning your unread GitHub notifications
2. Identifying PR review requests that were sent to a **team** you belong to (not directly to you)
3. Marking those threads as **read** and **muting** them so they stop generating future noise

Direct review requests (where you're personally requested) are left alone.

## Setup

### GitHub Token

mutemath requires a **Classic Personal Access Token** (not fine-grained — the notifications API doesn't support them).

1. Go to [github.com/settings/tokens](https://github.com/settings/tokens) and click **Generate new token (classic)**
2. Give it a descriptive name (e.g., `mutemath`)
3. Select the following scopes:
   - **`notifications`** — read and clear notifications
   - **`repo`** — required to fetch PR reviewer data from private repositories (there is no narrower scope that covers the pulls API)
4. Click **Generate token** and copy it
5. If your org uses SSO, click **Configure SSO** next to the token and authorize it for your organization

Set the token as an environment variable:

```
export GH_TOKEN=ghp_your_token_here
```

Using [direnv](https://direnv.net/) with a `.env` file is recommended for local development.

### Build

```
go build -o mutemath .
```

## Usage

```bash
# Dry-run (default) — shows what would be muted, no changes made
mutemath

# Dry-run with verbose output
mutemath --verbose

# Apply changes — mute spam threads and mark them read
mutemath --apply

# Long-running daemon mode — polls GitHub per their X-Poll-Interval header
mutemath --apply --daemon

# Filter by org
mutemath --include-org myorg
mutemath --exclude-org otherorg
```

### Docker

```bash
# Set your token and start the daemon
GH_TOKEN=ghp_... docker compose up -d
```

The container runs mutemath in daemon mode with `restart: unless-stopped`, suitable for running on boot via OrbStack or similar.

## How It Works

For each unread notification:

1. **Filter**: only process notifications with `reason: "review_requested"` and `subject.type: "PullRequest"`
2. **Check reviewers**: fetch the PR's requested reviewers via the GitHub API
3. **Decide**:
   - If your username is in the `users` array → **direct request** → leave it alone
   - If you're only there via a team → **spam** → mute + mark read
4. **Act** (in `--apply` mode): mark the thread read, then set `ignored: true` on the thread subscription

### Daemon Mode

In `--daemon` mode, mutemath polls continuously using GitHub's recommended `X-Poll-Interval` header (typically 60 seconds). It uses conditional requests (`If-Modified-Since`) so 304 responses don't consume rate limits.

## Flags

| Flag | Description |
|------|-------------|
| `--apply` | Perform mutations (default is dry-run) |
| `--verbose` | Detailed output |
| `--daemon` | Long-running mode |
| `--include-org` | Only process notifications from this org |
| `--exclude-org` | Skip notifications from this org |
