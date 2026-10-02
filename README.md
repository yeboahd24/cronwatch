# CronWatch

**Know what ran, what failed, and what never started.**

CronWatch wraps scheduled commands and records their results in a local SQLite
database. A small web dashboard shows job status, run history, duration, and
captured logs. It ships as one Go binary and needs no account or daemon to
record runs.

[Install](#install) · [Quick start](#quick-start) · [CLI](#cli) · [Development](#development)

## Highlights

- **Drop-in command wrapper.** Add `cronwatch run` to an existing cron entry.
- **Useful failure detail.** Capture stdout, stderr, exit codes, and duration.
- **Missed-run detection.** A five-field cron schedule and grace period show
  when a job did not start on time.
- **Local by default.** SQLite storage and a dashboard bound to
  `127.0.0.1:8765`; SSH forwarding covers remote servers.
- **Single binary.** HTML, CSS, JavaScript, and database migrations are embedded.

## Install

Once a release is published, the repository-hosted installer downloads the
matching Linux or macOS archive, checks its SHA-256 digest, and installs
CronWatch into `~/.local/bin` without sudo:

```sh
curl -fsSL https://github.com/yeboahd24/cronwatch/releases/latest/download/install.sh | sh
```

Set `CRONWATCH_INSTALL_DIR` to choose another user-owned directory. Ensure the
install directory is on your `PATH`.

To build from source instead, use Go 1.26 or newer:

```sh
go build -o cronwatch ./cmd/cronwatch
```

## Quick start

Wrap a command and start the dashboard:

```sh
cronwatch run --name "Database Backup" --schedule "0 2 * * *" --grace 10m -- ./backup.sh
cronwatch serve
```

Open <http://localhost:8765>. A command that exits nonzero also makes
`cronwatch run` exit with that code, so existing cron behavior is preserved.

For a remote server, keep the dashboard on its default loopback address and
forward the port from your workstation:

```sh
ssh -L 8765:localhost:8765 user@server
```

Then open <http://localhost:8765> on the workstation. The browser can inspect
jobs and logs; it cannot execute commands.

### Cron example

```cron
0 2 * * * $HOME/.local/bin/cronwatch run --name "Database Backup" --schedule "0 2 * * *" --grace 10m -- /opt/scripts/backup.sh
```

The cron entry and dashboard must run under the same user to see the same
database. A user-level systemd service example is in
[`examples/cronwatch.service`](examples/cronwatch.service).

## CLI

| Command | Purpose |
| --- | --- |
| `cronwatch run --name NAME [flags] -- command [args...]` | Execute and record a command |
| `cronwatch serve [--addr 127.0.0.1:8765]` | Serve the local dashboard |
| `cronwatch jobs` | List jobs and current status |
| `cronwatch runs [job-slug]` | List recent runs |
| `cronwatch version` | Print the version |

`run`, `serve`, `jobs`, and `runs` accept `--data-dir`. The
`CRONWATCH_DATA_DIR` environment variable sets the default directory. Run
`cronwatch run --help` for capture and schedule flags.

Logs are capped at 1 MiB per stream by default. `--max-log-bytes` changes that
limit, and `--no-echo` keeps child output off the terminal while still storing
it. Treat stored logs as sensitive: command output may contain secrets.

## How missed runs work

CronWatch compares the configured schedule with recorded run start times. Once
an expected start passes its grace period, the dashboard shows **missed** if
no run started in that schedule window. Missed occurrences are tracked
separately from command executions, so a command that never started is not
reported as a failed process. Schedules follow the server's local time zone.

## Development

```sh
go test ./...
go vet ./...
sqlc generate
```

Queries live in [`queries/`](queries/), generated Go code in
[`internal/db/`](internal/db/), and goose migrations in
[`migrations/`](migrations/). The build embeds migrations and web assets.

`sh scripts/release.sh VERSION` builds Linux and macOS archives plus checksums
and copies the publishable installer. See
[`examples/install-notes.md`](examples/install-notes.md) for the user-level
service setup.

## License

MIT. See [`LICENSE`](LICENSE).
