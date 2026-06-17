#!/usr/bin/env bash
# Installs http-cmd-runner as a systemd service on a Linux server.
#
# Run from the project directory on the server:
#   sudo ./install.sh
#
# It builds the binary (needs Go), creates an unprivileged user, installs a
# config bound to 127.0.0.1 (localhost only), and starts the service.
# Reach it remotely over an SSH tunnel — see the README.
set -euo pipefail

BIN_NAME="http-cmd-runner"
INSTALL_BIN="/usr/local/bin/${BIN_NAME}"
CONFIG_DIR="/etc/${BIN_NAME}"
CONFIG_FILE="${CONFIG_DIR}/config.json"
SERVICE_FILE="/etc/systemd/system/${BIN_NAME}.service"
RUN_USER="cmdrunner"

if [[ $EUID -ne 0 ]]; then
  echo "Please run with sudo: sudo ./install.sh" >&2
  exit 1
fi

# 1. Build the binary (prefers an already-built ./http-cmd-runner if present).
if [[ -x "./${BIN_NAME}" ]]; then
  echo "==> Using prebuilt ./${BIN_NAME}"
elif command -v go >/dev/null 2>&1; then
  echo "==> Building ${BIN_NAME} from source"
  CGO_ENABLED=0 go build -o "./${BIN_NAME}" .
else
  echo "ERROR: no ./${BIN_NAME} binary and Go is not installed." >&2
  echo "Install Go (https://go.dev/dl/) or copy a prebuilt binary here." >&2
  exit 1
fi

# 2. Create an unprivileged service user.
if ! id "${RUN_USER}" >/dev/null 2>&1; then
  echo "==> Creating user ${RUN_USER}"
  useradd -r -s /usr/sbin/nologin "${RUN_USER}"
fi

# 3. Install binary + config (config bound to localhost by default).
echo "==> Installing binary to ${INSTALL_BIN}"
install -m 0755 "./${BIN_NAME}" "${INSTALL_BIN}"

mkdir -p "${CONFIG_DIR}"
if [[ ! -f "${CONFIG_FILE}" ]]; then
  echo "==> Installing config to ${CONFIG_FILE}"
  install -m 0644 ./config.example.json "${CONFIG_FILE}"
else
  echo "==> Keeping existing ${CONFIG_FILE}"
fi

# 4. Install the systemd unit.
echo "==> Installing systemd unit"
install -m 0644 ./http-cmd-runner.service "${SERVICE_FILE}"

# 5. Start it.
systemctl daemon-reload
systemctl enable --now "${BIN_NAME}"

echo
echo "==> Done. Status:"
systemctl --no-pager --full status "${BIN_NAME}" || true
echo
echo "The API is listening on 127.0.0.1:8080 (localhost only)."
echo "Test locally on the server:"
echo "  curl http://127.0.0.1:8080/exec -H 'Content-Type: application/json' -d '{\"command\":\"uptime\"}'"
echo
echo "Reach it from your laptop over an SSH tunnel:"
echo "  ssh -N -L 8080:127.0.0.1:8080 user@SERVER_IP"
echo "  # then on your laptop: curl http://127.0.0.1:8080/exec ..."
