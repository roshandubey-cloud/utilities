# Run as a systemd service (Linux)

Boot at OS start, restart on crash, log to journald — the standard way to run
sftp-loadtest as a long-lived service on any modern Linux distro.

Tested on Ubuntu 22.04 / 24.04, Debian 12, RHEL 9, Fedora 40, Amazon Linux 2023.

---

## One-time install

```sh
# 1. Download + extract
cd /tmp
curl -LO https://github.com/roshandubey-cloud/utilities/releases/latest/download/sftp-loadtest-linux.zip
unzip sftp-loadtest-linux.zip

# 2. Install the binary system-wide
sudo mkdir -p /opt/sftp-loadtest
sudo cp sftp-loadtest-linux-amd64 /opt/sftp-loadtest/sftp-loadtest    # or -arm64
sudo chmod +x /opt/sftp-loadtest/sftp-loadtest

# 3. Create a dedicated service user (no shell, no home — minimal blast radius)
sudo useradd --system --no-create-home --shell /usr/sbin/nologin sftploadtest

# 4. Create state dirs the service user can write to
sudo mkdir -p /var/lib/sftp-loadtest/{reports,schedules}
sudo chown -R sftploadtest:sftploadtest /var/lib/sftp-loadtest
```

---

## Drop the unit file

Save as `/etc/systemd/system/sftp-loadtest.service`:

```ini
[Unit]
Description=SFTP Load Test
Documentation=https://github.com/roshandubey-cloud/utilities/tree/main/sftp-loadtest
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/opt/sftp-loadtest/sftp-loadtest \
    -addr 127.0.0.1:8080 \
    -reports-dir /var/lib/sftp-loadtest/reports \
    -schedules-dir /var/lib/sftp-loadtest/schedules
User=sftploadtest
Group=sftploadtest
Restart=always
RestartSec=5s
LimitNOFILE=65536

# Hardening — defensible defaults
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/var/lib/sftp-loadtest

[Install]
WantedBy=multi-user.target
```

The two lines that make it auto-start at boot:

- **`WantedBy=multi-user.target`** — systemd starts the service whenever it
  reaches the multi-user runlevel during boot.
- **`Restart=always`** — if the binary ever crashes, systemd restarts it
  within 5 seconds. Survives kernel oops, OOM kills, network blips, etc.

---

## Enable + start

```sh
sudo systemctl daemon-reload                # systemd picks up the new unit
sudo systemctl enable --now sftp-loadtest   # link into boot AND start now
```

(`enable` alone wires it into boot; `start` runs it right now; `--now` does
both in one command.)

---

## Verify

```sh
sudo systemctl status sftp-loadtest         # look for "active (running)" + "enabled"
systemctl is-enabled sftp-loadtest          # → enabled
curl http://127.0.0.1:8080/healthz          # → {"status":"ok",…}
```

Confirm it actually survives a reboot:

```sh
sudo reboot
# … wait, ssh back in …
sudo systemctl status sftp-loadtest         # should be active right after login
```

---

## Tail the logs

systemd captures stdout/stderr into journald. No log files to rotate.

```sh
sudo journalctl -u sftp-loadtest -f                 # follow live (like tail -f)
sudo journalctl -u sftp-loadtest --since "1 hour ago"
sudo journalctl -u sftp-loadtest -p err             # errors only
sudo journalctl -u sftp-loadtest -b                 # since this boot
```

---

## Reaching the UI from your laptop

The unit binds to `127.0.0.1` because the web UI ships **without
authentication**. SSH-tunnel from your laptop:

```sh
ssh -L 8080:localhost:8080 user@server
# now open http://localhost:8080 in your local browser
```

If you must expose publicly, do it through nginx with basic-auth — don't
change `-addr` to `0.0.0.0` on a network anyone can reach.

---

## Common operations

```sh
# Upgrade to a new release
sudo systemctl stop sftp-loadtest
sudo cp /tmp/sftp-loadtest-linux-amd64 /opt/sftp-loadtest/sftp-loadtest
sudo systemctl start sftp-loadtest

# Restart after editing the unit file
sudo systemctl daemon-reload && sudo systemctl restart sftp-loadtest

# Don't auto-start at next boot (keeps installed)
sudo systemctl disable sftp-loadtest

# Stop right now without disabling
sudo systemctl stop sftp-loadtest

# Uninstall completely
sudo systemctl disable --now sftp-loadtest
sudo rm /etc/systemd/system/sftp-loadtest.service
sudo systemctl daemon-reload
sudo rm -rf /opt/sftp-loadtest
sudo userdel sftploadtest
sudo rm -rf /var/lib/sftp-loadtest          # only if you don't want the historical CSVs
```

---

## Troubleshooting

```sh
# Why did the last start attempt fail?
sudo systemctl status sftp-loadtest
sudo journalctl -u sftp-loadtest -n 50
```

| Symptom | Likely cause | Fix |
|---|---|---|
| `Permission denied` writing reports | `sftploadtest` can't write `/var/lib/sftp-loadtest/reports` | `sudo chown -R sftploadtest:sftploadtest /var/lib/sftp-loadtest` |
| `address already in use` | Something else owns `:8080` | Change `-addr` in the unit, `daemon-reload`, restart |
| `exec format error` | Wrong arch (you copied amd64 onto an arm64 box, or vice versa) | Use the matching binary from the linux zip |
| Restart loop | Bad path in `ExecStart`, or unit references a flag the binary doesn't have | Read `journalctl` — the error is on the line right before each restart |
| Web UI unreachable from laptop | Service is bound to `127.0.0.1` (correct default — no auth on UI) | Tunnel: `ssh -L 8080:localhost:8080 user@server` |
| `too many open files` mid-run | Concurrency exceeds the soft FD limit | The unit already sets `LimitNOFILE=65536`; bump higher if you're running 100+ users × 30 streams |

---

## Where everything lives once installed

| Path | Purpose |
|---|---|
| `/opt/sftp-loadtest/sftp-loadtest` | The binary |
| `/etc/systemd/system/sftp-loadtest.service` | The unit file |
| `/var/lib/sftp-loadtest/reports/` | Finished-run CSVs + meta JSONs |
| `/var/lib/sftp-loadtest/schedules/` | Pending scheduled runs (one JSON per schedule) |
| `journalctl -u sftp-loadtest` | All logs |
