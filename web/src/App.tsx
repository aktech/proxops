import { useEffect, useState } from "react";

interface Status {
  version: string;
  location: string;
  uptime_seconds: number;
  last_cycle: CycleRecord | null;
  cycle_count: number;
  vm_count: number;
  service_count: number;
  route_count: number;
}

interface CycleRecord {
  started_at: string;
  ended_at: string;
  duration_ms: number;
  commit_hash?: string;
  new_vms: string[];
  tofu_applied: boolean;
  configs_pushed: boolean;
  error?: string;
}

interface VMData {
  location: string;
  vmid: number;
  static_ip: string;
  cores: number;
  memory_mb: number;
  disk_gb: number;
  services: ServiceData[];
  routes: RouteData[];
}

interface ServiceData {
  service_dir: string;
  compose_file: string;
  project_name: string;
  primary: boolean;
  is_global: boolean;
}

interface RouteData {
  name: string;
  subdomain: string;
  port: number;
  protocol: string;
  url: string;
  auth: boolean;
}

interface VMsResponse {
  domain: string;
  vms: Record<string, VMData>;
  global_services: ServiceData[];
  defaults: { cores: number; memory_mb: number; disk_gb: number };
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}

function formatTime(iso: string): string {
  return new Date(iso).toLocaleTimeString();
}

function formatMemory(mb: number): string {
  return mb >= 1024 ? `${(mb / 1024).toFixed(0)}G` : `${mb}M`;
}

function Header({ status }: { status: Status }) {
  return (
    <header className="bg-white border-b border-slate-200 px-6 py-4">
      <div className="max-w-6xl mx-auto flex items-center justify-between">
        <div className="flex items-center gap-3">
          <div className="w-8 h-8 bg-primary rounded-lg flex items-center justify-center">
            <span className="text-white font-bold text-sm">P</span>
          </div>
          <h1 className="text-xl font-semibold text-slate-900">ProxOps</h1>
        </div>
        <div className="flex items-center gap-2">
          <span className="px-2.5 py-1 bg-primary-light text-primary text-sm font-medium rounded-full">
            v{status.version}
          </span>
          <span className="px-2.5 py-1 bg-slate-100 text-slate-600 text-sm font-medium rounded-full">
            {status.location}
          </span>
        </div>
      </div>
    </header>
  );
}

function StatsCards({ status }: { status: Status }) {
  const cards = [
    { label: "Virtual Machines", value: status.vm_count },
    { label: "Services", value: status.service_count },
    { label: "Routes", value: status.route_count },
    { label: "Cycles", value: status.cycle_count },
  ];

  return (
    <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
      {cards.map((card) => (
        <div
          key={card.label}
          className="bg-white rounded-lg border border-slate-200 p-5"
        >
          <div className="text-sm text-slate-500">{card.label}</div>
          <div className="text-2xl font-semibold text-slate-900 mt-1">
            {card.value}
          </div>
        </div>
      ))}
    </div>
  );
}

