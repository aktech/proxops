package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Bootstrapper handles SSH-based setup of new VMs.
type Bootstrapper struct {
	sshKey  []byte
	user    string
	token   string
	dataDir string
	logger  *slog.Logger
}

// NewBootstrapper creates a bootstrapper with the given SSH key.
func NewBootstrapper(keyPath, user, gitToken, dataDir string, logger *slog.Logger) (*Bootstrapper, error) {
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read SSH key %s: %w", keyPath, err)
	}
	return &Bootstrapper{
		sshKey:  key,
		user:    user,
		token:   gitToken,
		dataDir: dataDir,
		logger:  logger,
	}, nil
}

// Bootstrap installs Docker, clones the repo, deploys env, and starts Doco-CD on a new VM.
func (b *Bootstrapper) Bootstrap(ctx context.Context, ip string, vm *VMConfig, repoURL string) error {
	b.logger.Info("bootstrapping VM", "ip", ip, "service", vm.PrimaryService().ServiceDir)

	client, err := b.sshConnect(ip)
	if err != nil {
		return fmt.Errorf("SSH connect to %s: %w", ip, err)
	}
	defer func() { _ = client.Close() }()

	primary := vm.PrimaryService()
	homeDir := fmt.Sprintf("/home/%s", b.user)
	repoDir := fmt.Sprintf("%s/repo", homeDir)
	envFilePath := fmt.Sprintf("%s/doco-cd.env", b.dataDir)

	commands := []struct {
		desc string
		cmd  string
	}{
		{"install Docker", installDockerScript(b.user)},
		{"create directories", fmt.Sprintf("sudo mkdir -p %s && sudo chown %s:%s %s", b.dataDir, b.user, b.user, b.dataDir)},
		{"clone repo", fmt.Sprintf("git clone --depth 1 %s %s || (cd %s && git pull)", repoURL, repoDir, repoDir)},
		{"write doco-cd.env", fmt.Sprintf("echo 'GIT_ACCESS_TOKEN=%s' | sudo tee %s > /dev/null", b.token, envFilePath)},
		{"ensure docker group", fmt.Sprintf("sudo usermod -aG docker %s", b.user)},
		{"stop existing doco-cd", fmt.Sprintf("sg docker -c 'cd %s/%s && docker compose -p doco-cd -f docker-compose.doco-cd.yml down --remove-orphans' 2>/dev/null || true", repoDir, primary.ServiceDir)},
		{"start doco-cd", fmt.Sprintf("sg docker -c 'cd %s/%s && docker compose -p doco-cd -f docker-compose.doco-cd.yml up -d'", repoDir, primary.ServiceDir)},
	}

	for _, c := range commands {
		if err := ctx.Err(); err != nil {
			return err
		}
		b.logger.Info("running", "step", c.desc, "ip", ip)
		output, err := b.runCommand(client, c.cmd)
		if err != nil {
			return fmt.Errorf("%s: %w\noutput: %s", c.desc, err, output)
		}
	}

	b.logger.Info("bootstrap complete", "ip", ip)
	return nil
}

// IsDocoCDRunning checks if a doco-cd container is running on the VM via SSH.
func (b *Bootstrapper) IsDocoCDRunning(ip string) (bool, error) {
	client, err := b.sshConnectQuick(ip)
	if err != nil {
		return false, err
	}
	defer func() { _ = client.Close() }()

	output, err := b.runCommand(client, "sg docker -c \"docker ps --format '{{.Names}}'\" 2>/dev/null")
	if err != nil {
		return false, nil // Docker not running = needs bootstrap
	}
	return strings.Contains(output, "doco-cd"), nil
}

// sshConnectQuick establishes an SSH connection with a short timeout (no retries).
func (b *Bootstrapper) sshConnectQuick(ip string) (*ssh.Client, error) {
	signer, err := ssh.ParsePrivateKey(b.sshKey)
	if err != nil {
		return nil, fmt.Errorf("parse SSH key: %w", err)
	}
	config := &ssh.ClientConfig{
		User:            b.user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	return ssh.Dial("tcp", net.JoinHostPort(ip, "22"), config)
}

// sshConnect establishes an SSH connection to the given IP.
func (b *Bootstrapper) sshConnect(ip string) (*ssh.Client, error) {
	signer, err := ssh.ParsePrivateKey(b.sshKey)
	if err != nil {
		return nil, fmt.Errorf("parse SSH key: %w", err)
	}

	config := &ssh.ClientConfig{
		User: b.user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	// Retry connection for up to 2 minutes (VM may still be booting)
	var client *ssh.Client
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		client, err = ssh.Dial("tcp", net.JoinHostPort(ip, "22"), config)
		if err == nil {
			return client, nil
		}
		time.Sleep(5 * time.Second)
	}
	return nil, fmt.Errorf("SSH connection failed after retries: %w", err)
}

// runCommand executes a command over SSH and returns the combined output.
func (b *Bootstrapper) runCommand(client *ssh.Client, cmd string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	defer func() { _ = session.Close() }()

	output, err := session.CombinedOutput(cmd)
	return strings.TrimSpace(string(output)), err
}

func installDockerScript(user string) string {
	return fmt.Sprintf(`#!/bin/bash
set -e
if command -v docker &>/dev/null; then
    echo "Docker already installed"
    exit 0
fi
sudo apt-get update -qq
sudo apt-get install -y -qq ca-certificates curl
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
sudo apt-get update -qq
sudo apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-compose-plugin
sudo usermod -aG docker %s
echo "Docker installed successfully"
`, user)
}
