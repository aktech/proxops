package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// StateList returns the resource addresses tracked in tofu state.
func (t *TofuRunner) StateList(ctx context.Context) ([]string, error) {
	if _, err := os.Stat(filepath.Join(t.tofuDir, "terraform.tfstate")); os.IsNotExist(err) {
		return nil, nil // never applied: nothing tracked
	}
	cmd := exec.CommandContext(ctx, "tofu", "state", "list")
	cmd.Dir = t.tofuDir
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("tofu state list: %w", err)
	}
	return strings.Fields(string(out)), nil
}

// Import brings an existing resource under tofu management.
func (t *TofuRunner) Import(ctx context.Context, address, id string) error {
	t.logger.Info("running tofu import", "address", address, "id", id)
	return t.run(ctx, "import", "-input=false", address, id)
}

// PlanAndApply runs `tofu plan`, refuses any plan that would delete a
// resource (outright or via replace), and otherwise applies exactly the plan
// it checked. Returns true if a plan was applied.
func (t *TofuRunner) PlanAndApply(ctx context.Context) (bool, error) {
	// The plan file must live outside the repo: proxops commits with `git add -A`.
	f, err := os.CreateTemp("", "proxops-*.tfplan")
	if err != nil {
		return false, fmt.Errorf("create plan file: %w", err)
	}
	planPath := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(planPath) }()

	t.logger.Info("running tofu plan")
	cmd := exec.CommandContext(ctx, "tofu", "plan", "-input=false", "-detailed-exitcode", "-out="+planPath)
	cmd.Dir = t.tofuDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return false, nil // exit 0: no changes
	case errors.As(err, &exitErr) && exitErr.ExitCode() == 2:
		// exit 2: changes present, check them below
	default:
		return false, fmt.Errorf("tofu plan: %w", err)
	}

	show := exec.CommandContext(ctx, "tofu", "show", "-json", planPath)
	show.Dir = t.tofuDir
	show.Stderr = os.Stderr
	planJSON, err := show.Output()
	if err != nil {
		return false, fmt.Errorf("tofu show: %w", err)
	}
	deletes, err := destructiveChanges(planJSON)
	if err != nil {
		return false, err
	}
	if len(deletes) > 0 {
		return false, fmt.Errorf("refusing to apply: plan would delete %s", strings.Join(deletes, ", "))
	}

	t.logger.Info("running tofu apply")
	if err := t.run(ctx, "apply", "-input=false", planPath); err != nil {
		return false, err
	}
	return true, nil
}

// destructiveChanges returns the addresses of resources that a plan (the
// JSON from `tofu show -json <planfile>`) would delete, including replaces.
func destructiveChanges(planJSON []byte) ([]string, error) {
	var plan struct {
		ResourceChanges []struct {
			Address string `json:"address"`
			Change  struct {
				Actions []string `json:"actions"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal(planJSON, &plan); err != nil {
		return nil, fmt.Errorf("parse plan JSON: %w", err)
	}
	var addrs []string
	for _, rc := range plan.ResourceChanges {
		for _, action := range rc.Change.Actions {
			if action == "delete" {
				addrs = append(addrs, rc.Address)
				break
			}
		}
	}
	return addrs, nil
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