function VMsSection({ data }: { data: VMsResponse }) {
  const entries = Object.entries(data.vms).sort(([a], [b]) =>
    a.localeCompare(b),
  );

  return (
    <section>
      <h2 className="text-lg font-semibold text-slate-900 mb-3">
        Virtual Machines
      </h2>
      <div className="bg-white rounded-lg border border-slate-200 overflow-hidden">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-slate-200 bg-slate-50">
              <th className="text-left px-4 py-3 font-medium text-slate-600">
                Name
              </th>
              <th className="text-left px-4 py-3 font-medium text-slate-600">
                Location
              </th>
              <th className="text-left px-4 py-3 font-medium text-slate-600">
                VMID
              </th>
              <th className="text-left px-4 py-3 font-medium text-slate-600">
                IP Address
              </th>
              <th className="text-left px-4 py-3 font-medium text-slate-600">
                Resources
              </th>
              <th className="text-left px-4 py-3 font-medium text-slate-600">
                Services
              </th>
            </tr>
          </thead>
          <tbody>
            {entries.map(([name, vm]) => (
              <tr
                key={name}
                className="border-b border-slate-100 last:border-0"
              >
                <td className="px-4 py-3 font-medium text-slate-900">
                  {name}
                </td>
                <td className="px-4 py-3 text-slate-600">{vm.location}</td>
                <td className="px-4 py-3 font-mono text-slate-600">
                  {vm.vmid || "\u2014"}
                </td>
                <td className="px-4 py-3 font-mono text-slate-600">
                  {vm.static_ip || "\u2014"}
                </td>
                <td className="px-4 py-3 text-slate-600">
                  {vm.cores} CPU &middot; {formatMemory(vm.memory_mb)} &middot;{" "}
                  {vm.disk_gb}G
                </td>
                <td className="px-4 py-3">
                  <div className="flex flex-wrap gap-1">
                    {vm.services.map((svc) => (
                      <span
                        key={svc.project_name}
                        className={`px-2 py-0.5 rounded text-xs ${
                          svc.is_global
                            ? "bg-primary-light text-primary"
                            : "bg-slate-100 text-slate-600"
                        }`}
                      >
                        {svc.project_name}
                      </span>
                    ))}
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function RoutesSection({ data }: { data: VMsResponse }) {
  const routes: {
    subdomain: string;
    vm: string;
    port: number;
    url: string;
    auth: boolean;
  }[] = [];

  for (const [vmName, vm] of Object.entries(data.vms)) {
    for (const route of vm.routes) {
      routes.push({
        subdomain: `${route.subdomain}.${data.domain}`,
        vm: vmName,
        port: route.port,
        url: route.url,
        auth: route.auth,
      });
    }
  }

  routes.sort((a, b) => a.subdomain.localeCompare(b.subdomain));

  if (routes.length === 0) return null;

  return (
    <section>
      <h2 className="text-lg font-semibold text-slate-900 mb-3">Routes</h2>
      <div className="bg-white rounded-lg border border-slate-200 overflow-hidden">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-slate-200 bg-slate-50">
              <th className="text-left px-4 py-3 font-medium text-slate-600">
                Subdomain
              </th>
              <th className="text-left px-4 py-3 font-medium text-slate-600">
                VM
              </th>
              <th className="text-left px-4 py-3 font-medium text-slate-600">
                Backend
              </th>
              <th className="text-left px-4 py-3 font-medium text-slate-600">
                Auth
              </th>
            </tr>
          </thead>
          <tbody>
            {routes.map((route) => (
              <tr
                key={route.subdomain}
                className="border-b border-slate-100 last:border-0"
              >
                <td className="px-4 py-3 font-mono text-sm text-slate-900">
                  {route.subdomain}
                </td>
                <td className="px-4 py-3 text-slate-600">{route.vm}</td>
                <td className="px-4 py-3 font-mono text-slate-500">
                  {route.url || `:${route.port}`}
                </td>
                <td className="px-4 py-3">
                  {route.auth ? (
                    <span className="text-green-600 font-medium">Yes</span>
                  ) : (
                    <span className="text-slate-300">&mdash;</span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function CyclesSection({ cycles }: { cycles: CycleRecord[] }) {
  if (cycles.length === 0) return null;

  return (
    <section>
      <h2 className="text-lg font-semibold text-slate-900 mb-3">
        Recent Cycles
      </h2>
      <div className="bg-white rounded-lg border border-slate-200 overflow-hidden">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-slate-200 bg-slate-50">
              <th className="text-left px-4 py-3 font-medium text-slate-600">
                Time
              </th>
              <th className="text-left px-4 py-3 font-medium text-slate-600">
                Commit
              </th>
              <th className="text-left px-4 py-3 font-medium text-slate-600">
                Duration
              </th>
              <th className="text-left px-4 py-3 font-medium text-slate-600">
                New VMs
              </th>
              <th className="text-left px-4 py-3 font-medium text-slate-600">
                Tofu
              </th>
              <th className="text-left px-4 py-3 font-medium text-slate-600">
                Pushed
              </th>
              <th className="text-left px-4 py-3 font-medium text-slate-600">
                Status
              </th>
            </tr>
          </thead>
          <tbody>
            {cycles.slice(0, 20).map((cycle, i) => (
              <tr
                key={i}
                className="border-b border-slate-100 last:border-0"
              >
                <td className="px-4 py-3 font-mono text-slate-600">
                  {formatTime(cycle.started_at)}
                </td>
                <td className="px-4 py-3 font-mono text-slate-500 text-xs">
                  {cycle.commit_hash || "\u2014"}
                </td>
                <td className="px-4 py-3 text-slate-600">
                  {formatDuration(cycle.duration_ms)}
                </td>
                <td className="px-4 py-3 text-slate-600">
                  {cycle.new_vms?.length || 0}
                </td>
                <td className="px-4 py-3">
                  {cycle.tofu_applied ? (
                    <span className="text-primary font-medium">Applied</span>
                  ) : (
                    <span className="text-slate-300">&mdash;</span>
                  )}
                </td>
                <td className="px-4 py-3">
                  {cycle.configs_pushed ? (
                    <span className="text-primary font-medium">Yes</span>
                  ) : (
                    <span className="text-slate-300">&mdash;</span>
                  )}
                </td>
                <td className="px-4 py-3">
                  {cycle.error ? (
                    <span
                      className="px-2 py-0.5 bg-red-50 text-red-700 rounded text-xs font-medium"
                      title={cycle.error}
                    >
                      Error
                    </span>
                  ) : (
                    <span className="px-2 py-0.5 bg-green-50 text-green-700 rounded text-xs font-medium">
                      OK
                    </span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

const DEMO_STATUS: Status = {
  version: "0.5.0",
  location: "rohini",
  uptime_seconds: 7263,
  last_cycle: {
    started_at: new Date(Date.now() - 45000).toISOString(),
    ended_at: new Date(Date.now() - 42700).toISOString(),
    duration_ms: 2300,
    new_vms: [],
    tofu_applied: false,
    configs_pushed: false,
  },
  cycle_count: 121,
  vm_count: 3,
  service_count: 8,
  route_count: 6,
};

const DEMO_VMS: VMsResponse = {
  domain: "iakte.ch",
  vms: {
    Traefik: {
      location: "rohini",
      vmid: 100,
      static_ip: "192.168.1.220",
      cores: 2,
      memory_mb: 2048,
      disk_gb: 20,
      services: [
        { service_dir: "traefik", compose_file: "docker-compose.rohini.yml", project_name: "traefik", primary: true, is_global: false },
        { service_dir: "grafana-alloy", compose_file: "docker-compose.yml", project_name: "grafana-alloy", primary: false, is_global: true },
      ],
      routes: [
        { name: "Traefik Dashboard", subdomain: "traefik.rohini", port: 8080, protocol: "http", url: "", auth: true },
        { name: "AdGuard", subdomain: "adguard.rohini", port: 3000, protocol: "http", url: "", auth: true },
      ],
    },
    "Frigate NVR": {
      location: "rohini",
      vmid: 102,
      static_ip: "192.168.1.222",
      cores: 4,
      memory_mb: 8192,
      disk_gb: 60,
      services: [
        { service_dir: "frigate-nvr-delhi", compose_file: "docker-compose.yml", project_name: "frigate", primary: true, is_global: false },
        { service_dir: "grafana-alloy", compose_file: "docker-compose.yml", project_name: "grafana-alloy", primary: false, is_global: true },
      ],
      routes: [
        { name: "Frigate", subdomain: "frigate.rohini", port: 8971, protocol: "http", url: "", auth: true },
      ],
    },
    WireGuard: {
      location: "rohini",
      vmid: 101,
      static_ip: "192.168.1.221",
      cores: 2,
      memory_mb: 2048,
      disk_gb: 20,
      services: [
        { service_dir: "wireguard-config", compose_file: "docker-compose.yml", project_name: "wireguard", primary: true, is_global: false },
        { service_dir: "vpn", compose_file: "docker-compose.yml", project_name: "frpc", primary: false, is_global: false },
        { service_dir: "grafana-alloy", compose_file: "docker-compose.yml", project_name: "grafana-alloy", primary: false, is_global: true },
      ],
      routes: [
        { name: "WireGuard UI", subdomain: "wg.rohini", port: 51821, protocol: "http", url: "", auth: true },
      ],
    },
  },
  global_services: [
    { service_dir: "grafana-alloy", compose_file: "docker-compose.yml", project_name: "grafana-alloy", primary: false, is_global: true },
  ],
  defaults: { cores: 2, memory_mb: 2048, disk_gb: 20 },
};

const DEMO_CYCLES: CycleRecord[] = [
  { started_at: new Date(Date.now() - 45000).toISOString(), ended_at: new Date(Date.now() - 42700).toISOString(), duration_ms: 2300, commit_hash: "1d5c168", new_vms: [], tofu_applied: false, configs_pushed: false },
  { started_at: new Date(Date.now() - 105000).toISOString(), ended_at: new Date(Date.now() - 103200).toISOString(), duration_ms: 1800, commit_hash: "1d5c168", new_vms: [], tofu_applied: false, configs_pushed: false },
  { started_at: new Date(Date.now() - 165000).toISOString(), ended_at: new Date(Date.now() - 160500).toISOString(), duration_ms: 4500, commit_hash: "ce04b40", new_vms: [], tofu_applied: true, configs_pushed: true },
  { started_at: new Date(Date.now() - 225000).toISOString(), ended_at: new Date(Date.now() - 223800).toISOString(), duration_ms: 1200, commit_hash: "883d098", new_vms: [], tofu_applied: false, configs_pushed: false },
  { started_at: new Date(Date.now() - 285000).toISOString(), ended_at: new Date(Date.now() - 283000).toISOString(), duration_ms: 2000, commit_hash: "6124e77", new_vms: [], tofu_applied: false, configs_pushed: false, error: "git pull: authentication failed" },
];

export default function App() {
  const [status, setStatus] = useState<Status | null>(null);
  const [vms, setVms] = useState<VMsResponse | null>(null);
  const [cycles, setCycles] = useState<CycleRecord[]>([]);
  const [demo, setDemo] = useState(false);

  useEffect(() => {
    const fetchData = async () => {
      try {
        const [statusRes, vmsRes, cyclesRes] = await Promise.all([
          fetch("/api/status").then((r) => r.json()),
          fetch("/api/vms").then((r) => r.json()),
          fetch("/api/cycles").then((r) => r.json()),
        ]);
        setStatus(statusRes);
        setVms(vmsRes);
        setCycles(cyclesRes.cycles || []);
        setDemo(false);
      } catch {
        setStatus(DEMO_STATUS);
        setVms(DEMO_VMS);
        setCycles(DEMO_CYCLES);
        setDemo(true);
      }
    };

    fetchData();
    const interval = setInterval(fetchData, 10000);
    return () => clearInterval(interval);
  }, []);

  if (!status || !vms) {
    return (
      <div className="min-h-screen bg-slate-50 flex items-center justify-center">
        <p className="text-slate-400">Loading...</p>
      </div>
    );
  }

  return (
    <div className="min-h-screen bg-slate-50">
      {demo && (
        <div className="bg-amber-50 border-b border-amber-200 px-6 py-2 text-center text-sm text-amber-700">
          Demo mode &mdash; showing sample data (API not available)
        </div>
      )}
      <Header status={status} />
      <main className="max-w-6xl mx-auto px-6 py-8 space-y-8">
        <StatsCards status={status} />
        <VMsSection data={vms} />
        <RoutesSection data={vms} />
        <CyclesSection cycles={cycles} />
      </main>
    </div>
  );
}
