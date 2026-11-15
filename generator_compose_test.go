package main

import (
	"log/slog"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The generated doco-cd compose pins the Doco-CD image by digest and scopes
// the global Git token to the repository's host, which Doco-CD 0.130+
// requires before it starts.
func TestDocoCDComposePinsImageAndScopesGitToken(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cfg := &Config{RepoDir: t.TempDir(), DataDir: "/opt/proxops", Timezone: "UTC",
		RepoURL: "https://token@git.example.com/org/homelab.git", DocoCDImage: DefaultDocoCDImage}
	out := NewGenerator(cfg, logger).generateDocoCDCompose()

	var compose struct {
		Services map[string]struct {
			Image       string            `yaml:"image"`
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(out), &compose); err != nil {
		t.Fatalf("generated compose is not YAML: %v\n%s", err, out)
	}
	svc := compose.Services["doco-cd"]
	if !strings.Contains(svc.Image, "ghcr.io/kimdre/doco-cd:") || !strings.Contains(svc.Image, "@sha256:") {
		t.Errorf("image %q is not pinned by digest", svc.Image)
	}
	var scope []struct {
		Domains []string `yaml:"domains"`
	}
	if err := yaml.Unmarshal([]byte(svc.Environment["GIT_AUTH_DOMAINS"]), &scope); err != nil {
		t.Fatalf("GIT_AUTH_DOMAINS is not a YAML list: %v\n%q", err, svc.Environment["GIT_AUTH_DOMAINS"])
	}
	if len(scope) != 1 || len(scope[0].Domains) != 1 || scope[0].Domains[0] != "git.example.com" {
		t.Errorf("GIT_AUTH_DOMAINS = %+v, want the repo host git.example.com only", scope)
	}
	for _, k := range []string{"POLL_CONFIG_FILE", "TZ", "LOG_LEVEL"} {
		if svc.Environment[k] == "" {
			t.Errorf("environment lost %s", k)
		}
	}
}

func TestGitHostFallsBackToGitHub(t *testing.T) {
	for in, want := range map[string]string{
		"https://x@github.com/aktech/homelab.git": "github.com",
		"git@github.com:aktech/homelab.git":       "github.com",
		"":                                        "github.com",
		"not a url":                               "github.com",
	} {
		if got := gitHost(in); got != want {
			t.Errorf("gitHost(%q) = %q, want %q", in, got, want)
		}
	}
}
