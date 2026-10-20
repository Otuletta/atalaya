package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"github.com/otuletta/atalaya/internal/config"
	"github.com/otuletta/atalaya/internal/scheduler"
	"github.com/otuletta/atalaya/internal/store"
)

func newSigner(t *testing.T) gossh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// startServer serves the demo over SSH on a random port, for one key.
func startServer(t *testing.T, allowed gossh.Signer) string {
	t.Helper()
	dir := t.TempDir()
	keys := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(keys, gossh.MarshalAuthorizedKey(allowed.PublicKey()), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := load("", true, false)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New()
	scheduler.New(st, s.sources...).RunOnce(context.Background())
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- serveSSH(ctx, func() *config.Config { return s.cfg }, st, l, keys, filepath.Join(dir, "host_key"), nil)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("serveSSH: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("the server did not stop")
		}
	})
	return l.Addr().String()
}

func dial(addr string, signer gossh.Signer) (*gossh.Client, error) {
	return gossh.Dial("tcp", addr, &gossh.ClientConfig{
		User:            "test",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
}

// syncBuffer is a bytes.Buffer safe to read while the session writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestServeOverSSH(t *testing.T) {
	key := newSigner(t)
	addr := startServer(t, key)

	client, err := dial(addr, key)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.RequestPty("xterm-256color", 30, 140, gossh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	var out syncBuffer
	sess.Stdout = &out
	if err := sess.Shell(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(out.String(), "solo lectura") || !strings.Contains(out.String(), "Resumen") {
		if time.Now().After(deadline) {
			t.Fatalf("no dashboard over SSH, got %q", out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestServeRejectsUnknownKeys(t *testing.T) {
	addr := startServer(t, newSigner(t))
	if c, err := dial(addr, newSigner(t)); err == nil {
		c.Close()
		t.Fatal("a key that is not in authorized_keys got in")
	}
}

func TestServeNeedsATerminal(t *testing.T) {
	key := newSigner(t)
	client, err := dial(startServer(t, key), key)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	out, _ := sess.CombinedOutput("")
	if !strings.Contains(string(out), "PTY") {
		t.Errorf("without a pty = %q", out)
	}
}

func TestServeRefusesWithoutAuthorizedKeys(t *testing.T) {
	_, err := runCmd(t, "--demo", "serve", "--authorized-keys", filepath.Join(t.TempDir(), "none"))
	if err == nil || !strings.Contains(err.Error(), "clave pública") {
		t.Errorf("err = %v", err)
	}
}
