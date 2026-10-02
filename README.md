# CronWatch

**Know what ran, what failed, and what never started.**

CronWatch wraps scheduled commands and records their results in a local SQLite
database. A small web dashboard shows job status, run history, duration, and
captured logs. It ships as one Go binary and needs no account or daemon to
record runs.

![CronWatch records cron jobs on a remote server; an SSH tunnel (ssh -L 8765:localhost:8765) brings its local dashboard to your browser at localhost:8765, showing each job's status, last run, duration, and logs.](docs/images/cronwatch-overview.png)

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

The installer downloads the latest release for Linux or macOS, checks its
SHA-256 digest, and installs CronWatch into `~/.local/bin` without sudo:

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

### Dashboard

- **Jobs** lists every job with its status, last run, duration, and next
  expected run, and refreshes every 10 seconds. Below it, **Recent logs** shows
  the end of the latest failing run's output (or the latest run when nothing
  is failing). Lines the command wrote to stderr are shown in red.
- **Runs** lists recent runs across all jobs. A run's page shows its last error,
  and its output opens at the end, with All / Stdout / Stderr views.
- **Logs** searches the output of the last 50 runs; **Errors only** limits
  results to stderr lines.

### Cron example

Take an existing cron entry:

```cron
0 2 * * * /opt/scripts/backup.sh
```

and put `cronwatch run ... --` in front of the command:

```cron
0 2 * * * $HOME/.local/bin/cronwatch run --name "Database Backup" --schedule "0 2 * * *" --grace 10m -- /opt/scripts/backup.sh
```

The line has two schedules, and they do different jobs:

```text
0 2 * * *  $HOME/.local/bin/cronwatch run  --name "Database Backup"  --schedule "0 2 * * *"  --grace 10m  --  /opt/scripts/backup.sh
└───┬───┘  └─────────────┬──────────────┘  └──────────┬───────────┘  └─────────┬──────────┘  └────┬────┘  │   └─────────┬──────────┘
    1                    2                            3                        4                  5       6             7
```

1. **Cron's schedule.** Cron reads this to decide *when to run* the line:
   02:00 every day. CronWatch does not change it.
2. **The wrapper.** Cron starts CronWatch, which starts your command and
   records the result. Use the full path, because cron's `PATH` is minimal.
3. **The job name** shown on the dashboard.
4. **CronWatch's copy of the schedule.** It tells CronWatch *when to expect a
   run*, so it can show the next expected time and flag a run that never
   started. Keep it identical to (1).
5. **Grace period.** How late a run may start before it counts as missed.
   Default 5m.
6. **`--`** ends CronWatch's flags; everything after it is your command.
7. **Your command**, exactly as it was before.

You can leave out `--schedule`: while `cronwatch serve` is running, it reads
your crontab and takes the schedule from (1). Pass `--schedule` when you don't
run `serve`, or when you want the expectation written next to the command.

Shell redirects after the command still work, because CronWatch passes the
command's output through:

```cron
0 2 * * * $HOME/.local/bin/cronwatch run --name "Database Backup" -- /opt/scripts/backup.sh >> $HOME/backup.log 2>&1
```

The cron entry and dashboard must run under the same user to see the same
database. A user-level systemd service example is in
[`examples/cronwatch.service`](examples/cronwatch.service).

## CLI

