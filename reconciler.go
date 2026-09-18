package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"sort"
	"time"
)

// Reconciler compares desired VM state (services.yml) with actual state
// and uses OpenTofu to converge infrastructure.
type Reconciler struct {
	tofu   *TofuRunner
	cfg    *Config
	logger *slog.Logger
}

// NewReconciler creates a reconciler.
func NewReconciler(tofu *TofuRunner, cfg *Config, logger *slog.Logger) *Reconciler {
	return &Reconciler{tofu: tofu, cfg: cfg, logger: logger}
}

// ReconcileResult describes what happened during reconciliation.
type ReconcileResult struct {
	NewVMs      []string // VM names that are new (need bootstrap)
	TofuApplied bool     // whether tofu apply ran
}

// Reconcile assigns IDs/IPs to new VMs, generates tfvars, and runs tofu apply.
func (r *Reconciler) Reconcile(ctx context.Context, services *ServicesFile) (*ReconcileResult, error) {
	localVMs := services.VMsForLocation(r.cfg.Location)
	if len(localVMs) == 0 {
		r.logger.Info("no VMs for this location", "location", r.cfg.Location)
		return &ReconcileResult{}, nil
	}

	pxHost, ok := services.ProxmoxHosts[r.cfg.Location]
	if !ok {
		return nil, fmt.Errorf("no proxmox_hosts entry for location %q", r.cfg.Location)
	}

	result := &ReconcileResult{}

	// Collect all known VMIDs/IPs to avoid collisions
	usedVMIDs := make(map[int]bool)
	usedIPs := make(map[string]bool)
	for _, vm := range services.VMs {
		if vm.VMID > 0 {
			usedVMIDs[vm.VMID] = true
		}
		if vm.StaticIP != "" {
			usedIPs[vm.StaticIP] = true
		}
	}

	// Assign VMID and static IP to any new VMs
	for vmName, vmCfg := range localVMs {
		if vmCfg.VMID == 0 {
			newID := nextAvailableVMID(usedVMIDs, r.cfg.VMIDStart, r.cfg.VMIDEnd)
			vmCfg.VMID = newID
			usedVMIDs[newID] = true
			r.logger.Info("assigned VMID", "vm", vmName, "vmid", newID)
			result.NewVMs = append(result.NewVMs, vmName)
		}

		if vmCfg.StaticIP == "" {
			ip, err := nextAvailableIP(usedIPs, pxHost, r.cfg.IPRangeStart, r.cfg.IPRangeEnd)
			if err != nil {
				r.logger.Error("failed to assign IP", "vm", vmName, "error", err)
				continue
			}
			vmCfg.StaticIP = ip
			usedIPs[ip] = true
			r.logger.Info("assigned static IP", "vm", vmName, "ip", ip)
			if !contains(result.NewVMs, vmName) {
				result.NewVMs = append(result.NewVMs, vmName)
			}
		}
	}

	// Generate tfvars and run tofu
	if err := r.tofu.GenerateVars(services, r.cfg.Location, r.cfg.Proxmox); err != nil {
		return result, fmt.Errorf("generate tfvars: %w", err)
	}

	if err := r.tofu.Init(ctx); err != nil {
		return result, fmt.Errorf("tofu init: %w", err)
	}

	if err := r.adoptExistingVMs(ctx, localVMs, pxHost.Node); err != nil {
		return result, fmt.Errorf("adopt existing VMs: %w", err)
	}

	applied, err := r.tofu.PlanAndApply(ctx)
	if err != nil {
		return result, fmt.Errorf("tofu: %w", err)
	}
	result.TofuApplied = applied
	if !applied {
		r.logger.Info("tofu: no changes needed")
	}

	// Verify all VMs are reachable at expected IPs; repair if not
	r.repairVMNetworking(localVMs)

	return result, nil
}

// repairVMNetworking checks each VM is reachable at its expected static IP.
// If not, it uses the Proxmox guest agent (qm guest exec) to force cloud-init
// to re-apply networking config and reboots the VM.
func (r *Reconciler) repairVMNetworking(vms map[string]*VMConfig) {
	for vmName, vm := range vms {
		if vm.StaticIP == "" || vm.VMID == 0 {
			continue
		}

		// Quick TCP check on port 22
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:22", vm.StaticIP), 5*time.Second)
		if err == nil {
			_ = conn.Close()
			continue
		}

		r.logger.Warn("VM unreachable at expected IP, attempting cloud-init repair",
			"vm", vmName, "expected_ip", vm.StaticIP, "vmid", vm.VMID)

		vmid := fmt.Sprintf("%d", vm.VMID)

		// Use qm guest exec to clean cloud-init state so it re-runs on reboot
		cmd := exec.Command("qm", "guest", "exec", vmid, "--", "cloud-init", "clean")
		if out, err := cmd.CombinedOutput(); err != nil {
			r.logger.Error("cloud-init clean failed", "vm", vmName, "error", err, "output", string(out))
			continue
		}

		// Reboot via qm
		cmd = exec.Command("qm", "reboot", vmid)
		if out, err := cmd.CombinedOutput(); err != nil {
			r.logger.Error("VM reboot failed", "vm", vmName, "error", err, "output", string(out))
			continue
		}

		r.logger.Info("rebooted VM for cloud-init repair, waiting for IP", "vm", vmName)

		// Wait for VM to come back at the correct IP
		if r.waitForIP(vm.StaticIP, vm.VMID, 120*time.Second) {
			r.logger.Info("VM recovered with correct IP", "vm", vmName, "ip", vm.StaticIP)
		} else {
			r.logger.Error("VM did not recover expected IP after reboot", "vm", vmName, "expected_ip", vm.StaticIP)
		}
	}
}

