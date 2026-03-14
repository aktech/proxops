# ProxOps

GitOps VM lifecycle manager for Proxmox. Runs on your Proxmox host, continuously reconciling desired state from a Git repo into running VMs with Docker Compose services.

## What it does

ProxOps runs a reconciliation loop:

1. **Git pull** — fetches latest from your infrastructure repo
2. **Load services** — reads `services.yml` (single source of truth)
3. **Reconcile VMs** — assigns VMIDs/IPs to new VMs, runs OpenTofu to create/update them
4. **Bootstrap** — SSHs into new VMs, installs Docker, starts [Doco-CD](https://github.com/kimdre/doco-cd)
5. **Generate configs** — writes Doco-CD poll configs and Traefik routing rules
6. **Commit & push** — pushes generated configs back to the repo

To add a new service, you add an entry to `services.yml` and push. ProxOps handles the rest.

## Prerequisites

- Proxmox host with [OpenTofu](https://opentofu.org/) installed
- A Git repo containing your Docker Compose services and a `services.yml`
- An SSH key for VM access
- A Git access token with push permissions

## Installation

### From release

```bash
curl -fsSL https://raw.githubusercontent.com/aktech/proxops/main/install/install.sh | sudo bash
```

Or manually:

```bash
# Download latest release
curl -fsSL https://github.com/aktech/proxops/releases/latest/download/proxops_<version>_linux_amd64.tar.gz | tar -xz
sudo install -m 0755 proxops /usr/local/bin/proxops
```

### From source

```bash
go install github.com/aktech/proxops@latest
```

## Configuration

Create `/opt/proxops/config.yml`:

```yaml
# Required
location: site1                    # Which location this Proxmox host manages
repo_url: https://github.com/you/infra.git

# Proxmox API
proxmox:
  api_url: https://localhost:8006
  token_id: root@pam!provisioner
  token_secret: <your-token-secret>
  node: pve

# Auth
ssh_private_key: /opt/proxops/id_ed25519
git_access_token: <your-git-token>

# Optional (shown with defaults)
repo_dir: /opt/proxops/repo     # Where to clone the repo locally
poll_interval: 60s                 # How often to run reconciliation
default_user: ubuntu               # SSH user for VMs
data_dir: /opt/proxops           # Data directory on VMs
timezone: UTC                      # Timezone for Doco-CD containers
doco_cd_image: ghcr.io/kimdre/doco-cd:0.67.1
git_reference: refs/heads/main
doco_cd_poll_interval: 120         # Doco-CD poll interval in seconds
committer_email: proxops@localhost
committer_name: proxops
vmid_start: 100                    # VMID allocation range
vmid_end: 999
ip_range_start: 220                # Static IP last-octet range
ip_range_end: 254

# Self-update (disabled by default)
auto_update: true                  # Check GitHub releases and update in-place
update_repo: aktech/proxops      # GitHub owner/repo to check
update_interval: 1h                # How often to check for updates
```

### Self-update

ProxOps can update itself without any network access to the Proxmox host. Two modes:

**Pin a version in `services.yml` (recommended):**

```yaml
proxops_version: "0.3.0"
```

On every reconciliation cycle, after `git pull`, ProxOps checks if the running version matches `proxops_version`. If not, it downloads that exact version from GitHub releases, atomically replaces its own binary, and exits. Systemd `Restart=always` starts the new version automatically.

To upgrade: bump `proxops_version` in `services.yml` and push. To rollback: set it back to the old version.

**Auto-update to latest (`auto_update: true` in config.yml):**

When no `proxops_version` is set in `services.yml`, and `auto_update` is enabled in the local config, ProxOps periodically checks GitHub releases for the latest version (rate-limited by `update_interval`, default 1h).

## services.yml

This file lives in your infrastructure repo at `ansible/services.yml` and defines everything:

```yaml
domain: example.com

proxmox_hosts:
  site1:
    api_host: 10.0.0.1
    api_token_id: root@pam!provisioner
    node: pve
    gateway: 10.0.0.1
    dns_servers: [10.0.0.1, 8.8.8.8]
    netmask: 24
    datastore: local-zfs

vm_defaults:
  template_vmid: 100
  cores: 2
  memory_mb: 2048
  disk_gb: 32
  ansible_user: ubuntu

vms:
  Traefik:
    location: site1
    # vmid and static_ip are auto-assigned if omitted
    services:
      - service_dir: traefik
        compose_file: docker-compose.yml
        project_name: traefik
        primary: true
    routes:
      - name: traefik
        subdomain: traefik
        url: "http://127.0.0.1:8080/"
        auth: true

  MyApp:
    location: site1
    cores: 4              # Override vm_defaults
    memory_mb: 8192
    services:
      - service_dir: myapp
        compose_file: docker-compose.yml
        project_name: myapp
        primary: true
    routes:
      - name: myapp
        subdomain: myapp
        port: 8080
        protocol: https
        auth: true

# Physical devices (traefik routes only, no VM management)
devices:
  site1:
    - name: router
      subdomain: router
      url: "https://10.0.0.1/"
    - name: proxmox
      subdomain: proxmox
      url: "https://10.0.0.1:8006/"
      auth: true
```

## Running

### As a systemd service (recommended)

```bash
# The install script sets this up automatically, or manually:
sudo cp install/proxops.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now proxops

# View logs
journalctl -u proxops -f
```

### One-shot mode

```bash
proxops -config /opt/proxops/config.yml -once
```

### CLI flags

```
-config string    path to config file (default "/opt/proxops/config.yml")
-once             run one reconciliation cycle and exit
-version          print version and exit
```

## Adding a new service

1. Create a directory in your repo with a `docker-compose.yml`
2. Add a VM entry to `services.yml` with services and routes
3. Push to main

ProxOps will automatically:
- Assign a VMID and static IP
- Create the VM via OpenTofu
- Bootstrap it with Docker and Doco-CD
- Generate Traefik routing rules
- Commit the assigned IDs/IPs and generated configs back to the repo

## License

MIT
