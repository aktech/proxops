package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// proxops applies plans unattended, so any plan that would delete a VM
// (outright or as part of a replace) must be caught before apply.
func TestDestructiveChangesListsDeletesAndReplaces(t *testing.T) {
	plan := []byte(`{
	  "format_version": "1.2",
	  "resource_changes": [
	    {"address": "proxmox_virtual_environment_vm.vm[\"Updated\"]", "change": {"actions": ["update"]}},
	    {"address": "proxmox_virtual_environment_vm.vm[\"Replaced\"]", "change": {"actions": ["delete", "create"]}},
	    {"address": "proxmox_virtual_environment_vm.vm[\"Created\"]", "change": {"actions": ["create"]}},
	    {"address": "proxmox_virtual_environment_vm.vm[\"Removed\"]", "change": {"actions": ["delete"]}},
	    {"address": "proxmox_virtual_environment_vm.vm[\"Same\"]", "change": {"actions": ["no-op"]}}
	  ]
	}`)

	got, err := destructiveChanges(plan)
	if err != nil {
		t.Fatalf("destructiveChanges: %v", err)
	}
	want := []string{
		`proxmox_virtual_environment_vm.vm["Replaced"]`,
		`proxmox_virtual_environment_vm.vm["Removed"]`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("destructiveChanges = %v, want %v", got, want)
	}
}

func TestDestructiveChangesRejectsUnreadablePlan(t *testing.T) {
	if _, err := destructiveChanges([]byte("not json")); err == nil {
		t.Error("expected an error for an unreadable plan, got nil (an unreadable plan must not be treated as safe)")
	}
}

// A VM can be cloned from its own template; VMs that don't say keep the
// default. London's default VMID is a running VM, not a template, so a
// new VM there must be able to point at the real template.
func TestGenerateVarsPerVMTemplate(t *testing.T) {
	dir := t.TempDir()
	services := &ServicesFile{
		ProxmoxHosts: map[string]ProxmoxHostConfig{"london": {APIHost: "10.0.0.1", Node: "pve", Gateway: "10.0.0.254", Netmask: 24, Datastore: "local-lvm"}},
		VMDefaults:   VMDefaults{TemplateVMID: 100, Cores: 2, MemoryMB: 2048, DiskGB: 32, CPUType: "host", AnsibleUser: "ubuntu"},
		VMs: map[string]*VMConfig{
			"Old": {Location: "london", VMID: 105, StaticIP: "10.0.0.5"},
			"New": {Location: "london", VMID: 112, StaticIP: "10.0.0.12", TemplateVMID: 101},
		},
	}
	if err := NewTofuRunner(dir, slog.New(slog.NewTextHandler(io.Discard, nil))).GenerateVars(services, "london", ProxmoxConfig{}); err != nil {
		t.Fatalf("GenerateVars: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "terraform.tfvars.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vars TofuVars
	if err := json.Unmarshal(raw, &vars); err != nil {
		t.Fatal(err)
	}
	if got := vars.VMs["New"].TemplateVMID; got != 101 {
		t.Errorf("New template = %d, want 101", got)
	}
	if got := vars.VMs["Old"].TemplateVMID; got != 100 {
		t.Errorf("Old template = %d, want the default 100", got)
	}
}
