package collector

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/otuletta/atalaya/internal/config"
)

func TestMatchAgent(t *testing.T) {
	patterns := []string{"claude", "cline*"}
	tests := []struct {
		name string
		argv []string
		want string
	}{
		{"claude", nil, "claude"},
		{"Claude", nil, ""}, // the desktop app, not a CLI session
		{"Claude Helper", nil, ""},
		{"node", []string{"node", "/opt/homebrew/bin/claude", "--resume"}, "claude"},
		{"node", []string{"node", "/usr/local/lib/cline-cli/index.js"}, ""},
		{"node", []string{"/usr/bin/node", "/x/cline"}, "cline"},
		{"cline-agent", nil, "cline-agent"},
		{"zsh", []string{"zsh", "claude"}, ""}, // argv[1] only counts for interpreters
		{"vim", []string{"vim", "claude.md"}, ""},
	}
	for _, tt := range tests {
		if got := MatchAgent(patterns, tt.name, tt.argv); got != tt.want {
			t.Errorf("MatchAgent(%q, %q) = %q, want %q", tt.name, tt.argv, got, tt.want)
		}
	}
}

func TestBindAddr(t *testing.T) {
	tests := map[string][]string{
		"*":         {"127.0.0.1", "*"},
		"localhost": {"::1", "127.0.0.1"},
		"10.0.0.5":  {"10.0.0.5"},
	}
	for want, ips := range tests {
		if got := bindAddr(ips); got != want {
			t.Errorf("bindAddr(%v) = %q, want %q", ips, got, want)
		}
	}
}

func TestPortsFindsOwnListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := uint32(ln.Addr().(*net.TCPAddr).Port)
	cwd, _ := os.Getwd()

	p := &Ports{Projects: []config.Project{{Name: "atalaya", Path: cwd}}}
	snap, err := p.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range snap.(PortsSnapshot).Ports {
		if got.Port == port {
			if got.PID != int32(os.Getpid()) || got.Project != "atalaya" || got.Addr != "localhost" {
				t.Errorf("port = %+v", got)
			}
			return
		}
	}
	t.Errorf("own listener on :%d not found (an ephemeral port mapped to a project must be shown)", port)
}

func TestPortsHidesUnmappedEphemeral(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := uint32(ln.Addr().(*net.TCPAddr).Port)
	if port < ephemeralStart {
		t.Skipf("the OS gave a non-ephemeral port %d", port)
	}
	snap, err := (&Ports{}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range snap.(PortsSnapshot).Ports {
		if got.Port == port {
			t.Errorf("unmapped ephemeral port shown: %+v", got)
		}
	}
}

func TestDocker(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "docker")
	script := `#!/bin/sh
cat <<'EOF'
{"ID":"a1b2","Names":"odomto-db-1","Image":"postgres:17","State":"running","Status":"Up 2 hours","Ports":"0.0.0.0:5432->5432/tcp","Labels":"com.docker.compose.project=odomto,com.docker.compose.project.working_dir=/w/odomto"}
not json
{"ID":"c3d4","Names":"redis","Image":"redis:8","State":"running","Status":"Up 5 minutes","Ports":"","Labels":""}
EOF
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &Docker{Projects: []config.Project{{Name: "odomto", Path: "/w/odomto"}}, Binary: fake}
	snap, err := d.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s := snap.(DockerSnapshot)
	if !s.Available || len(s.Containers) != 2 {
		t.Fatalf("snap = %+v", s)
	}
	if c := s.Containers[0]; c.Name != "odomto-db-1" || c.Project != "odomto" || c.Image != "postgres:17" {
		t.Errorf("container = %+v", c)
	}
	if c := s.Containers[1]; c.Project != "" {
		t.Errorf("container without compose labels mapped to %q", c.Project)
	}
}

func TestDockerUnavailable(t *testing.T) {
	d := &Docker{Binary: "docker-that-does-not-exist"}
	snap, err := d.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s := snap.(DockerSnapshot); s.Available || s.Note == "" {
		t.Errorf("snap = %+v", s)
	}

	dir := t.TempDir()
	stopped := filepath.Join(dir, "docker")
	_ = os.WriteFile(stopped, []byte("#!/bin/sh\necho 'Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?' >&2\nexit 1\n"), 0o755)
	snap, err = (&Docker{Binary: stopped}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s := snap.(DockerSnapshot); s.Available || s.Note != "el daemon de Docker no está corriendo" {
		t.Errorf("snap = %+v", s)
	}
}

func TestOllama(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ps" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen3:8b","model":"qwen3:8b","size":6000000000,"size_vram":6000000000,
			"expires_at":"2026-10-19T12:05:00Z","details":{"parameter_size":"8.2B","quantization_level":"Q4_K_M"}}]}`))
	}))
	defer srv.Close()
	snap, err := (&Ollama{Host: srv.URL + "/"}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s := snap.(OllamaSnapshot)
	if !s.Available || len(s.Models) != 1 {
		t.Fatalf("snap = %+v", s)
	}
	if m := s.Models[0]; m.Name != "qwen3:8b" || m.Params != "8.2B" || m.Quant != "Q4_K_M" || m.VRAMBytes != 6e9 {
		t.Errorf("model = %+v", m)
	}
}

func TestOllamaNotRunning(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	snap, err := (&Ollama{Host: "http://" + addr}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s := snap.(OllamaSnapshot); s.Available || s.Note == "" {
		t.Errorf("snap = %+v", s)
	}
}

func TestAgentsCollectRuns(t *testing.T) {
	// The test binary itself is not an agent; this checks the process walk
	// works on this OS and finds a process named like the test binary.
	self, _ := os.Executable()
	a := &Agents{Patterns: []string{filepath.Base(self)}}
	snap, err := a.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, ag := range snap.(AgentsSnapshot).Agents {
		if ag.PID == int32(os.Getpid()) {
			if ag.Cwd == "" || ag.Started.IsZero() {
				t.Errorf("agent details missing: %+v", ag)
			}
			return
		}
	}
	t.Errorf("the test process (%s) was not found", filepath.Base(self))
}
