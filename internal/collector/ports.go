package collector

import (
	"context"
	"slices"
	"sort"
	"time"

	psnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"

	"github.com/otuletta/atalaya/internal/config"
)

// Port is a listening TCP port and the process behind it.
type Port struct {
	Port    uint32    `json:"port"`
	Addr    string    `json:"addr"` // "*" when listening on all interfaces
	PID     int32     `json:"pid"`
	Process string    `json:"process"`
	Project string    `json:"project,omitempty"`
	Cwd     string    `json:"cwd,omitempty"`
	Cmdline string    `json:"cmdline,omitempty"`
	Started time.Time `json:"started"`
}

// PortsSnapshot is the result of the ports collector.
type PortsSnapshot struct {
	Ports []Port `json:"ports"`
}

// ephemeralStart is where the IANA dynamic port range begins. Apps such as
// editors and chat clients listen there on random ports.
const ephemeralStart = 49152

// Ports lists listening ports and maps each to its process and project
// (`:3000 → node · odomto`).
type Ports struct {
	Projects      []config.Project
	Ignore        []string
	ShowEphemeral bool
	Every         time.Duration

	matcher *projectMatcher
}

func (p *Ports) Name() string            { return "ports" }
func (p *Ports) Interval() time.Duration { return p.Every }

func (p *Ports) Collect(ctx context.Context) (Snapshot, error) {
	if p.matcher == nil {
		p.matcher = newProjectMatcher(p.Projects)
	}
	conns, err := psnet.ConnectionsWithContext(ctx, "tcp")
	if err != nil {
		return nil, err
	}
	type key struct {
		port uint32
		pid  int32
	}
	addrs := map[key][]string{}
	var keys []key
	for _, c := range conns {
		if c.Status != "LISTEN" || c.Pid == 0 {
			continue
		}
		k := key{c.Laddr.Port, c.Pid}
		if _, seen := addrs[k]; !seen {
			keys = append(keys, k)
		}
		addrs[k] = append(addrs[k], c.Laddr.IP)
	}
	procs := map[int32]procInfo{}
	var ports []Port
	for _, k := range keys {
		info, ok := procs[k.pid]
		if !ok {
			if pr, err := process.NewProcessWithContext(ctx, k.pid); err == nil {
				info = inspect(ctx, pr)
			} else {
				info = procInfo{PID: k.pid}
			}
			procs[k.pid] = info
		}
		if slices.Contains(p.Ignore, info.Name) {
			continue
		}
		project := p.matcher.match(info.Cwd)
		if k.port >= ephemeralStart && project == "" && !p.ShowEphemeral {
			continue
		}
		ports = append(ports, Port{
			Port: k.port, Addr: bindAddr(addrs[k]), PID: k.pid, Process: info.Name,
			Project: project, Cwd: info.Cwd, Cmdline: commandLine(info.Cmdline), Started: info.Started,
		})
	}
	sort.Slice(ports, func(i, j int) bool {
		if ports[i].Port != ports[j].Port {
			return ports[i].Port < ports[j].Port
		}
		return ports[i].PID < ports[j].PID
	})
	return PortsSnapshot{Ports: ports}, nil
}

// bindAddr collapses the IPv4/IPv6 pair of a socket into one label.
func bindAddr(ips []string) string {
	for _, ip := range ips {
		switch ip {
		case "*", "0.0.0.0", "::", "":
			return "*"
		}
	}
	slices.Sort(ips)
	for _, ip := range ips {
		if ip == "127.0.0.1" || ip == "::1" {
			return "localhost"
		}
	}
	return ips[0]
}
