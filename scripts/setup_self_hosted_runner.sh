#!/usr/bin/env bash
# ==============================================================================
# scripts/setup_self_hosted_runner.sh
# 1-Click Remote Self-Hosted GitHub Actions Runner & Map Storage Setup
# Run this on your remote VPS (Ubuntu 22.04 / 24.04 LTS) as root.
# ==============================================================================
set -euo pipefail

RUNNER_REPO_URL="${1:-https://github.com/qwaudai6-hash/OMNIGO-APP-3}"
RUNNER_TOKEN="${2:-}"

if [[ -z "$RUNNER_TOKEN" ]]; then
    echo "Usage: sudo ./scripts/setup_self_hosted_runner.sh <REPO_URL> <GITHUB_RUNNER_TOKEN>"
    echo "Example: sudo ./scripts/setup_self_hosted_runner.sh https://github.com/qwaudai6-hash/OMNIGO-APP-3 AABC12345XYZ"
    exit 1
fi

echo "==> [Step 1/5] Installing dependencies & Docker Buildx..."
apt-get update && apt-get install -y curl wget git jq aria2 pigz tar ca-certificates gnupg lsb-release

install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
chmod a+r /etc/apt/keyrings/docker.gpg

echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" | tee /etc/apt/sources.list.d/docker.list > /dev/null

apt-get update && apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

echo "==> [Step 2/5] Creating persistent map storage directory (/opt/omnigo/map-data)..."
mkdir -p /opt/omnigo/map-data/{osm,tiles,routing,geocoding}
mkdir -p /opt/omnigo/actions-runner
useradd -m -s /bin/bash omnigo-runner || true
usermod -aG docker omnigo-runner
chown -R omnigo-runner:omnigo-runner /opt/omnigo

echo "==> [Step 3/5] Downloading GitHub Actions Runner..."
cd /opt/omnigo/actions-runner
RUNNER_VERSION="2.321.0"
curl -o actions-runner-linux-x64-${RUNNER_VERSION}.tar.gz -L \
    https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-linux-x64-${RUNNER_VERSION}.tar.gz
tar xzf actions-runner-linux-x64-${RUNNER_VERSION}.tar.gz
rm actions-runner-linux-x64-${RUNNER_VERSION}.tar.gz
chown -R omnigo-runner:omnigo-runner /opt/omnigo/actions-runner

echo "==> [Step 4/5] Registering Runner with GitHub Repository..."
su - omnigo-runner -c "cd /opt/omnigo/actions-runner && ./config.sh --url ${RUNNER_REPO_URL} --token ${RUNNER_TOKEN} --labels omnigo-vps,self-hosted --unattended --replace"

echo "==> [Step 5/5] Installing and starting systemd daemon..."
cd /opt/omnigo/actions-runner
./svc.sh install omnigo-runner
./svc.sh start

echo "=============================================================================="
echo "[SUCCESS] Self-hosted runner is now online and ready to accept jobs!"
echo "Persistent map storage ready at: /opt/omnigo/map-data"
echo "=============================================================================="
