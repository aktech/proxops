package main

import (
	"reflect"
	"strings"
	"testing"
)

// A VM declared in services.yml that already exists on Proxmox under the same
// vmid and name, but is missing from tofu state (state lost, or the VM
// predates proxops), must be imported, not re-created.
func TestVMsToAdoptImportsExistingUntrackedVMs(t *testing.T) {
	desired := map[string]int{"Frigate": 102, "Wireguard": 103, "Traefik": 104, "NewVM": 110}
	state := []string{`proxmox_virtual_environment_vm.vm["Wireguard"]`}
	onProxmox := map[int]string{101: "haos", 102: "Frigate", 103: "Wireguard", 104: "Traefik"}

	got, err := vmsToAdopt(desired, state, onProxmox)
	if err != nil {
		t.Fatalf("vmsToAdopt: %v", err)
	}
	want := []adoption{{Name: "Frigate", VMID: 102}, {Name: "Traefik", VMID: 104}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("vmsToAdopt = %v, want %v (tracked VMs and VMs not on Proxmox are left to plan)", got, want)
	}
}

// If the vmid is taken by a differently named VM, importing would put an
// unrelated VM under proxops control; stop instead.
func TestVMsToAdoptRefusesVMIDHeldByAnotherVM(t *testing.T) {
	desired := map[string]int{"Traefik": 104}
	onProxmox := map[int]string{104: "scratch"}

	_, err := vmsToAdopt(desired, nil, onProxmox)
	if err == nil {
		t.Fatal("expected an error when vmid 104 belongs to a VM named \"scratch\", got nil")
	}
	for _, want := range []string{"Traefik", "104", "scratch"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}
