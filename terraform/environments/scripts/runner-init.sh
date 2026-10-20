#!/bin/bash
set -e

# Divert logs to cloud-init log for troubleshooting
exec > >(tee -a /var/log/runner-init.log) 2>&1
echo "=========================================================================="
echo " Starting GitHub Self-Hosted Runner Initialization: $(date)"
echo " Environment: ${ENV_NAME} | Repository: ${GITHUB_REPO}"
echo "=========================================================================="

# 1. Update OS and install essential system tools
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y ca-certificates curl gnupg lsb-release jq git apt-transport-https cron

# 2. Install Docker Engine
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
chmod a+r /etc/apt/keyrings/docker.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" | tee /etc/apt/sources.list.d/docker.list > /dev/null
apt-get update -y
apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
systemctl enable docker
systemctl start docker

# 3. Install Azure CLI
curl -sL https://aka.ms/InstallAzureCLIDeb | bash

# 4. Create dedicated non-root runner user with Docker permissions
if ! id "runner" &>/dev/null; then
  useradd -m -s /bin/bash runner
fi
usermod -aG docker runner

# 5. Download GitHub Actions Runner package
RUNNER_DIR="/home/runner/actions-runner"
mkdir -p "$RUNNER_DIR"
cd "$RUNNER_DIR"

# GitHub Actions enforces minimum runner version 2.329.0 as of early 2026
RUNNER_VERSION="2.329.0"
LATEST_TAG=$(curl -s "https://api.github.com/repos/actions/runner/releases/latest" | jq -r .tag_name | tr -d 'v' || echo "")
if [ -n "$LATEST_TAG" ] && [ "$LATEST_TAG" != "null" ]; then
  RUNNER_VERSION="$LATEST_TAG"
fi

if [ ! -f "actions-runner-linux-x64-$${RUNNER_VERSION}.tar.gz" ]; then
  curl -o "actions-runner-linux-x64-$${RUNNER_VERSION}.tar.gz" -L "https://github.com/actions/runner/releases/download/v$${RUNNER_VERSION}/actions-runner-linux-x64-$${RUNNER_VERSION}.tar.gz"
  tar xzf "./actions-runner-linux-x64-$${RUNNER_VERSION}.tar.gz"
  chown -R runner:runner /home/runner
fi

# 6. Request Registration Token using GitHub PAT and configure runner service
if [ -n "${GITHUB_PAT}" ]; then
  echo "Obtaining runner registration token from GitHub API for ${GITHUB_REPO}..."
  REG_TOKEN=$(curl -sX POST -H "Accept: application/vnd.github.v3+json" \
    -H "Authorization: token ${GITHUB_PAT}" \
    "https://api.github.com/repos/${GITHUB_REPO}/actions/runners/registration-token" | jq -r .token)

  if [ "$REG_TOKEN" != "null" ] && [ -n "$REG_TOKEN" ]; then
    echo "Registering runner with labels: self-hosted, azure-vnet, ${ENV_NAME}..."
    su - runner -c "$RUNNER_DIR/config.sh --url https://github.com/${GITHUB_REPO} --token $REG_TOKEN --name runner-${NAME_PREFIX} --labels self-hosted,azure-vnet,${ENV_NAME} --unattended --replace"
    cd "$RUNNER_DIR"
    ./svc.sh install runner
    ./svc.sh start
    echo "GitHub Self-Hosted Runner service started successfully!"
  else
    echo "ERROR: Failed to retrieve runner registration token. Verify GITHUB_PAT permissions (repo scope)."
  fi
else
  echo "WARNING: GITHUB_PAT not supplied; runner unconfigured. Configure manually or supply PAT."
fi

# 7. Configure recurring Docker prune cron job to prevent disk exhaustion
echo "Configuring recurring Docker prune cron job..."
cat << 'CRON' > /etc/cron.d/docker-prune
0 2 * * * root /usr/bin/docker system prune -af --filter "until=48h" >> /var/log/docker-prune.log 2>&1
CRON
chmod 0644 /etc/cron.d/docker-prune
systemctl enable cron
systemctl restart cron
echo "Docker prune cron schedule active: daily at 02:00 AM (until=48h)"
