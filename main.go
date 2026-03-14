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

type cycleResult struct {
	Updated       bool
	CommitHash    string
	NewVMs        []string
	TofuApplied   bool
	ConfigsPushed bool
}

func main() {
	configPath := flag.String("config", "/opt/proxops/config.yml", "path to config file")
	once := flag.Bool("once", false, "run one reconciliation cycle and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("proxops %s (%s)\n", version, commit)
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

	logger.Info("proxops starting",
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

	updater := NewUpdater(cfg.UpdateRepo, cfg.UpdateInterval, logger)

	// Start web dashboard
	ws := NewWebServer(cfg, logger)
	go func() {
		if err := ws.Start(cfg.WebPort); err != nil {
			logger.Error("web server failed", "error", err)
		}
	}()

	// Ensure repo is cloned
	if err := git.EnsureCloned(); err != nil {
		logger.Error("failed to clone repo", "error", err)
		os.Exit(1)
	}

	// doCycle wraps runCycle with timing and cycle recording for the dashboard.
	doCycle := func() (cycleResult, error) {
		start := time.Now()
		result, err := runCycle(ctx, cfg, git, reconciler, bootstrapper, gen, updater, ws, logger)
		record := CycleRecord{
			StartedAt:     start,
			EndedAt:       time.Now(),
			DurationMs:    time.Since(start).Milliseconds(),
			CommitHash:    result.CommitHash,
			NewVMs:        result.NewVMs,
			TofuApplied:   result.TofuApplied,
			ConfigsPushed: result.ConfigsPushed,
		}
		if err != nil {
			record.Error = err.Error()
		}
		ws.RecordCycle(record)
		return result, err
	}

	if *once {
		if _, err := doCycle(); err != nil {
			logger.Error("reconciliation failed", "error", err)
			os.Exit(1)
		}
		return
	}

	// Poll loop
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	// Run immediately on start
	if result, err := doCycle(); err != nil {
		logger.Error("reconciliation failed", "error", err)
	} else if result.Updated {
		logger.Info("binary updated, exiting for systemd restart")
		os.Exit(0)
	}

	for {
		select {
		case <-ctx.Done():
			logger.Info("shutting down")
			return
		case <-ticker.C:
			if result, err := doCycle(); err != nil {
				logger.Error("reconciliation failed", "error", err)
			} else if result.Updated {
				logger.Info("binary updated, exiting for systemd restart")
				os.Exit(0)
			}
		}
	}
}

// runCycle performs one full reconciliation cycle:
// pull → check version → reconcile VMs (tofu) → bootstrap new VMs → generate configs → commit & push.
// Returns cycleResult where Updated=true means the binary was replaced and caller should exit.
func runCycle(ctx context.Context, cfg *Config, git *GitOps, reconciler *Reconciler, bootstrapper *Bootstrapper, gen *Generator, updater *Updater, ws *WebServer, logger *slog.Logger) (cycleResult, error) {
	logger.Info("starting reconciliation cycle")

	// 1. Pull latest
	if err := git.Pull(); err != nil {
		return cycleResult{}, fmt.Errorf("git pull: %w", err)
	}

	// 2. Record current commit hash
	commitHash := git.HeadCommit()

	// 3. Load services
	servicesPath := filepath.Join(cfg.RepoDir, "ansible", "services.yml")
	services, err := LoadServices(servicesPath)
	if err != nil {
		return cycleResult{}, fmt.Errorf("load services: %w", err)
	}

	// Update web dashboard with latest services data
	ws.UpdateServices(services)

	// 3. Self-update: if services.yml declares a proxops_version, match it immediately
	if services.ProxOpsVersion != "" {
		if updater.UpdateToVersion(services.ProxOpsVersion) {
			return cycleResult{Updated: true}, nil
		}
	} else if cfg.AutoUpdate {
		// No pinned version — fall back to auto-update to latest (rate-limited)
		if updater.CheckAndUpdateToLatest() {
			return cycleResult{Updated: true}, nil
		}
	}

	// 4. Validate
	if err := ensureSubdomainUniqueness(services, cfg.Location); err != nil {
		return cycleResult{}, fmt.Errorf("validation: %w", err)
	}

	// 5. Reconcile VMs via OpenTofu (assign IDs/IPs, generate tfvars, tofu apply)
	result, err := reconciler.Reconcile(ctx, services)
	if err != nil {
		return cycleResult{}, fmt.Errorf("reconcile: %w", err)
	}

	// 6. If new VMs were created (IDs/IPs assigned), save services.yml
	if len(result.NewVMs) > 0 {
		if err := SaveServices(servicesPath, services); err != nil {
			return cycleResult{}, fmt.Errorf("save services: %w", err)
		}
	}

	// 7. Bootstrap VMs that need it (new VMs or VMs where doco-cd isn't running)
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

	// 8. Generate configs (doco-cd + traefik routes)
	configsChanged, err := gen.GenerateAll(services, cfg.Location)
	if err != nil {
		return cycleResult{}, fmt.Errorf("generate configs: %w", err)
	}

	// 9. Commit and push if anything changed
	hasChanges, err := git.HasChanges()
	if err != nil {
		return cycleResult{}, fmt.Errorf("check changes: %w", err)
	}

	pushed := false
	if hasChanges {
		msg := "proxops: update configs"
		if len(result.NewVMs) > 0 {
			msg = fmt.Sprintf("proxops: provision VMs %v", result.NewVMs)
		} else if configsChanged {
			msg = "proxops: regenerate configs"
		}
		if err := git.CommitAndPush(msg); err != nil {
			logger.Warn("commit and push failed (non-fatal)", "error", err)
		} else {
			pushed = true
		}
	} else {
		logger.Info("no changes to commit")
	}

	logger.Info("reconciliation cycle complete",
		"new_vms", len(result.NewVMs),
		"tofu_applied", result.TofuApplied,
	)
	return cycleResult{
		CommitHash:    commitHash,
		NewVMs:        result.NewVMs,
		TofuApplied:   result.TofuApplied,
		ConfigsPushed: pushed,
	}, nil
}
