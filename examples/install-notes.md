# User-local installation

Once `install.sh` and the release archives are published with the repository's
releases, install without sudo:

```sh
curl -fsSL https://github.com/yeboahd24/cronwatch/releases/latest/download/install.sh | sh
```

The installer downloads the archive for the current Linux or macOS CPU,
checks its SHA-256 digest, and installs `cronwatch` to `~/.local/bin`.
Set `CRONWATCH_INSTALL_DIR` to choose another user-owned directory.

From a source checkout, create the same release artifacts with:

```sh
sh scripts/release.sh v0.1.0
```

Publish everything in `dist/` as assets of the repository release. The
generated `dist/install.sh` points to the release URL, so its users need
only the one-line command above.

To keep the dashboard running under systemd without a system service:

```sh
mkdir -p ~/.config/systemd/user
cp examples/cronwatch.service ~/.config/systemd/user/cronwatch.service
systemctl --user daemon-reload
systemctl --user enable --now cronwatch
```

Cron commands and the dashboard must run under the same user so they use the
same SQLite data directory. On a remote server, connect from a laptop with:

```sh
ssh -L 8765:localhost:8765 user@server
```

Then open `http://localhost:8765`.
