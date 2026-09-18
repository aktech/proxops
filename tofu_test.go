package main

import (
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
