# ProxPilot

GitOps VM lifecycle manager for Proxmox. Runs on your Proxmox host, continuously reconciling desired state from a Git repo into running VMs with Docker Compose services.

## What it does

ProxPilot runs a reconciliation loop:

1. **Git pull** — fetches latest from your infrastructure repo
2. **Load services** — reads `services.yml` (single source of truth)
3. **Reconcile VMs** — assigns VMIDs/IPs to new VMs, runs OpenTofu to create/update them
4. **Bootstrap** — SSHs into new VMs, installs Docker, starts [Doco-CD](https://github.com/kimdre/doco-cd)
5. **Generate configs** — writes Doco-CD poll configs and Traefik routing rules
6. **Commit & push** — pushes generated configs back to the repo

To add a new service, you add an entry to `services.yml` and push. ProxPilot handles the rest.

## Prerequisites

- Proxmox host with [OpenTofu](https://opentofu.org/) installed
- A Git repo containing your Docker Compose services and a `services.yml`
- An SSH key for VM access
- A Git access token with push permissions

## Installation

### From release

```bash
curl -fsSL https://raw.githubusercontent.com/aktech/proxpilot/main/install/install.sh | sudo bash
```

Or manually:

```bash
# Download latest release
curl -fsSL https://github.com/aktech/proxpilot/releases/latest/download/proxpilot_<version>_linux_amd64.tar.gz | tar -xz
sudo install -m 0755 proxpilot /usr/local/bin/proxpilot
```

### From source

```bash
go install github.com/aktech/proxpilot@latest
```

## Configuration

Create `/opt/proxpilot/config.yml`:

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
ssh_private_key: /opt/proxpilot/id_ed25519
git_access_token: <your-git-token>

# Optional (shown with defaults)
repo_dir: /opt/proxpilot/repo     # Where to clone the repo locally
poll_interval: 60s                 # How often to run reconciliation
default_user: ubuntu               # SSH user for VMs
data_dir: /opt/proxpilot           # Data directory on VMs
timezone: UTC                      # Timezone for Doco-CD containers
doco_cd_image: ghcr.io/kimdre/doco-cd:0.67.1
git_reference: refs/heads/main
doco_cd_poll_interval: 120         # Doco-CD poll interval in seconds
committer_email: proxpilot@localhost
committer_name: proxpilot
vmid_start: 100                    # VMID allocation range
vmid_end: 999
ip_range_start: 220                # Static IP last-octet range
ip_range_end: 254

# Self-update (disabled by default)
auto_update: true                  # Check GitHub releases and update in-place
update_repo: aktech/proxpilot      # GitHub owner/repo to check
update_interval: 1h                # How often to check for updates
```

### Self-update

When `auto_update: true`, ProxPilot periodically checks GitHub releases for a newer version. If found, it downloads the binary, atomically replaces itself, and exits. Since systemd has `Restart=always`, the new version starts automatically — no SSH access or manual intervention required.

To trigger an update: push a new tag (e.g. `v0.2.0`) to the `update_repo`. ProxPilot will pick it up within `update_interval`.

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
sudo cp install/proxpilot.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now proxpilot

# View logs
journalctl -u proxpilot -f
```

### One-shot mode

```bash
proxpilot -config /opt/proxpilot/config.yml -once
```

### CLI flags

```
-config string    path to config file (default "/opt/proxpilot/config.yml")
-once             run one reconciliation cycle and exit
-version          print version and exit
```

## Adding a new service

1. Create a directory in your repo with a `docker-compose.yml`
2. Add a VM entry to `services.yml` with services and routes
3. Push to main

ProxPilot will automatically:
- Assign a VMID and static IP
- Create the VM via OpenTofu
- Bootstrap it with Docker and Doco-CD
- Generate Traefik routing rules
- Commit the assigned IDs/IPs and generated configs back to the repo

## License

MIT
