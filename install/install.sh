#!/usr/bin/env bash
set -euo pipefail

# One-time install script for proxops on a Proxmox host.
# Prerequisites: config.yml and SSH key already in /opt/proxops/

REPO="aktech/proxops"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/opt/proxops"
SERVICE_FILE="/etc/systemd/system/proxops.service"

ARCH=$(uname -m)
case "$ARCH" in
  x86_64) ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
esac

echo "==> Downloading latest proxops binary (linux/${ARCH})..."
DOWNLOAD_URL=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
  | grep -o "\"browser_download_url\": *\"[^\"]*linux_${ARCH}[^\"]*tar.gz\"" \
  | head -1 \
  | cut -d'"' -f4)

if [ -z "$DOWNLOAD_URL" ]; then
  echo "ERROR: Could not find release download URL"
  exit 1
fi

TMP=$(mktemp -d)
curl -fsSL "$DOWNLOAD_URL" | tar -xz -C "$TMP"
install -m 0755 "$TMP/proxops" "${INSTALL_DIR}/proxops"
rm -rf "$TMP"
echo "    Installed to ${INSTALL_DIR}/proxops"

echo "==> Checking config..."
if [ ! -f "${CONFIG_DIR}/config.yml" ]; then
  echo "ERROR: ${CONFIG_DIR}/config.yml not found"
  echo "Create it first — see README for the config schema."
  exit 1
fi

echo "==> Installing systemd service..."
cp "$(dirname "$0")/proxops.service" "$SERVICE_FILE"
systemctl daemon-reload
systemctl enable --now proxops

echo "==> Done! Check status with: journalctl -u proxops -f"