// guestAgentInterfaces is the structure returned by qm agent network-get-interfaces.
type guestAgentInterfaces []struct {
	Name        string `json:"name"`
	IPAddresses []struct {
		IPAddress string `json:"ip-address"`
		Type      string `json:"ip-address-type"`
	} `json:"ip-addresses"`
}

// waitForIP polls until the VM is reachable at the expected IP or timeout.
func (r *Reconciler) waitForIP(expectedIP string, vmid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	vmidStr := fmt.Sprintf("%d", vmid)

	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Second)

		// Check via guest agent what IP the VM has
		cmd := exec.Command("qm", "agent", vmidStr, "network-get-interfaces")
		out, err := cmd.Output()
		if err != nil {
			continue // VM may still be booting
		}

		var ifaces guestAgentInterfaces
		if err := json.Unmarshal(out, &ifaces); err != nil {
			continue
		}

		for _, iface := range ifaces {
			if iface.Name == "lo" {
				continue
			}
			for _, ip := range iface.IPAddresses {
				if ip.Type == "ipv4" && ip.IPAddress == expectedIP {
					return true
				}
			}
		}
	}
	return false
}

// adoptExistingVMs imports VMs that exist on this Proxmox node but are missing
// from tofu state, so tofu manages them instead of trying to create them again.
func (r *Reconciler) adoptExistingVMs(ctx context.Context, vms map[string]*VMConfig, node string) error {
	stateAddrs, err := r.tofu.StateList(ctx)
	if err != nil {
		return err
	}
	onProxmox, err := proxmoxVMs(ctx, node)
	if err != nil {
		return err
	}
	desired := make(map[string]int, len(vms))
	for name, vm := range vms {
		desired[name] = vm.VMID
	}
	adoptions, err := vmsToAdopt(desired, stateAddrs, onProxmox)
	if err != nil {
		return err
	}
	for _, a := range adoptions {
		r.logger.Info("adopting existing VM into tofu state", "vm", a.Name, "vmid", a.VMID)
		if err := r.tofu.Import(ctx, vmAddress(a.Name), fmt.Sprintf("%s/%d", node, a.VMID)); err != nil {
			return err
		}
	}
	return nil
}

// proxmoxVMs lists the VMs on a node of the local Proxmox host (vmid → name).
func proxmoxVMs(ctx context.Context, node string) (map[int]string, error) {
	out, err := exec.CommandContext(ctx, "pvesh", "get", fmt.Sprintf("/nodes/%s/qemu", node), "--output-format", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("list Proxmox VMs: %w", err)
	}
	var vms []struct {
		VMID int    `json:"vmid"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(out, &vms); err != nil {
		return nil, fmt.Errorf("parse Proxmox VM list: %w", err)
	}
	byID := make(map[int]string, len(vms))
	for _, vm := range vms {
		byID[vm.VMID] = vm.Name
	}
	return byID, nil
}

// vmAddress is the tofu resource address of a VM in the proxmox module.
func vmAddress(name string) string {
	return fmt.Sprintf("proxmox_virtual_environment_vm.vm[%q]", name)
}

// adoption is an existing Proxmox VM to import into tofu state.
type adoption struct {
	Name string
	VMID int
}

// vmsToAdopt returns the desired VMs (name → vmid) that tofu state does not
// track but that already exist on Proxmox (vmid → name) under the same name.
// Untracked VMs absent from Proxmox are left for tofu to create. A vmid held
// by a differently named VM is an error: importing it would hand an unrelated
// VM to proxops.
func vmsToAdopt(desired map[string]int, stateAddrs []string, onProxmox map[int]string) ([]adoption, error) {
	tracked := make(map[string]bool, len(stateAddrs))
	for _, addr := range stateAddrs {
		tracked[addr] = true
	}
	var out []adoption
	for name, vmid := range desired {
		if tracked[vmAddress(name)] {
			continue
		}
		existing, ok := onProxmox[vmid]
		if !ok {
			continue
		}
		if existing != name {
			return nil, fmt.Errorf("VM %q wants vmid %d, which Proxmox already uses for VM %q", name, vmid, existing)
		}
		out = append(out, adoption{Name: name, VMID: vmid})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// nextAvailableVMID finds the lowest unused VMID in the given range.
func nextAvailableVMID(used map[int]bool, start, end int) int {
	for id := start; id <= end; id++ {
		if !used[id] {
			return id
		}
	}
	return 0
}

// nextAvailableIP finds the next unused IP in the subnet within the given range.
func nextAvailableIP(usedIPs map[string]bool, pxHost ProxmoxHostConfig, rangeStart, rangeEnd int) (string, error) {
	gw := pxHost.Gateway
	prefix := gw
	for i := len(gw) - 1; i >= 0; i-- {
		if gw[i] == '.' {
			prefix = gw[:i+1]
			break
		}
	}

	for i := rangeStart; i <= rangeEnd; i++ {
		ip := fmt.Sprintf("%s%d", prefix, i)
		if !usedIPs[ip] {
			return ip, nil
		}
	}
	return "", fmt.Errorf("no available IPs in subnet %s%d-%d", prefix, rangeStart, rangeEnd)
}

func contains(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}
