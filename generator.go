package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// Generator creates doco-cd configs and traefik routing rules from services.yml.
type Generator struct {
	cfg    *Config
	logger *slog.Logger
}

// NewGenerator creates a config generator.
func NewGenerator(cfg *Config, logger *slog.Logger) *Generator {
	return &Generator{cfg: cfg, logger: logger}
}

// GenerateAll generates all configs for a location and returns true if any files changed.
func (g *Generator) GenerateAll(services *ServicesFile, location string) (bool, error) {
	changed := false

	// Generate doco-cd configs per VM
	for vmName, vm := range services.VMsForLocation(location) {
		c, err := g.generateDocoCDConfigs(vmName, vm, services)
		if err != nil {
			return changed, fmt.Errorf("doco-cd configs for %s: %w", vmName, err)
		}
		if c {
			changed = true
		}
	}

	// Generate traefik routing rules
	c, err := g.generateTraefikRoutes(services, location)
	if err != nil {
		return changed, fmt.Errorf("traefik routes: %w", err)
	}
	if c {
		changed = true
	}

	return changed, nil
}

// generateDocoCDConfigs writes docker-compose.doco-cd.yml and doco-cd-poll.yml for a VM.
func (g *Generator) generateDocoCDConfigs(vmName string, vm *VMConfig, services *ServicesFile) (bool, error) {
	if len(vm.Services) == 0 {
		// VM runs no docker services (e.g. a bare SSH/systemd VM) — nothing
		// for doco-cd to manage here.
		return false, nil
	}
	primary := vm.PrimaryService()
	serviceDir := filepath.Join(g.cfg.RepoDir, primary.ServiceDir)

	if _, err := os.Stat(serviceDir); os.IsNotExist(err) {
		g.logger.Warn("service directory does not exist, skipping", "vm", vmName, "dir", primary.ServiceDir)
		return false, nil
	}

	changed := false

	// docker-compose.doco-cd.yml
	composePath := filepath.Join(serviceDir, "docker-compose.doco-cd.yml")
	composeContent := g.generateDocoCDCompose()
	if c, err := writeIfChanged(composePath, composeContent); err != nil {
		return false, err
	} else if c {
		g.logger.Info("wrote docker-compose.doco-cd.yml", "vm", vmName, "dir", primary.ServiceDir)
		changed = true
	}

	// doco-cd-poll.yml
	pollPath := filepath.Join(serviceDir, "doco-cd-poll.yml")
	pollContent, err := g.generateDocoCDPoll(services.AllServices(vm))
	if err != nil {
		return false, err
	}
	if c, err := writeIfChanged(pollPath, pollContent); err != nil {
		return false, err
	} else if c {
		g.logger.Info("wrote doco-cd-poll.yml", "vm", vmName, "dir", primary.ServiceDir)
		changed = true
	}

	return changed, nil
}

func (g *Generator) generateDocoCDCompose() string {
	envFilePath := filepath.Join(g.cfg.DataDir, "doco-cd.env")
	return fmt.Sprintf(`services:
  doco-cd:
    container_name: doco-cd
    image: %s
    restart: unless-stopped
    env_file:
      - path: %s
        required: false
    environment:
      - POLL_CONFIG_FILE=/poll-config.yml
      - TZ=%s
      - LOG_LEVEL=info
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - doco-cd-data:/data
      - ./doco-cd-poll.yml:/poll-config.yml:ro
      - %s:/env/doco-cd.env:ro

volumes:
  doco-cd-data:
`, g.cfg.DocoCDImage, envFilePath, g.cfg.Timezone, envFilePath)
}

// pollDeployment mirrors the YAML structure of doco-cd-poll.yml.
type pollDeployment struct {
	Name           string   `yaml:"name"`
	WorkingDir     string   `yaml:"working_dir"`
	ComposeFiles   []string `yaml:"compose_files"`
	EnvFiles       []string `yaml:"env_files"`
	ForceRecreate  bool     `yaml:"force_recreate"`
	RemoveOrphans  bool     `yaml:"remove_orphans"`
	ForceImagePull bool     `yaml:"force_image_pull"`
}

type pollConfig struct {
	URL         string           `yaml:"url"`
	Reference   string           `yaml:"reference"`
	Interval    int              `yaml:"interval"`
	Deployments []pollDeployment `yaml:"deployments"`
}

