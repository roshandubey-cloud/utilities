# Tutorial 05 — Worker fan-out + cumulative cluster reporting

> **Audience:** SREs and capacity engineers running production-scale load
> tests. Anyone who has hit the per-machine FD or NIC ceiling and needs
> to fan out across multiple boxes.
> **Duration:** 3:30 (target 3:20–3:40)
> **Goal:** Spawn two worker nodes via the SSH wizard, distribute one
> unified config across them, watch cumulative metrics aggregate in
> real time, and pull per-worker CSV archives on stop.

After this video the operator should know: when single-machine ceilings
matter; how the SSH wizard installs and starts a worker without
preinstalling anything on the remote box; what the *Distribute load
across workers* toggle does to the Start path; how cumulative reporting
sums per-worker metrics; how per-worker CSVs are pulled and archived on
Stop; and what version-skew detection guards against.

---

## Setup before recording

This tutorial needs two SSH-reachable Linux/macOS hosts. For the recording
we use two local-loopback tunnels to the same machine (`127.0.0.1` on two
different SSH ports), but the script names them `worker-east` and
`worker-west` for clarity.

```bash
# Pre-recording: ensure SSH is enabled on the local box
sudo systemsetup -setremotelogin on    # macOS
# or `sudo systemctl start sshd` on Linux

# Verify ssh works for the recording user (use a real user)
ssh -p 22 $USER@127.0.0.1 echo ok

# Mock SFTP target (our 'production-like' SFTP server)
/tmp/tut/mocksftp -addr 127.0.0.1:2225 -trackid-delay 500ms \
  -pairs "up1=dl1" -persist-content > /tmp/tut/mocksftp.log 2>&1 &

# Fresh app
rm -rf "$HOME/Library/Application Support/sftp-loadtest" \
       "$HOME/Library/WebKit/com.roshandubey.sftp-loadtest-desktop"
open /path/to/sftp-loadtest-desktop.app
```

**Disable Distribute toggle in localStorage** before launch so the cold-
open shows the empty state:
```bash
defaults write com.roshandubey.sftp-loadtest-desktop \
  NSUserDefaults_localStorage_sftp-loadtest-distribute-v1 -string "0"
```

---

## Storyboard

### 0:00 — 0:25 · The single-machine ceiling

**Visual.** Slide / overlay graphic showing one box hitting its limits:
- 1 client → 4096 FD limit
- 1 client → 1 NIC at 10 Gbps
- 1 client → CPU saturated at 60% upload throughput

Then transition to the app on the *Cluster* sidebar view (currently empty:
"No workers configured. Add one to fan out a run.").

**VO.**
> *Single-machine load tests hit ceilings. File descriptor limits.
> Single-NIC bandwidth. CPU saturation when crypto becomes the
> bottleneck. The realistic answer for production-scale tests is fan
> out: spawn the same load across N boxes and aggregate. Sftp-loadtest
> ships with built-in cluster orchestration — no Kubernetes, no
> Ansible, no preinstalled agents on the targets. Just SSH access.
> The Cluster panel shows there are no workers yet. Let's spawn two.*

---

### 0:25 — 1:25 · The SSH wizard, end to end

**Visual.** Click **Add worker**. The 6-step wizard modal opens.

**On-screen action — Step 1 (Add worker URL or SSH).**
1. Wizard shows two paths: paste a Worker URL (an already-running worker)
   or use SSH to spawn one. Click the SSH path.

**VO.**
> *The wizard has two modes. If you've already started a worker on
> a remote machine — say, you're managing it with systemd — paste
> its URL. Otherwise, the SSH path: the master box opens an SSH
> connection to the remote, uploads the binary over SFTP, starts it,
> and tunnels traffic back through the SSH session. No preinstalled
> agent. Pick SSH.*

**On-screen action — Step 2 (where).** Operator types
`SSH host: 127.0.0.1`, `SSH port: 22`, **next**.

**Step 3 (auth).** Operator types `SSH user: $USER`, picks the **password**
auth radio, types their account password. (If using a real worker box
the operator would paste an SSH private key here instead.)

**Step 4 (probe TCP + SSH + Go availability).** Operator clicks
**Probe**. Live log: TCP… SSH… remote shell version… Go binary detected
at `/usr/local/go/bin/go`. Green check.

**Step 5 (binary + listen address).** Wizard auto-fills `bin path:
~/sftp-loadtest-worker`, `listen: 127.0.0.1:9100`. Operator clicks **Spawn**.

