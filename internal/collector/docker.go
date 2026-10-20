package collector

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"

	"github.com/otuletta/atalaya/internal/config"
)

// Container is a running Docker container.
type Container struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Image   string `json:"image"`
	State   string `json:"state"`
	Status  string `json:"status"`
	Ports   string `json:"ports,omitempty"`
	Project string `json:"project,omitempty"`
}

// DockerSnapshot is the result of the docker collector. Available is false
// when Docker is not installed or its daemon is stopped; that is a normal
// state, not an error.
type DockerSnapshot struct {
	Available  bool        `json:"available"`
	Note       string      `json:"note,omitempty"`
	Containers []Container `json:"containers"`
}

// Docker lists running containers through the docker CLI, which already
// knows the active context (Docker Desktop, OrbStack, Colima…).
type Docker struct {
	Projects []config.Project
	Every    time.Duration
	// Binary overrides the docker executable (tests).
	Binary string

	matcher *projectMatcher
}

func (d *Docker) Name() string            { return "docker" }
func (d *Docker) Interval() time.Duration { return d.Every }

func (d *Docker) Collect(ctx context.Context) (Snapshot, error) {
	if d.matcher == nil {
		d.matcher = newProjectMatcher(d.Projects)
	}
	bin := d.Binary
	if bin == "" {
		bin = "docker"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return DockerSnapshot{Note: "docker no está instalado"}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := run(ctx, nil, bin, "ps", "--format", "{{json .}}")
	if err != nil {
		if strings.Contains(err.Error(), "Cannot connect to the Docker daemon") || strings.Contains(err.Error(), "daemon is not running") {
			return DockerSnapshot{Note: "el daemon de Docker no está corriendo"}, nil
		}
		return nil, err
	}
	return DockerSnapshot{Available: true, Containers: parseDockerPS(out, d.matcher)}, nil
}

func parseDockerPS(out []byte, m *projectMatcher) []Container {
	var cs []Container
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var row struct {
			ID, Names, Image, State, Status, Ports, Labels string
		}
		if json.Unmarshal(sc.Bytes(), &row) != nil {
			continue
		}
		c := Container{ID: row.ID, Name: row.Names, Image: row.Image, State: row.State, Status: row.Status, Ports: row.Ports}
		// Compose records where it was launched from: map it to a project.
		for _, kv := range strings.Split(row.Labels, ",") {
			if dir, ok := strings.CutPrefix(kv, "com.docker.compose.project.working_dir="); ok && m != nil {
				c.Project = m.match(dir)
			}
		}
		cs = append(cs, c)
	}
	return cs
}
