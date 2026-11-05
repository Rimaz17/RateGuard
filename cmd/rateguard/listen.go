package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"
)

// endpoint is one listener and the server that serves it.
type endpoint struct {
	ln  net.Listener
	srv *http.Server
}

// listenTCP listens on a TCP address before anything is served, so a
// taken port fails startup rather than a goroutine.
func listenTCP(ctx context.Context, addr string) (net.Listener, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listening on %s: %w", addr, err)
	}
	return ln, nil
}

// staleSocketProbe bounds the check that a leftover socket is dead.
const staleSocketProbe = time.Second

// listenUnix listens on a Unix socket at path with the given permission
// (ADR 0017). A leftover socket that nothing answers on, as a crash leaves
// behind, is replaced; a live one, or a file that is not a socket, is never
// touched. Closing the listener removes the file.
func listenUnix(ctx context.Context, path string, mode fs.FileMode) (net.Listener, error) {
	if err := removeStaleSocket(ctx, path); err != nil {
		return nil, err
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("listening on unix socket %s: %w", path, err)
	}
	// Until this runs, the socket has the mode the umask gives it, which
	// for the usual 022 already denies connecting to others.
	if err := os.Chmod(path, mode); err != nil {
		_ = ln.Close() // the chmod error is the one worth reporting
		return nil, fmt.Errorf("setting the mode of unix socket %s: %w", path, err)
	}
	return ln, nil
}

func removeStaleSocket(ctx context.Context, path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking unix socket path %s: %w", path, err)
	}
	if fi.Mode().Type() != fs.ModeSocket {
		return fmt.Errorf("%s exists and is not a socket; refusing to replace it", path)
	}
	probeCtx, cancel := context.WithTimeout(ctx, staleSocketProbe)
	defer cancel()
	var d net.Dialer
	if conn, err := d.DialContext(probeCtx, "unix", path); err == nil {
		_ = conn.Close() // it answered, which is all the probe asked
		return fmt.Errorf("unix socket %s is in use by another process", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("removing stale unix socket %s: %w", path, err)
	}
	return nil
}

// serveAll serves every endpoint until ctx ends or one of them fails, then
// drains them all, giving in-flight requests up to shutdownTimeout.
func serveAll(ctx context.Context, logger *slog.Logger, shutdownTimeout time.Duration, endpoints []endpoint) error {
	served := make(chan error, len(endpoints))
	for _, ep := range endpoints {
		logger.Info("http server listening",
			slog.String("network", ep.ln.Addr().Network()),
			slog.String("addr", ep.ln.Addr().String()))
		go func() { served <- ep.srv.Serve(ep.ln) }()
	}

	running := len(endpoints)
	var errs []error
	select {
	case err := <-served:
		running--
		errs = append(errs, fmt.Errorf("http server: %w", err))
	case <-ctx.Done():
	}

	logger.Info("shutting down", slog.String("timeout", shutdownTimeout.String()))
	// The signal context may already be cancelled, so the drain deadline
	// keeps its values but not its cancellation.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	for _, ep := range endpoints {
		if err := ep.srv.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, fmt.Errorf("shutting down %s server: %w", ep.ln.Addr().Network(), err))
		}
	}
	for range running {
		if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs = append(errs, fmt.Errorf("http server: %w", err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	logger.Info("stopped")
	return nil
}