func (g *Generator) generateDocoCDPoll(services []VMService) (string, error) {
	deployments := make([]pollDeployment, 0, len(services))
	for _, svc := range services {
		deployments = append(deployments, pollDeployment{
			Name:           svc.ProjectName,
			WorkingDir:     svc.ServiceDir + "/",
			ComposeFiles:   []string{svc.ComposeFile},
			EnvFiles:       []string{"/env/doco-cd.env"},
			ForceRecreate:  false,
			RemoveOrphans:  true,
			ForceImagePull: false,
		})
	}
	config := []pollConfig{{
		URL:         g.cfg.RepoURL,
		Reference:   g.cfg.GitReference,
		Interval:    g.cfg.DocoCDInterval,
		Deployments: deployments,
	}}
	data, err := yaml.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("marshal poll config: %w", err)
	}
	return string(data), nil
}

// generateTraefikRoutes writes the traefik routing rules file for a location.
func (g *Generator) generateTraefikRoutes(services *ServicesFile, location string) (bool, error) {
	rulesPath := filepath.Join(g.cfg.RepoDir, "traefik", fmt.Sprintf("routing-%s", location), "rules.yml")

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(rulesPath), 0755); err != nil {
		return false, fmt.Errorf("create routing dir: %w", err)
	}

	domain := services.Domain
	locationDomain := fmt.Sprintf("%s.%s", location, domain)

	routers := make(map[string]interface{})
	svcDefs := make(map[string]interface{})

	// Collect all route names for sorted iteration
	type routeEntry struct {
		name      string
		subdomain string
		url       string
		auth      bool
	}
	var allRoutes []routeEntry

	// VM routes
	for _, vm := range services.VMsForLocation(location) {
		for _, route := range vm.Routes {
			allRoutes = append(allRoutes, routeEntry{
				name:      route.Name,
				subdomain: route.Subdomain,
				url:       route.BackendURL(vm.StaticIP),
				auth:      route.Auth,
			})
		}
	}

	// Device routes
	if devices, ok := services.Devices[location]; ok {
		for _, dev := range devices {
			allRoutes = append(allRoutes, routeEntry{
				name:      dev.Name,
				subdomain: dev.Subdomain,
				url:       dev.URL,
				auth:      dev.Auth,
			})
		}
	}

	// Sort for deterministic output
	sort.Slice(allRoutes, func(i, j int) bool {
		return allRoutes[i].name < allRoutes[j].name
	})

	for _, route := range allRoutes {
		fqdn := fmt.Sprintf("%s.%s", route.subdomain, locationDomain)

		router := map[string]interface{}{
			"rule":    fmt.Sprintf("Host(`%s`)", fqdn),
			"service": route.name,
			"tls": map[string]string{
				"certresolver": "letsencrypt",
			},
		}
		if route.auth {
			router["middlewares"] = []string{"authentication"}
		}
		routers[route.name] = router

		svcDefs[route.name] = map[string]interface{}{
			"loadBalancer": map[string]interface{}{
				"servers": []map[string]string{
					{"url": route.url},
				},
			},
		}
	}

	rules := map[string]interface{}{
		"http": map[string]interface{}{
			"routers":  routers,
			"services": svcDefs,
		},
	}

	data, err := yaml.Marshal(rules)
	if err != nil {
		return false, fmt.Errorf("marshal traefik rules: %w", err)
	}

	content := string(data)

	// Add header comment
	header := fmt.Sprintf("# Auto-generated by proxops for %s\n# Do not edit manually — changes will be overwritten.\n\n", location)
	content = header + content

	return writeIfChanged(rulesPath, content)
}

// writeIfChanged writes content to a file only if it differs from current content.
// Returns true if the file was written.
func writeIfChanged(path, content string) (bool, error) {
	existing, err := os.ReadFile(path)
	if err == nil && string(existing) == content {
		return false, nil
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false, fmt.Errorf("create directory %s: %w", dir, err)
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

// ensureSubdomainUniqueness checks that no two routes use the same subdomain.
func ensureSubdomainUniqueness(services *ServicesFile, location string) error {
	seen := make(map[string]string) // subdomain → source name
	for vmName, vm := range services.VMsForLocation(location) {
		for _, route := range vm.Routes {
			if prev, ok := seen[route.Subdomain]; ok {
				return fmt.Errorf("duplicate subdomain %q: used by both %s and %s", route.Subdomain, prev, vmName)
			}
			seen[route.Subdomain] = vmName
		}
	}
	if devices, ok := services.Devices[location]; ok {
		for _, dev := range devices {
			if prev, ok := seen[dev.Subdomain]; ok {
				return fmt.Errorf("duplicate subdomain %q: used by both %s and device %s", dev.Subdomain, prev, dev.Name)
			}
			seen[dev.Subdomain] = "device:" + dev.Name
		}
	}
	return nil
}
