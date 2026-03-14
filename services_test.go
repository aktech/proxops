package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadServices(t *testing.T) {
	sf, err := LoadServices(filepath.Join("testdata", "services.yml"))
	if err != nil {
		t.Fatalf("LoadServices: %v", err)
	}

	// Verify top-level fields
	if sf.Domain != "example.com" {
		t.Errorf("domain = %q, want %q", sf.Domain, "example.com")
	}

	// Verify proxmox_hosts
	if _, ok := sf.ProxmoxHosts["site1"]; !ok {
		t.Error("missing proxmox_hosts.site1")
	}
	site1 := sf.ProxmoxHosts["site1"]
	if site1.APIHost != "10.0.0.1" {
		t.Errorf("site1.api_host = %q, want %q", site1.APIHost, "10.0.0.1")
	}
	if site1.Gateway != "10.0.0.1" {
		t.Errorf("site1.gateway = %q, want %q", site1.Gateway, "10.0.0.1")
	}

	// Verify vm_defaults
	if sf.VMDefaults.TemplateVMID != 100 {
		t.Errorf("vm_defaults.template_vmid = %d, want 100", sf.VMDefaults.TemplateVMID)
	}
	if sf.VMDefaults.Cores != 2 {
		t.Errorf("vm_defaults.cores = %d, want 2", sf.VMDefaults.Cores)
	}

	// Verify VMs
	if len(sf.VMs) != 3 {
		t.Errorf("len(vms) = %d, want 3", len(sf.VMs))
	}

	traefik, ok := sf.VMs["Traefik"]
	if !ok {
		t.Fatal("missing VM Traefik")
	}
	if traefik.VMID != 104 {
		t.Errorf("Traefik.vmid = %d, want 104", traefik.VMID)
	}
	if traefik.StaticIP != "10.0.0.220" {
		t.Errorf("Traefik.static_ip = %q, want %q", traefik.StaticIP, "10.0.0.220")
	}
	if traefik.Location != "site1" {
		t.Errorf("Traefik.location = %q, want %q", traefik.Location, "site1")
	}
	if len(traefik.Routes) != 1 {
		t.Errorf("Traefik.routes len = %d, want 1", len(traefik.Routes))
	}
	if traefik.Routes[0].URL != "http://127.0.0.1:8080/" {
		t.Errorf("Traefik route URL = %q, want %q", traefik.Routes[0].URL, "http://127.0.0.1:8080/")
	}

	// Verify AppServer route backend URL generation
	app := sf.VMs["AppServer"]
	if app.Routes[0].BackendURL(app.StaticIP) != "https://10.0.0.222:8080/" {
		t.Errorf("AppServer backend URL = %q, want %q", app.Routes[0].BackendURL(app.StaticIP), "https://10.0.0.222:8080/")
	}

	// Verify devices
	if len(sf.Devices["site1"]) != 3 {
		t.Errorf("len(devices.site1) = %d, want 3", len(sf.Devices["site1"]))
	}

	// Verify VMsForLocation filter
	site1VMs := sf.VMsForLocation("site1")
	if len(site1VMs) != 3 {
		t.Errorf("VMsForLocation(site1) = %d, want 3", len(site1VMs))
	}
	otherVMs := sf.VMsForLocation("other")
	if len(otherVMs) != 0 {
		t.Errorf("VMsForLocation(other) = %d, want 0", len(otherVMs))
	}

	// Verify EffectiveCores (AppServer has cores: 4 override)
	if app.EffectiveCores(sf.VMDefaults) != 4 {
		t.Errorf("AppServer.EffectiveCores = %d, want 4", app.EffectiveCores(sf.VMDefaults))
	}

	// Verify PrimaryService
	primary := traefik.PrimaryService()
	if primary.ServiceDir != "traefik" {
		t.Errorf("Traefik.PrimaryService().ServiceDir = %q, want %q", primary.ServiceDir, "traefik")
	}

	// Verify GlobalServices
	if len(sf.GlobalServices) != 1 {
		t.Fatalf("len(global_services) = %d, want 1", len(sf.GlobalServices))
	}
	if sf.GlobalServices[0].ProjectName != "grafana-alloy" {
		t.Errorf("global_services[0].project_name = %q, want %q", sf.GlobalServices[0].ProjectName, "grafana-alloy")
	}

	// Verify AllServices merges global into VM services
	allTraefik := sf.AllServices(traefik)
	if len(allTraefik) != 2 {
		t.Errorf("AllServices(Traefik) = %d, want 2", len(allTraefik))
	}
	found := false
	for _, svc := range allTraefik {
		if svc.ProjectName == "grafana-alloy" {
			found = true
		}
	}
	if !found {
		t.Error("AllServices(Traefik) missing grafana-alloy from global_services")
	}
}

