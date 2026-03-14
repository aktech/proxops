package main

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the provisioner config file (e.g. /opt/proxops/config.yml).
type Config struct {
	Location     string        `yaml:"location"`
	RepoURL      string        `yaml:"repo_url"`
	PollInterval time.Duration `yaml:"poll_interval"`
	Proxmox      ProxmoxConfig `yaml:"proxmox"`
	SSHKeyPath   string        `yaml:"ssh_private_key"`
	GitToken     string        `yaml:"git_access_token"`
	RepoDir      string        `yaml:"repo_dir"`

	// VM user and paths
	DefaultUser string `yaml:"default_user"`
	DataDir     string `yaml:"data_dir"`

	// Doco-CD settings
	DocoCDImage    string `yaml:"doco_cd_image"`
	GitReference   string `yaml:"git_reference"`
	DocoCDInterval int    `yaml:"doco_cd_poll_interval"`
	Timezone       string `yaml:"timezone"`

	// Git committer identity
	CommitterEmail string `yaml:"committer_email"`
	CommitterName  string `yaml:"committer_name"`

	// Resource allocation ranges
	VMIDStart    int `yaml:"vmid_start"`
	VMIDEnd      int `yaml:"vmid_end"`
	IPRangeStart int `yaml:"ip_range_start"`
	IPRangeEnd   int `yaml:"ip_range_end"`

	// Self-update
	AutoUpdate     bool          `yaml:"auto_update"`
	UpdateRepo     string        `yaml:"update_repo"`
	UpdateInterval time.Duration `yaml:"update_interval"`

	// Web dashboard
	WebPort int `yaml:"web_port"`
}

type ProxmoxConfig struct {
	APIURL      string `yaml:"api_url"`
	TokenID     string `yaml:"token_id"`
	TokenSecret string `yaml:"token_secret"`
	Node        string `yaml:"node"`
}

// LoadConfig reads and parses the provisioner config file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{
		PollInterval:   60 * time.Second,
		RepoDir:        "/opt/proxops/repo",
		DefaultUser:    "ubuntu",
		DataDir:        "/opt/proxops",
		DocoCDImage:    "ghcr.io/kimdre/doco-cd:0.67.1",
		GitReference:   "refs/heads/main",
		DocoCDInterval: 120,
		Timezone:       "UTC",
		CommitterEmail: "proxops@localhost",
		CommitterName:  "proxops",
		VMIDStart:      100,
		VMIDEnd:        999,
		IPRangeStart:   220,
		IPRangeEnd:     254,
		AutoUpdate:     false,
		UpdateRepo:     "aktech/proxops",
		UpdateInterval: 1 * time.Hour,
		WebPort:        9100,
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Proxmox.APIURL == "" {
		cfg.Proxmox.APIURL = "https://localhost:8006"
	}
	if cfg.Location == "" {
		return nil, fmt.Errorf("config: location is required")
	}
	if cfg.RepoURL == "" {
		return nil, fmt.Errorf("config: repo_url is required")
	}
	if cfg.VMIDStart >= cfg.VMIDEnd {
		return nil, fmt.Errorf("config: vmid_start (%d) must be less than vmid_end (%d)", cfg.VMIDStart, cfg.VMIDEnd)
	}
	if cfg.IPRangeStart >= cfg.IPRangeEnd || cfg.IPRangeStart < 1 || cfg.IPRangeEnd > 254 {
		return nil, fmt.Errorf("config: ip_range_start (%d) and ip_range_end (%d) must be within 1-254", cfg.IPRangeStart, cfg.IPRangeEnd)
	}
	return cfg, nil
}