| Command | Purpose |
| --- | --- |
| [`cronwatch run`](#cronwatch-run) | Run a command and record the result |
| [`cronwatch serve`](#cronwatch-serve) | Serve the local dashboard |
| [`cronwatch jobs`](#cronwatch-jobs) | List jobs and their current status |
| [`cronwatch runs`](#cronwatch-runs) | List recent runs |
| [`cronwatch sync`](#cronwatch-sync) | Register jobs from your crontab before they run |
| [`cronwatch prune`](#cronwatch-prune) | Delete old finished runs |
| [`cronwatch version`](#cronwatch-version) | Print the version |

Run `cronwatch COMMAND --help` (or `cronwatch help COMMAND`) for a command's
flags. Every command except `version` accepts `--data-dir DIR` to choose the
database directory. The `CRONWATCH_DATA_DIR` environment variable sets the default,
which is otherwise `~/.config/cronwatch` on Linux. Flags go before any
positional argument, e.g. `cronwatch runs --data-dir DIR database-backup`.

### `cronwatch run`

```sh
cronwatch run --name NAME [flags] -- command [args...]
```

Runs the command, shows its output as usual, and records the run. CronWatch
exits with the command's exit code, so cron and scripts see the same result as
before.

```console
$ cronwatch run --name "Database Backup" --schedule "0 2 * * *" --grace 10m -- /opt/scripts/backup.sh
Dumping database...
Backup written to /backups/db.sql.gz
```

| Flag | Meaning |
| --- | --- |
| `--name NAME` | Job name shown on the dashboard. Required. |
| `--slug SLUG` | Stable ID for the job. Defaults to the name in lowercase with dashes (`database-backup`). |
| `--schedule "EXPR"` | Five-field cron expression CronWatch should expect the job on. |
| `--grace DURATION` | How late a run may start before it counts as missed, e.g. `10m`. Default `5m`. |
| `--max-log-bytes N` | Output kept per stream. Default 1 MiB, maximum 64 MiB. |
| `--no-echo` | Record output without also printing it. |

`--schedule` and `--grace` only change the stored job when you pass them, so
running a job by hand to test it does not reset its schedule.

When output exceeds `--max-log-bytes`, CronWatch keeps the first and last half
and replaces the middle with a marker, so the error at the end of a failing job
is kept. Treat stored logs as sensitive: command output may contain secrets.

Different names that produce the same slug share one job; CronWatch prints a
warning when that happens, and `--slug` keeps them apart.

### `cronwatch serve`

```sh
cronwatch serve [--addr 127.0.0.1:8765]
```

Serves the dashboard at <http://127.0.0.1:8765>. It is read-only and has no
login, so it only listens on loopback; reach it from another machine with
`ssh -L 8765:localhost:8765 user@server`. Binding to another address requires
`--public`. While running, it also registers jobs from your crontab
([crontab sync](#crontab-sync)) and checks for missed runs every minute.

To start it at boot from cron:

```cron
@reboot /usr/bin/flock -n $HOME/.cache/cronwatch-serve.lock $HOME/.local/bin/cronwatch serve >> $HOME/.cache/cronwatch-serve.log 2>&1
```

### `cronwatch jobs`

Lists every job with its current status:

```console
$ cronwatch jobs
NAME              STATUS     LAST RUN          DURATION
Database Backup   success    2026-10-02 02:00  42s
Generate Reports  failed     2026-10-02 01:00  800ms
Queue worker      never_run  —                 —
```

Statuses: `success`, `failed`, `running`, `cancelled`, `missed` (a scheduled
run never started), `never_run`, and `invalid_schedule`.

### `cronwatch runs`

Lists the 100 most recent runs, optionally for one job by its slug:

```console
$ cronwatch runs database-backup
RUN ID                            JOB              STATUS   STARTED
9b56e10bd024da121ddce143fd49270c  Database Backup  success  2026-10-02 02:00:01
```

Open a run's output on the dashboard at `/runs/RUN-ID`.

### `cronwatch sync`

```sh
cronwatch sync [--crontab FILE]
```

Registers the jobs in your crontab (`crontab -l`, or `FILE`) so they appear on
the dashboard before their first run, and lists lines that are not monitored:

```console
$ cronwatch sync
Added    Queue worker

2 jobs in the crontab, all registered.

Not monitored (1 line without cronwatch run):
  line 3: 30 4 * * 0 docker system prune -f
```

When everything is covered it prints
`8 jobs in the crontab, all registered. Every crontab line is monitored.`
`cronwatch serve` runs the same sync every minute, so you rarely need this by
hand. See [crontab sync](#crontab-sync).

### `cronwatch prune`

```sh
cronwatch prune [--keep N] [--older-than DURATION]
```

Deletes finished runs: `--keep N` keeps the newest N per job, and
`--older-than` deletes runs and missed-run records older than a duration
(`720h` is 30 days). At least one is required. Running runs are never deleted.

```console
$ cronwatch prune --keep 200 --older-than 720h
Deleted 37 runs and 2 missed occurrences.
```

### `cronwatch version`

```console
$ cronwatch version
v0.2.2
```

## Crontab sync

`cronwatch serve` reads your crontab (`crontab -l`) at startup and every
minute, and registers every job a line runs through `cronwatch run`. Jobs
appear on the dashboard as **Never run**, with their next expected time,
before their first run. A line without `--schedule` uses its own cron
schedule. Sync never deletes jobs, and it only corrects the schedule and grace
period of existing jobs; names and commands come from real runs.

Run `cronwatch sync` to do the same by hand. It also lists crontab lines that
are not wrapped with `cronwatch run`, so you can see what is not monitored.
Pass `--sync-crontab=false` to `serve` to turn this off.

## Retention

CronWatch does not delete runs on its own. Schedule `prune` to bound the
database, for example keeping the latest 200 runs per job and nothing older
than 30 days:

```cron
30 3 * * * $HOME/.local/bin/cronwatch prune --keep 200 --older-than 720h
```

Running runs are never pruned. `--older-than` also removes old missed-run
records.

## How missed runs work

CronWatch compares the configured schedule with recorded run start times. Once
an expected start passes its grace period, it is recorded as missed if no run
started between it and the next expected start. Every missed occurrence is
recorded, not just the latest. A job shows **missed** until a run starts.
Missed occurrences are tracked separately from command executions, so a
command that never started is not reported as a failed process. Schedules
follow the server's local time zone.

Detection runs every minute while `cronwatch serve` is running, and each time
`cronwatch jobs`, `runs`, or `prune` is invoked. Changing a job's schedule
starts detection from that moment. A cron expression that can never fire (such
as `0 0 30 2 *`) is rejected by `run`, and shows as `invalid_schedule`.

If a `cronwatch run` process is killed before it can record a result, the run
is marked `failed` with a note in its log once CronWatch sees the process is
gone.

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