func TestGenerateDocoCDPoll(t *testing.T) {
	cfg := &Config{
		RepoURL:        "https://github.com/example/repo.git",
		GitReference:   "refs/heads/main",
		DocoCDInterval: 120,
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	gen := NewGenerator(cfg, logger)

	services := []VMService{
		{ServiceDir: "traefik", ComposeFile: "docker-compose.site1.yml", ProjectName: "traefik-site1", Primary: true},
	}
	output, err := gen.generateDocoCDPoll(services)
	if err != nil {
		t.Fatalf("generateDocoCDPoll: %v", err)
	}

	// Verify it contains expected fields
	if !strings.Contains(output, "traefik-site1") {
		t.Error("poll config missing project name")
	}
	if !strings.Contains(output, "traefik/") {
		t.Error("poll config missing working_dir")
	}
	if !strings.Contains(output, "docker-compose.site1.yml") {
		t.Error("poll config missing compose file")
	}
	if !strings.Contains(output, "refs/heads/main") {
		t.Error("poll config missing reference")
	}
	if !strings.Contains(output, "https://github.com/example/repo.git") {
		t.Error("poll config missing repo URL")
	}
}

func TestGenerateTraefikRoutes(t *testing.T) {
	sf, err := LoadServices(filepath.Join("testdata", "services.yml"))
	if err != nil {
		t.Fatalf("LoadServices: %v", err)
	}

	tmpDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cfg := &Config{
		RepoDir:      tmpDir,
		PollInterval: 60 * time.Second,
	}
	gen := NewGenerator(cfg, logger)

	// Create the routing dir structure
	routingDir := filepath.Join(tmpDir, "traefik", "routing-site1")
	_ = os.MkdirAll(routingDir, 0755)

	changed, err := gen.generateTraefikRoutes(sf, "site1")
	if err != nil {
		t.Fatalf("generateTraefikRoutes: %v", err)
	}
	if !changed {
		t.Error("expected changed=true for new file")
	}

	// Read and verify the output
	rulesPath := filepath.Join(routingDir, "rules.yml")
	data, err := os.ReadFile(rulesPath)
	if err != nil {
		t.Fatalf("read rules: %v", err)
	}
	content := string(data)

	// Verify VM routes
	if !strings.Contains(content, "myapp.site1.example.com") {
		t.Error("missing myapp.site1.example.com")
	}
	if !strings.Contains(content, "traefik.site1.example.com") {
		t.Error("missing traefik.site1.example.com")
	}
	if !strings.Contains(content, "vpn.site1.example.com") {
		t.Error("missing vpn.site1.example.com")
	}

	// Verify device routes
	if !strings.Contains(content, "proxmox.site1.example.com") {
		t.Error("missing proxmox.site1.example.com (device)")
	}
	if !strings.Contains(content, "ha.site1.example.com") {
		t.Error("missing ha.site1.example.com (device)")
	}

	// Verify backend URLs
	if !strings.Contains(content, "https://10.0.0.222:8080/") {
		t.Error("missing appserver backend URL")
	}
	if !strings.Contains(content, "http://127.0.0.1:8080/") {
		t.Error("missing traefik backend URL")
	}

	// Verify idempotency — second call should return changed=false
	changed2, err := gen.generateTraefikRoutes(sf, "site1")
	if err != nil {
		t.Fatalf("generateTraefikRoutes (2nd): %v", err)
	}
	if changed2 {
		t.Error("expected changed=false on second call (idempotency)")
	}
}

func TestTofuVarsGeneration(t *testing.T) {
	sf, err := LoadServices(filepath.Join("testdata", "services.yml"))
	if err != nil {
		t.Fatalf("LoadServices: %v", err)
	}

	tmpDir := t.TempDir()
	tofuLogger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	gen := NewTofuRunner(tmpDir, tofuLogger)

	proxCfg := ProxmoxConfig{
		TokenID:     "root@pam!test",
		TokenSecret: "test-secret",
		Node:        "pve",
	}

	err = gen.GenerateVars(sf, "site1", proxCfg)
	if err != nil {
		t.Fatalf("GenerateVars: %v", err)
	}

	// Verify file was written
	varsPath := filepath.Join(tmpDir, "terraform.tfvars.json")
	data, err := os.ReadFile(varsPath)
	if err != nil {
		t.Fatalf("read tfvars: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "10.0.0.220") {
		t.Error("missing Traefik static IP in tfvars")
	}
	if !strings.Contains(content, "10.0.0.222") {
		t.Error("missing AppServer static IP in tfvars")
	}
	if !strings.Contains(content, `"proxmox_api_url"`) {
		t.Error("missing proxmox_api_url key")
	}
}
