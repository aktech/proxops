package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
)

// TofuRunner generates tfvars from services.yml and runs `tofu apply`.
type TofuRunner struct {
	tofuDir string // directory containing .tf files (the tofu/proxmox module)
	logger  *slog.Logger
}

// NewTofuRunner creates a tofu runner.
func NewTofuRunner(tofuDir string, logger *slog.Logger) *TofuRunner {
	return &TofuRunner{tofuDir: tofuDir, logger: logger}
}

// TofuVM is the per-VM structure written into terraform.tfvars.json.
type TofuVM struct {
	VMID     int      `json:"vmid"`
	Name     string   `json:"name"`
	Cores    int      `json:"cores"`
	MemoryMB int      `json:"memory_mb"`
	DiskGB   int      `json:"disk_gb"`
	CPUType  string   `json:"cpu_type"`
	StaticIP string   `json:"static_ip"`
}

// TofuVars is the top-level structure for terraform.tfvars.json.
type TofuVars struct {
	ProxmoxAPIURL     string            `json:"proxmox_api_url"`
	ProxmoxTokenID    string            `json:"proxmox_token_id"`
	ProxmoxTokenSecret string           `json:"proxmox_token_secret"`
	ProxmoxNode       string            `json:"proxmox_node"`
	TemplateVMID      int               `json:"template_vmid"`
	Gateway           string            `json:"gateway"`
	DNSServers        []string          `json:"dns_servers"`
	Netmask           int               `json:"netmask"`
	CloudInitDatastore string           `json:"cloud_init_datastore"`
	SSHUser           string            `json:"ssh_user"`
	VMs               map[string]TofuVM `json:"vms"`
}

// GenerateVars builds terraform.tfvars.json from services.yml for a location.
func (t *TofuRunner) GenerateVars(services *ServicesFile, location string, proxmoxCfg ProxmoxConfig) error {
	pxHost, ok := services.ProxmoxHosts[location]
	if !ok {
		return fmt.Errorf("no proxmox_hosts entry for location %q", location)
	}

	vars := TofuVars{
		ProxmoxAPIURL:      fmt.Sprintf("https://%s:8006/", pxHost.APIHost),
		ProxmoxTokenID:     proxmoxCfg.TokenID,
		ProxmoxTokenSecret: proxmoxCfg.TokenSecret,
		ProxmoxNode:        pxHost.Node,
		TemplateVMID:       services.VMDefaults.TemplateVMID,
		Gateway:            pxHost.Gateway,
		DNSServers:         pxHost.DNSServers,
		Netmask:            pxHost.Netmask,
		CloudInitDatastore: pxHost.Datastore,
		SSHUser:            services.VMDefaults.AnsibleUser,
		VMs:                make(map[string]TofuVM),
	}

	for vmName, vm := range services.VMsForLocation(location) {
		if vm.VMID == 0 || vm.StaticIP == "" {
			return fmt.Errorf("VM %q missing vmid or static_ip — assign before running tofu", vmName)
		}
		vars.VMs[vmName] = TofuVM{
			VMID:     vm.VMID,
			Name:     vmName,
			Cores:    vm.EffectiveCores(services.VMDefaults),
			MemoryMB: vm.EffectiveMemoryMB(services.VMDefaults),
			DiskGB:   vm.EffectiveDiskGB(services.VMDefaults),
			CPUType:  vm.EffectiveCPUType(services.VMDefaults),
			StaticIP: vm.StaticIP,
		}
	}

	varsPath := filepath.Join(t.tofuDir, "terraform.tfvars.json")
	data, err := json.MarshalIndent(vars, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal tfvars: %w", err)
	}
	if err := os.WriteFile(varsPath, data, 0600); err != nil {
		return fmt.Errorf("write tfvars: %w", err)
	}
	t.logger.Info("wrote terraform.tfvars.json", "vms", len(vars.VMs))
	return nil
}

// Init runs `tofu init` if not already initialized.
func (t *TofuRunner) Init(ctx context.Context) error {
	lockFile := filepath.Join(t.tofuDir, ".terraform.lock.hcl")
	if _, err := os.Stat(lockFile); err == nil {
		return nil // already initialized
	}
	t.logger.Info("running tofu init")
	return t.run(ctx, "init")
}

// Apply runs `tofu apply -auto-approve`.
func (t *TofuRunner) Apply(ctx context.Context) error {
	t.logger.Info("running tofu apply")
	return t.run(ctx, "apply", "-auto-approve")
}

// Plan runs `tofu plan` and returns true if there are changes.
func (t *TofuRunner) Plan(ctx context.Context) (bool, error) {
	t.logger.Info("running tofu plan")
	cmd := exec.CommandContext(ctx, "tofu", "plan", "-detailed-exitcode")
	cmd.Dir = t.tofuDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err == nil {
		return false, nil // exit 0 = no changes
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 2 {
		return true, nil // exit 2 = changes present
	}
	return false, fmt.Errorf("tofu plan: %w", err)
}

func (t *TofuRunner) run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "tofu", args...)
	cmd.Dir = t.tofuDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tofu %v: %w", args, err)
	}
	return nil
}
