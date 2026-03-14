package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

//go:embed all:web/dist
var webAssets embed.FS

// CycleRecord stores the result of a reconciliation cycle.
type CycleRecord struct {
	StartedAt     time.Time `json:"started_at"`
	EndedAt       time.Time `json:"ended_at"`
	DurationMs    int64     `json:"duration_ms"`
	CommitHash    string    `json:"commit_hash,omitempty"`
	NewVMs        []string  `json:"new_vms"`
	TofuApplied   bool      `json:"tofu_applied"`
	ConfigsPushed bool      `json:"configs_pushed"`
	Error         string    `json:"error,omitempty"`
}

// WebServer serves the ProxPilot dashboard UI and API.
type WebServer struct {
	config    *Config
	mu        sync.RWMutex
	services  *ServicesFile
	cycles    []CycleRecord
	startTime time.Time
	logger    *slog.Logger
}

func NewWebServer(config *Config, logger *slog.Logger) *WebServer {
	return &WebServer{
		config:    config,
		cycles:    make([]CycleRecord, 0),
		startTime: time.Now(),
		logger:    logger,
	}
}

func (ws *WebServer) UpdateServices(sf *ServicesFile) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.services = sf
}

func (ws *WebServer) RecordCycle(record CycleRecord) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.cycles = append(ws.cycles, record)
	if len(ws.cycles) > 100 {
		ws.cycles = ws.cycles[len(ws.cycles)-100:]
	}
}

// API response types

type statusResponse struct {
	Version       string       `json:"version"`
	Location      string       `json:"location"`
	UptimeSeconds float64      `json:"uptime_seconds"`
	LastCycle      *CycleRecord `json:"last_cycle"`
	CycleCount    int          `json:"cycle_count"`
	VMCount       int          `json:"vm_count"`
	ServiceCount  int          `json:"service_count"`
	RouteCount    int          `json:"route_count"`
}

type vmResponse struct {
	Location string            `json:"location"`
	VMID     int               `json:"vmid"`
	StaticIP string            `json:"static_ip"`
	Cores    int               `json:"cores"`
	MemoryMB int               `json:"memory_mb"`
	DiskGB   int               `json:"disk_gb"`
	Services []serviceResponse `json:"services"`
	Routes   []routeResponse   `json:"routes"`
}

type serviceResponse struct {
	ServiceDir  string `json:"service_dir"`
	ComposeFile string `json:"compose_file"`
	ProjectName string `json:"project_name"`
	Primary     bool   `json:"primary"`
	IsGlobal    bool   `json:"is_global"`
}

type routeResponse struct {
	Name      string `json:"name"`
	Subdomain string `json:"subdomain"`
	Port      int    `json:"port"`
	Protocol  string `json:"protocol"`
	URL       string `json:"url"`
	Auth      bool   `json:"auth"`
}

type vmsAPIResponse struct {
	Domain         string                `json:"domain"`
	VMs            map[string]vmResponse `json:"vms"`
	GlobalServices []serviceResponse     `json:"global_services"`
	Defaults       defaultsResponse      `json:"defaults"`
}

type defaultsResponse struct {
	Cores    int `json:"cores"`
	MemoryMB int `json:"memory_mb"`
	DiskGB   int `json:"disk_gb"`
}

type cyclesAPIResponse struct {
	Cycles []CycleRecord `json:"cycles"`
}

// API handlers

func (ws *WebServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	ws.mu.RLock()
	defer ws.mu.RUnlock()

	resp := statusResponse{
		Version:       version,
		Location:      ws.config.Location,
		UptimeSeconds: time.Since(ws.startTime).Seconds(),
		CycleCount:    len(ws.cycles),
	}

	if len(ws.cycles) > 0 {
		last := ws.cycles[len(ws.cycles)-1]
		resp.LastCycle = &last
	}

	if ws.services != nil {
		for _, vm := range ws.services.VMs {
			resp.VMCount++
			resp.ServiceCount += len(ws.services.AllServices(vm))
			resp.RouteCount += len(vm.Routes)
		}
	}

	writeJSON(w, resp)
}

func (ws *WebServer) handleVMs(w http.ResponseWriter, r *http.Request) {
	ws.mu.RLock()
	defer ws.mu.RUnlock()

	if ws.services == nil {
		writeJSON(w, vmsAPIResponse{VMs: make(map[string]vmResponse)})
		return
	}

	globalNames := make(map[string]bool)
	for _, gs := range ws.services.GlobalServices {
		globalNames[gs.ProjectName] = true
	}

	resp := vmsAPIResponse{
		Domain: ws.services.Domain,
		VMs:    make(map[string]vmResponse),
		Defaults: defaultsResponse{
			Cores:    ws.services.VMDefaults.Cores,
			MemoryMB: ws.services.VMDefaults.MemoryMB,
			DiskGB:   ws.services.VMDefaults.DiskGB,
		},
	}

	for name, vm := range ws.services.VMs {
		allSvc := ws.services.AllServices(vm)
		services := make([]serviceResponse, 0, len(allSvc))
		for _, svc := range allSvc {
			services = append(services, serviceResponse{
				ServiceDir:  svc.ServiceDir,
				ComposeFile: svc.ComposeFile,
				ProjectName: svc.ProjectName,
				Primary:     svc.Primary,
				IsGlobal:    globalNames[svc.ProjectName],
			})
		}

		routes := make([]routeResponse, 0, len(vm.Routes))
		for _, rt := range vm.Routes {
			routes = append(routes, routeResponse(rt))
		}

		resp.VMs[name] = vmResponse{
			Location: vm.Location,
			VMID:     vm.VMID,
			StaticIP: vm.StaticIP,
			Cores:    vm.EffectiveCores(ws.services.VMDefaults),
			MemoryMB: vm.EffectiveMemoryMB(ws.services.VMDefaults),
			DiskGB:   vm.EffectiveDiskGB(ws.services.VMDefaults),
			Services: services,
			Routes:   routes,
		}
	}

	for _, gs := range ws.services.GlobalServices {
		resp.GlobalServices = append(resp.GlobalServices, serviceResponse{
			ServiceDir:  gs.ServiceDir,
			ComposeFile: gs.ComposeFile,
			ProjectName: gs.ProjectName,
			IsGlobal:    true,
		})
	}

	writeJSON(w, resp)
}

func (ws *WebServer) handleCycles(w http.ResponseWriter, r *http.Request) {
	ws.mu.RLock()
	defer ws.mu.RUnlock()

	reversed := make([]CycleRecord, len(ws.cycles))
	for i, c := range ws.cycles {
		reversed[len(ws.cycles)-1-i] = c
	}

	writeJSON(w, cyclesAPIResponse{Cycles: reversed})
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// spaHandler serves embedded static files, falling back to index.html for unknown paths.
func spaHandler(root http.FileSystem) http.Handler {
	fileServer := http.FileServer(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path != "/" {
			f, err := root.Open(path)
			if err != nil {
				r.URL.Path = "/"
			} else {
				_ = f.Close()
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}

func (ws *WebServer) Start(port int) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/status", ws.handleStatus)
	mux.HandleFunc("/api/vms", ws.handleVMs)
	mux.HandleFunc("/api/cycles", ws.handleCycles)

	distFS, err := fs.Sub(webAssets, "web/dist")
	if err != nil {
		return fmt.Errorf("embedded assets: %w", err)
	}
	mux.Handle("/", spaHandler(http.FS(distFS)))

	addr := fmt.Sprintf(":%d", port)
	ws.logger.Info("starting web server", "addr", addr)
	return http.ListenAndServe(addr, mux)
}
