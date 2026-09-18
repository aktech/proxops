package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Two locations each run a VM whose primary service lives in the same
// directory (e.g. a per-site traefik). Each location's proxops generates its
// own doco-cd files; neither may overwrite the other's, or the two proxops
// instances rewrite and commit the same file forever.
func TestGenerateAllKeepsDocoCDFilesPerVM(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "traefik"), 0755); err != nil {
		t.Fatal(err)
	}
	sf := &ServicesFile{
		Domain: "example.com",
		VMs: map[string]*VMConfig{
			"Traefik": {Location: "site1", VMID: 104, StaticIP: "10.0.0.220", Services: []VMService{
				{ServiceDir: "traefik", ComposeFile: "docker-compose.site1.yml", ProjectName: "traefik-site1", Primary: true},
			}},
			"Traefik-Site2": {Location: "site2", VMID: 105, StaticIP: "10.0.1.220", Services: []VMService{
				{ServiceDir: "traefik", ComposeFile: "docker-compose.site2.yml", ProjectName: "traefik-site2", Primary: true},
			}},
		},
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	site1 := NewGenerator(&Config{RepoDir: repo, DataDir: "/opt/proxops", Timezone: "UTC"}, logger)
	site2 := NewGenerator(&Config{RepoDir: repo, DataDir: "/opt/proxops", Timezone: "Europe/London"}, logger)

	if _, err := site1.GenerateAll(sf, "site1"); err != nil {
		t.Fatalf("site1 GenerateAll: %v", err)
	}
	if _, err := site2.GenerateAll(sf, "site2"); err != nil {
		t.Fatalf("site2 GenerateAll: %v", err)
	}

	changed, err := site1.GenerateAll(sf, "site1")
	if err != nil {
		t.Fatalf("site1 GenerateAll (again): %v", err)
	}
	if changed {
		t.Error("site1 regenerated files after site2 ran: the two locations overwrite each other")
	}

	for vm, want := range map[string]string{"Traefik": "traefik-site1", "Traefik-Site2": "traefik-site2"} {
		poll, err := os.ReadFile(filepath.Join(repo, "doco-cd", vm, "doco-cd-poll.yml"))
		if err != nil {
			t.Fatalf("%s poll config: %v", vm, err)
		}
		if !strings.Contains(string(poll), want) {
			t.Errorf("%s poll config missing its own stack %q:\n%s", vm, want, poll)
		}
		if _, err := os.Stat(filepath.Join(repo, "doco-cd", vm, "docker-compose.yml")); err != nil {
			t.Errorf("%s doco-cd compose: %v", vm, err)
		}
	}
}
