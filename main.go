package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

var (
	version = "dev"
	commit  = "none"
)

func main() {
	configPath := flag.String("config", "/opt/proxpilot/config.yml", "path to config file")
	once := flag.Bool("once", false, "run one reconciliation cycle and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("proxpilot %s (%s)\n", version, commit)
		os.Exit(0)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	logger.Info("proxpilot starting",
		"location", cfg.Location,
		"poll_interval", cfg.PollInterval,
		"repo_dir", cfg.RepoDir,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Info("received signal, shutting down", "signal", sig)
		cancel()
	}()

	// Initialize components
	git := NewGitOps(cfg.RepoDir, cfg.RepoURL, cfg.CommitterEmail, cfg.CommitterName, logger)
	tofuDir := filepath.Join(cfg.RepoDir, "tofu", "proxmox")
	tofu := NewTofuRunner(tofuDir, logger)

	bootstrapper, err := NewBootstrapper(cfg.SSHKeyPath, cfg.DefaultUser, cfg.GitToken, cfg.DataDir, logger)
	if err != nil {
		logger.Error("failed to create bootstrapper", "error", err)
		os.Exit(1)
	}

	gen := NewGenerator(cfg, logger)
	reconciler := NewReconciler(tofu, cfg, logger)

	var updater *Updater
	if cfg.AutoUpdate {
		updater = NewUpdater(cfg.UpdateRepo, cfg.UpdateInterval, logger)
	}

	// Ensure repo is cloned
	if err := git.EnsureCloned(); err != nil {
		logger.Error("failed to clone repo", "error", err)
		os.Exit(1)
	}

	if *once {
		if err := runCycle(ctx, cfg, git, reconciler, bootstrapper, gen, logger); err != nil {
			logger.Error("reconciliation failed", "error", err)
			os.Exit(1)
		}
		return
	}

	// Poll loop
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	// Run immediately on start
	if err := runCycle(ctx, cfg, git, reconciler, bootstrapper, gen, logger); err != nil {
		logger.Error("reconciliation failed", "error", err)
	}

	for {
		select {
		case <-ctx.Done():
			logger.Info("shutting down")
			return
		case <-ticker.C:
			// Self-update check (rate-limited internally by UpdateInterval)
			if updater != nil && updater.CheckAndUpdate() {
				logger.Info("binary updated, exiting for systemd restart")
				os.Exit(0)
			}

			if err := runCycle(ctx, cfg, git, reconciler, bootstrapper, gen, logger); err != nil {
				logger.Error("reconciliation failed", "error", err)
			}
		}
	}
}

// runCycle performs one full reconciliation cycle:
// pull → reconcile VMs (tofu) → bootstrap new VMs → generate configs → commit & push.
func runCycle(ctx context.Context, cfg *Config, git *GitOps, reconciler *Reconciler, bootstrapper *Bootstrapper, gen *Generator, logger *slog.Logger) error {
	logger.Info("starting reconciliation cycle")

	// 1. Pull latest
	if err := git.Pull(); err != nil {
		return fmt.Errorf("git pull: %w", err)
	}

	// 2. Load services
	servicesPath := filepath.Join(cfg.RepoDir, "ansible", "services.yml")
	services, err := LoadServices(servicesPath)
	if err != nil {
		return fmt.Errorf("load services: %w", err)
	}

	// 3. Validate
	if err := ensureSubdomainUniqueness(services, cfg.Location); err != nil {
		return fmt.Errorf("validation: %w", err)
	}

	// 4. Reconcile VMs via OpenTofu (assign IDs/IPs, generate tfvars, tofu apply)
	result, err := reconciler.Reconcile(ctx, services)
	if err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}

	// 5. If new VMs were created (IDs/IPs assigned), save services.yml
	if len(result.NewVMs) > 0 {
		if err := SaveServices(servicesPath, services); err != nil {
			return fmt.Errorf("save services: %w", err)
		}
	}

	// 6. Bootstrap VMs that need it (new VMs or VMs where doco-cd isn't running)
	localVMs := services.VMsForLocation(cfg.Location)
	for vmName, vm := range localVMs {
		if vm.StaticIP == "" {
			continue
		}
		needsBootstrap := contains(result.NewVMs, vmName)
		if !needsBootstrap {
			// Check if doco-cd is running
			running, err := bootstrapper.IsDocoCDRunning(vm.StaticIP)
			if err != nil {
				logger.Warn("could not check doco-cd status", "vm", vmName, "error", err)
				continue
			}
			needsBootstrap = !running
		}
		if needsBootstrap {
			logger.Info("bootstrapping VM (doco-cd not running)", "vm", vmName)
			if err := bootstrapper.Bootstrap(ctx, vm.StaticIP, vm, cfg.RepoURL); err != nil {
				logger.Error("bootstrap failed", "vm", vmName, "error", err)
			}
		}
	}

	// 7. Generate configs (doco-cd + traefik routes)
	configsChanged, err := gen.GenerateAll(services, cfg.Location)
	if err != nil {
		return fmt.Errorf("generate configs: %w", err)
	}

	// 8. Commit and push if anything changed
	hasChanges, err := git.HasChanges()
	if err != nil {
		return fmt.Errorf("check changes: %w", err)
	}

	if hasChanges {
		msg := "proxpilot: update configs"
		if len(result.NewVMs) > 0 {
			msg = fmt.Sprintf("proxpilot: provision VMs %v", result.NewVMs)
		} else if configsChanged {
			msg = "proxpilot: regenerate configs"
		}
		if err := git.CommitAndPush(msg); err != nil {
			logger.Warn("commit and push failed (non-fatal)", "error", err)
		}
	} else {
		logger.Info("no changes to commit")
	}

	logger.Info("reconciliation cycle complete",
		"new_vms", len(result.NewVMs),
		"tofu_applied", result.TofuApplied,
	)
	return nil
}
