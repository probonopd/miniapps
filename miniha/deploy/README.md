# Deploying MiniHA

MiniHA is one static binary. It runs as the unprivileged user `miniha`,
keeps its state in `/var/lib/miniha`, and listens on TCP 8123 (HTTP and
WebSocket API, web UI) and TCP 1883 (embedded MQTT broker). Neither port
needs root.

## 1. Install the binary

Build it on any machine with Go 1.23 or newer:

    make build
    install -m 0755 miniha /usr/local/bin/miniha

For another target, use `make all` (cross-builds into `dist/`) and copy the
matching `dist/miniha-<os>-<arch>` file.

## 2. Create the user and data directory

Linux (glibc or musl):

    useradd --system --home-dir /var/lib/miniha --shell /usr/sbin/nologin miniha
    install -d -o miniha -g miniha -m 0750 /var/lib/miniha

FreeBSD:

    pw useradd miniha -d /var/lib/miniha -s /usr/sbin/nologin -c "MiniHA" -w no
    install -d -o miniha -g miniha -m 0750 /var/lib/miniha

OpenBSD:

    useradd -d /var/lib/miniha -s /sbin/nologin -L daemon miniha
    install -d -o miniha -g miniha -m 0750 /var/lib/miniha

The first `miniha run` writes `configuration.yaml` into the data directory.
Set the admin password as the service user, so the files stay owned by
`miniha`: `sudo -u miniha /usr/local/bin/miniha -data /var/lib/miniha set-password`.

## 3. Install a service

Start the hub by hand once to check it, then install the matching service
file. Each one runs `/usr/local/bin/miniha -data /var/lib/miniha run`.

### systemd (Debian, Fedora, Arch, openSUSE, ...)

    install -m 0644 deploy/systemd/miniha.service /etc/systemd/system/miniha.service
    systemctl daemon-reload
    systemctl enable --now miniha

The unit creates `/var/lib/miniha` itself if it is missing, restarts the hub
after a crash, and grants no capabilities. The hardening (`ProtectSystem=strict`,
`ProtectHome`, `PrivateTmp`, and so on) limits writes to the data directory.

### OpenRC (Alpine, Gentoo, Void with OpenRC)

    install -m 0755 deploy/openrc/miniha /etc/init.d/miniha
    rc-update add miniha default
    rc-service miniha start

The service uses `supervise-daemon`, which restarts a crashed hub. To use a
different data directory, set `MINIHA_DIR` in `/etc/conf.d/miniha`.

### FreeBSD

    install -m 0555 deploy/freebsd/rc.d/miniha /usr/local/etc/rc.d/miniha
    sysrc miniha_enable=YES
    service miniha start

Settings go in `/etc/rc.conf`: `miniha_user` (default `miniha`) and
`miniha_datadir` (default `/var/lib/miniha`). `daemon(8)` supervises the
hub and restarts it after a crash.

### OpenBSD

    install -m 0555 deploy/openbsd/rc.d/miniha /etc/rc.d/miniha
    rcctl enable miniha
    rcctl start miniha

The data directory and user are fixed in the script (`/var/lib/miniha`,
`miniha`); edit the script to change them.

## 4. Logs

- systemd: the journal, `journalctl -u miniha -f`.
- OpenRC, FreeBSD and OpenBSD: `/var/log/miniha.log`. The init script creates
  it owned by `miniha`. Add a rotation rule (`newsyslog` on FreeBSD and
  OpenBSD, `logrotate` on Linux) for long-running installs.
- Foreground runs print to stderr.

## 5. Verify

    curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8123/

Any HTTP status means the API is up.