**Step 6 (live spawn log).** Status ticker:
1. Uploading binary (12 MB, ~3 sec on a real link)
2. Setting executable bit
3. Starting remote listener
4. Establishing reverse tunnel
5. Worker reachable at http://127.0.0.1:9100

**VO (compress over the whole wizard).**
> *Worker name — worker-east — though we're hitting localhost on
> port twenty-two for the demo. SSH user is my account. Password
> auth for the demo; in production you'd paste an SSH private key.
> Probe — checks TCP, SSH handshake, finds the Go runtime if needed.
> Now the spawn step: the master uploads the worker binary —
> twelve megabytes, takes a few seconds — sets the executable bit,
> starts the remote listener, and establishes a reverse SSH tunnel
> so we can reach the worker without opening any ports on the
> remote box. Total spawn time is typically under ten seconds
> against a healthy remote.*

---

### 1:25 — 1:45 · A second worker

**Visual.** Repeat the wizard with `127.0.0.1:22` (same box) but a different
listen port `127.0.0.1:9101`. Name it `worker-west`. Cluster panel now
shows two workers, both green.

**VO.**
> *Repeat for a second worker — worker-west — different listen
> port. Cluster panel now shows two workers, both healthy. Each
> worker exposes its own /healthz endpoint; the master pings every
> ten seconds for liveness and version info. If a worker drifts to
> a different version, you'll see a yellow chip — version skew
> isn't blocked, but you'll know.*

---

### 1:45 — 2:10 · Distribute toggle + Start

**Visual.** Sidebar → Configure → Workload → top of Upload card.
Now there's a **Distribute load across workers** checkbox row that was
hidden before any workers existed.

**On-screen action.**
1. Tick **Distribute load across workers**. Status to the right reads
   `2 workers enabled · fpm will be split across them`.
2. Configure: same as tutorials — host 127.0.0.1, port 2225, user up1,
   FPM 60, duration 0.01 hours.
3. Start run.

**VO.**
> *Now the magic. Sidebar — Configure. The Workload section now
> shows a Distribute row that wasn't there before; it appeared the
> moment the first worker was added. Tick it. Status reads, two
> workers enabled, fpm will be split. Configure as usual — sixty
> files per minute total — and start the run. Behind the scenes,
> the master posts /api/cluster/start with the unified config. Each
> worker gets thirty fpm, runs independently against the SFTP
> target, reports back. The master synthesises cumulative metrics.*

---

### 2:10 — 2:50 · Cumulative metrics live

**Visual.** Cut to the Records panel during the run. The throughput
chart shows ONE aggregate line. Latency percentiles are computed across
ALL workers' samples. Below the chart, a small cluster status strip
shows per-worker file counts:
```
Cluster · 2 workers · synthesised totals
  worker-east  http://127.0.0.1:9100  18 files · 1.4 MB · 0 errors · v0.14.18
  worker-west  http://127.0.0.1:9101  18 files · 1.4 MB · 0 errors · v0.14.18
```

**VO.**
> *Live metrics. Throughput is now a single aggregate line — the
> sum across both workers, refreshed every two seconds. Latency
> percentiles are computed from ALL workers' samples; p ninety-
> nine reflects whichever worker is slowest. Below the chart, a
> per-worker strip — file counts, megabytes, error counts, version.
> If one worker's error count starts climbing, you spot it here
> before the aggregate dilutes the signal. Version chip in green
> means all workers match the master's version; a yellow chip
> would warn of skew.*

---

### 2:50 — 3:15 · Stop + per-worker archive

**Visual.** Operator clicks Stop. Status flips to grey "Stopping". A
status ticker overlays the run header:
1. *Broadcasting stop to 2 workers…*
2. *Pulling per-worker reports…*
3. *Archived 2/2 reports.*

Then sidebar → **Runs**. The most recent run card shows a `cluster · 2
workers` chip. Operator clicks the small expand arrow on the card; a
drawer reveals per-worker rows with individual CSV download buttons.

**VO.**
> *Stop. The master broadcasts stop to every worker, pulls each
> worker's individual CSV via the reverse tunnel, archives them
> alongside the master's aggregate report. Sidebar — Runs. The
> finished run carries a cluster chip. Expand it — per-worker
> rows, each with its own CSV button. The aggregate CSV at the
> top is what your dashboards consume; the per-worker files are
> what you grep when one worker misbehaves. Both are persisted
> under your reports directory: master report, then a sub-folder
> with per-worker CSVs and a meta.json that records spawn-time,
> binary version, and SSH endpoint.*

