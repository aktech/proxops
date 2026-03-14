package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// ServicesFile represents the top-level ansible/services.yml.
type ServicesFile struct {
	Domain           string                       `yaml:"domain"`
	ProxmoxHosts     map[string]ProxmoxHostConfig `yaml:"proxmox_hosts"`
	VMDefaults       VMDefaults                   `yaml:"vm_defaults"`
	GlobalServices   []VMService                  `yaml:"global_services,omitempty"`
	VMs              map[string]*VMConfig         `yaml:"vms"`
	Devices          map[string][]DeviceRoute     `yaml:"devices"`
	ProxOpsVersion string                       `yaml:"proxops_version,omitempty"`
}

// AllServices returns a VM's services with global services appended.
func (sf *ServicesFile) AllServices(vm *VMConfig) []VMService {
	seen := make(map[string]bool)
	for _, svc := range vm.Services {
		seen[svc.ProjectName] = true
	}
	result := make([]VMService, len(vm.Services))
	copy(result, vm.Services)
	for _, gs := range sf.GlobalServices {
		if !seen[gs.ProjectName] {
			result = append(result, gs)
		}
	}
	return result
}

type ProxmoxHostConfig struct {
	APIHost    string   `yaml:"api_host"`
	APITokenID string   `yaml:"api_token_id"`
	Node       string   `yaml:"node"`
	Gateway    string   `yaml:"gateway"`
	DNSServers []string `yaml:"dns_servers"`
	Netmask    int      `yaml:"netmask"`
	Datastore  string   `yaml:"datastore"`
}

type VMDefaults struct {
	TemplateVMID int    `yaml:"template_vmid"`
	Cores        int    `yaml:"cores"`
	MemoryMB     int    `yaml:"memory_mb"`
	DiskGB       int    `yaml:"disk_gb"`
	AnsibleUser  string `yaml:"ansible_user"`
}

type VMConfig struct {
	Location  string      `yaml:"location"`
	VMID      int         `yaml:"vmid,omitempty"`
	StaticIP  string      `yaml:"static_ip,omitempty"`
	Cores     int         `yaml:"cores,omitempty"`
	MemoryMB  int         `yaml:"memory_mb,omitempty"`
	DiskGB    int         `yaml:"disk_gb,omitempty"`
	Services  []VMService `yaml:"services"`
	Routes    []VMRoute   `yaml:"routes,omitempty"`
}

type VMService struct {
	ServiceDir  string `yaml:"service_dir"`
	ComposeFile string `yaml:"compose_file"`
	ProjectName string `yaml:"project_name"`
	Primary     bool   `yaml:"primary,omitempty"`
}

type VMRoute struct {
	Name      string `yaml:"name"`
	Subdomain string `yaml:"subdomain"`
	Port      int    `yaml:"port,omitempty"`
	Protocol  string `yaml:"protocol,omitempty"`
	URL       string `yaml:"url,omitempty"`
	Auth      bool   `yaml:"auth,omitempty"`
}

type DeviceRoute struct {
	Name      string `yaml:"name"`
	Subdomain string `yaml:"subdomain"`
	URL       string `yaml:"url"`
	Auth      bool   `yaml:"auth,omitempty"`
}

// LoadServices reads and parses services.yml.
func LoadServices(path string) (*ServicesFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read services.yml: %w", err)
	}
	var sf ServicesFile
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return nil, fmt.Errorf("parse services.yml: %w", err)
	}
	return &sf, nil
}

// SaveServices writes the services file back (e.g. after assigning vmid/static_ip).
func SaveServices(path string, sf *ServicesFile) error {
	data, err := yaml.Marshal(sf)
	if err != nil {
		return fmt.Errorf("marshal services.yml: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write services.yml: %w", err)
	}
	return nil
}

// VMsForLocation returns VMs filtered to a specific location.
func (sf *ServicesFile) VMsForLocation(location string) map[string]*VMConfig {
	result := make(map[string]*VMConfig)
	for name, vm := range sf.VMs {
		if vm.Location == location {
			result[name] = vm
		}
	}
	return result
}

// PrimaryService returns the primary service for a VM (or the first one).
func (vm *VMConfig) PrimaryService() VMService {
	for _, svc := range vm.Services {
		if svc.Primary {
			return svc
		}
	}
	return vm.Services[0]
}

// EffectiveCores returns the VM's cores or the default.
func (vm *VMConfig) EffectiveCores(defaults VMDefaults) int {
	if vm.Cores > 0 {
		return vm.Cores
	}
	return defaults.Cores
}

// EffectiveMemoryMB returns the VM's memory or the default.
func (vm *VMConfig) EffectiveMemoryMB(defaults VMDefaults) int {
	if vm.MemoryMB > 0 {
		return vm.MemoryMB
	}
	return defaults.MemoryMB
}

// EffectiveDiskGB returns the VM's disk or the default.
func (vm *VMConfig) EffectiveDiskGB(defaults VMDefaults) int {
	if vm.DiskGB > 0 {
		return vm.DiskGB
	}
	return defaults.DiskGB
}

// BackendURL builds the backend URL for a route given the VM's static IP.
func (r VMRoute) BackendURL(vmIP string) string {
	if r.URL != "" {
		return r.URL
	}
	proto := r.Protocol
	if proto == "" {
		proto = "http"
	}
	return fmt.Sprintf("%s://%s:%d/", proto, vmIP, r.Port)
}
