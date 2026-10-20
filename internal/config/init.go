package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// FoundRepo is a git repository discovered by Scan.
type FoundRepo struct {
	Name   string
	Path   string
	GitHub string // owner/repo when origin points to GitHub
}

// skipDirs are never descended into while scanning.
var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "Library": true, ".Trash": true,
	"target": true, "dist": true, "build": true, ".venv": true, "venv": true,
}

// Scan finds git repositories under root, at most maxDepth levels deep. It
// does not descend into a repository once found, nor into hidden folders.
func Scan(ctx context.Context, root string, maxDepth int) ([]FoundRepo, error) {
	root, err := filepath.Abs(ExpandHome(root))
	if err != nil {
		return nil, err
	}
	var repos []FoundRepo
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Unreadable folders (permissions) are skipped, not fatal.
			if d != nil && d.IsDir() && path != root {
				return fs.SkipDir
			}
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		name := d.Name()
		if path != root && (strings.HasPrefix(name, ".") || skipDirs[name]) {
			return fs.SkipDir
		}
		if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
			repos = append(repos, FoundRepo{
				Name:   name,
				Path:   path,
				GitHub: githubFromOrigin(ctx, path),
			})
			return fs.SkipDir
		}
		depth := strings.Count(strings.TrimPrefix(path, root), string(filepath.Separator))
		if depth >= maxDepth {
			return fs.SkipDir
		}
		return nil
	})
	sort.Slice(repos, func(i, j int) bool { return strings.ToLower(repos[i].Path) < strings.ToLower(repos[j].Path) })
	dedupeNames(repos)
	return repos, err
}

// dedupeNames makes names unique by appending the parent folder.
func dedupeNames(repos []FoundRepo) {
	count := map[string]int{}
	for _, r := range repos {
		count[r.Name]++
	}
	for i, r := range repos {
		if count[r.Name] > 1 {
			repos[i].Name = filepath.Base(filepath.Dir(r.Path)) + "/" + r.Name
		}
	}
}

func githubFromOrigin(ctx context.Context, dir string) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return ParseGitHubRemote(strings.TrimSpace(string(out)))
}

var githubRemoteRE = regexp.MustCompile(`^(?:https://(?:[^@/]+@)?github\.com/|git@github\.com:|ssh://git@github\.com/)([A-Za-z0-9-]+/[A-Za-z0-9._-]+?)(?:\.git)?/?$`)

// ParseGitHubRemote extracts owner/repo from a GitHub remote URL.
func ParseGitHubRemote(remote string) string {
	m := githubRemoteRE.FindStringSubmatch(remote)
	if m == nil {
		return ""
	}
	return m[1]
}

// Render writes repos as [[project]] blocks. With header it also writes the
// file preamble and the [agents] section.
func Render(repos []FoundRepo, header bool) []byte {
	var b bytes.Buffer
	if header {
		b.WriteString("# Atalaya — generado por `atalaya init`.\n")
		b.WriteString("# Edita a mano: añade health = [\"https://…\"] a los proyectos con producción.\n\n")
		b.WriteString("# editor = \"code\"   # para la tecla o; por defecto $VISUAL / $EDITOR\n\n")
	}
	for _, r := range repos {
		b.WriteString("[[project]]\n")
		fmt.Fprintf(&b, "name = %s\n", strconv.Quote(r.Name))
		fmt.Fprintf(&b, "path = %s\n", strconv.Quote(ContractHome(r.Path)))
		if r.GitHub != "" {
			fmt.Fprintf(&b, "github = %s\n", strconv.Quote(r.GitHub))
		}
		b.WriteString("\n")
	}
	if header {
		b.WriteString("[agents]\n")
		b.WriteString("patterns = [\"claude\", \"cline\"]\n")
	}
	return b.Bytes()
}

// InitResult says what Init did.
type InitResult struct {
	Path    string
	Added   []FoundRepo
	Skipped int // already registered
	Created bool
}

// Init scans root and writes the repos to the config at path. An existing
// file is never rewritten: new projects are appended to it as text, so
// comments and formatting survive.
func Init(ctx context.Context, path, root string, maxDepth int) (InitResult, error) {
	res := InitResult{Path: path}
	repos, err := Scan(ctx, root, maxDepth)
	if err != nil {
		return res, err
	}
	existing, err := Load(path)
	switch {
	case errors.Is(err, ErrNotFound):
		res.Created = true
	case err != nil:
		return res, fmt.Errorf("the existing config is invalid, fix it first: %w", err)
	}
	if existing != nil {
		have := map[string]bool{}
		names := map[string]bool{}
		for _, p := range existing.Projects {
			have[p.Path] = true
			names[p.Name] = true
		}
		var fresh []FoundRepo
		for _, r := range repos {
			if have[r.Path] {
				res.Skipped++
				continue
			}
			for names[r.Name] {
				r.Name += "-2"
			}
			names[r.Name] = true
			fresh = append(fresh, r)
		}
		repos = fresh
	}
	res.Added = repos
	if len(repos) == 0 {
		return res, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return res, err
	}
	if res.Created {
		return res, os.WriteFile(path, Render(repos, true), 0o644)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return res, err
	}
	block := append([]byte("\n# Añadidos por `atalaya init` el "+time.Now().Format("2006-01-02")+"\n"), Render(repos, false)...)
	if _, err := f.Write(block); err != nil {
		_ = f.Close()
		return res, err
	}
	return res, f.Close()
}