---

### 3:15 — 3:30 · Despawn cleanup

**Visual.** Sidebar → Cluster. Each worker has a small **Despawn**
button. Operator clicks Despawn on worker-east. The status ticker:
*Stopping remote listener… closing tunnel… removing binary…*. Worker
disappears. Repeat for worker-west.

**VO.**
> *Despawn cleans up — stops the remote listener, closes the SSH
> tunnel, and optionally removes the binary from the remote box.
> No state left behind on the workers. The same wizard that spawned
> them tears them down. That's cluster fan-out, end to end, with
> one production-grade UI flow.*

---

## VO script (paste-ready)

```
Single-machine load tests hit ceilings. File descriptor limits. Single-
NIC bandwidth. CPU saturation when crypto becomes the bottleneck. The
realistic answer for production-scale tests is fan out: spawn the same
load across N boxes and aggregate. Sftp-loadtest ships with built-in
cluster orchestration — no Kubernetes, no Ansible, no preinstalled
agents on the targets. Just SSH access. The Cluster panel shows there
are no workers yet. Let's spawn two.

The wizard has two modes. If you've already started a worker on a
remote machine — say, you're managing it with systemd — paste its
URL. Otherwise, the SSH path: the master box opens an SSH connection
to the remote, uploads the binary over SFTP, starts it, and tunnels
traffic back through the SSH session. No preinstalled agent. Pick
SSH.

Worker name — worker-east — though we're hitting localhost on port
twenty-two for the demo. SSH user is my account. Password auth for
the demo; in production you'd paste an SSH private key. Probe —
checks TCP, SSH handshake, finds the Go runtime if needed. Now the
spawn step: the master uploads the worker binary — twelve megabytes,
takes a few seconds — sets the executable bit, starts the remote
listener, and establishes a reverse SSH tunnel so we can reach the
worker without opening any ports on the remote box. Total spawn time
is typically under ten seconds against a healthy remote.

Repeat for a second worker — worker-west — different listen port.
Cluster panel now shows two workers, both healthy. Each worker
exposes its own slash health-z endpoint; the master pings every ten
seconds for liveness and version info. If a worker drifts to a
different version, you'll see a yellow chip — version skew isn't
blocked, but you'll know.

Now the magic. Sidebar — Configure. The Workload section now shows a
Distribute row that wasn't there before; it appeared the moment the
first worker was added. Tick it. Status reads, two workers enabled,
fpm will be split. Configure as usual — sixty files per minute total
— and start the run. Behind the scenes, the master posts /api/cluster/
start with the unified config. Each worker gets thirty fpm, runs
independently against the SFTP target, reports back. The master
synthesises cumulative metrics.

Live metrics. Throughput is now a single aggregate line — the sum
across both workers, refreshed every two seconds. Latency percentiles
are computed from ALL workers' samples; p ninety-nine reflects
whichever worker is slowest. Below the chart, a per-worker strip —
file counts, megabytes, error counts, version. If one worker's error
count starts climbing, you spot it here before the aggregate dilutes
the signal. Version chip in green means all workers match the
master's version; a yellow chip would warn of skew.

Stop. The master broadcasts stop to every worker, pulls each worker's
individual CSV via the reverse tunnel, archives them alongside the
master's aggregate report. Sidebar — Runs. The finished run carries a
cluster chip. Expand it — per-worker rows, each with its own CSV
button. The aggregate CSV at the top is what your dashboards consume;
the per-worker files are what you grep when one worker misbehaves.
Both are persisted under your reports directory: master report, then
a sub-folder with per-worker CSVs and a meta-dot-json that records
spawn-time, binary version, and SSH endpoint.

Despawn cleans up — stops the remote listener, closes the SSH tunnel,
and optionally removes the binary from the remote box. No state left
behind on the workers. The same wizard that spawned them tears them
down. That's cluster fan-out, end to end, with one production-grade
UI flow.
```

Word count: ~700 ≈ 4:40 at 150 wpm. Compress with 2× during the wizard
spawn beats and the per-worker drawer reveal. Realistic land at 3:30.

---

## Talking points to call out on screen

- **0:50** — "No preinstalled agent — SSH-only spawn"
- **1:30** — "Reverse SSH tunnel — no ports opened on remote"
- **1:55** — "Distribute row only appears when ≥1 worker exists"
- **2:25** — "Per-worker strip surfaces problem workers before aggregate dilutes signal"
- **3:00** — "Per-worker CSVs + meta.json archived alongside aggregate"
