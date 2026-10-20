package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/ssh"
	"charm.land/wish/v2"
	"charm.land/wish/v2/activeterm"
	bm "charm.land/wish/v2/bubbletea"
	"charm.land/wish/v2/logging"

	"github.com/otuletta/atalaya/internal/config"
	"github.com/otuletta/atalaya/internal/store"
	"github.com/otuletta/atalaya/internal/ui"
)

// serveCmd runs the collectors once for everyone and serves the dashboard
// over SSH: `ssh -p 2222 tu-mac.local` from another machine or a phone.
func serveCmd(ctx context.Context, path string, demoMode bool, addr, authorizedKeys string, out io.Writer) error {
	keys := config.ExpandHome(authorizedKeys)
	if _, err := os.Stat(keys); err != nil {
		return fmt.Errorf("sin %s no hay a quién dejar entrar: añade ahí tu clave pública (la de ~/.ssh/id_ed25519.pub) o indica otro archivo con --authorized-keys", config.ContractHome(keys))
	}
	s, err := load(path, demoMode, true)
	if err != nil {
		return err
	}
	stateDir, err := config.StateDir()
	if err != nil {
		stateDir = os.TempDir()
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	st := store.New()
	e := startEngine(ctx, path, s, st)
	go e.Watch(ctx, 2*time.Second) // each new session sees the config in use

	host, port, _ := net.SplitHostPort(l.Addr().String())
	fmt.Fprintf(out, "Atalaya por SSH en %s, solo lectura, para las claves de %s.\n  ssh -p %s %s\n",
		l.Addr(), config.ContractHome(keys), port, host)
	if host != "127.0.0.1" && host != "::1" {
		fmt.Fprintln(out, "  Escucha fuera de localhost: cualquiera de tu red con una de esas claves puede verlo.")
	}
	return serveSSH(ctx, e.Config, st, l, keys, filepath.Join(stateDir, "ssh_host_ed25519"), e.Refresh)
}

// serveSSH serves a read-only dashboard to every SSH connection until ctx is
// done. All sessions share the store; each one has its own subscription.
func serveSSH(ctx context.Context, cfg func() *config.Config, st *store.Store, l net.Listener, authorizedKeys, hostKey string, refresh func(...string)) error {
	srv, err := wish.NewServer(
		wish.WithHostKeyPath(hostKey),
		wish.WithAuthorizedKeys(authorizedKeys),
		wish.WithIdleTimeout(30*time.Minute),
		wish.WithMiddleware(
			bm.Middleware(func(sess ssh.Session) (tea.Model, []tea.ProgramOption) {
				changes := st.Subscribe()
				go func() {
					<-sess.Context().Done()
					st.Unsubscribe(changes)
				}()
				return ui.New(ui.Deps{
					Config:   cfg(),
					Store:    st,
					Changes:  changes,
					Refresh:  refresh,
					ReadOnly: true,
					Version:  version,
				}), nil
			}),
			activeterm.Middleware(),
			logging.Middleware(),
		),
	)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.Serve(l); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
		return err
	}
	return nil
}
