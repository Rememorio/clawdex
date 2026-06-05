package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	l.Close()
	return addr
}

func TestHealthz(t *testing.T) {
	addr := freePort(t)
	srv := New(addr)

	go func() { _ = srv.Start() }()
	t.Cleanup(func() { srv.Shutdown(context.Background()) })

	// Wait for the server to be ready.
	var resp *http.Response
	for i := 0; i < 50; i++ {
		var err error
		resp, err = http.Get("http://" + addr + "/healthz")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NotNil(t, resp, "server did not start")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "ok", string(body))
}

func TestHealthz_404OnOtherPaths(t *testing.T) {
	addr := freePort(t)
	srv := New(addr)

	go func() { _ = srv.Start() }()
	t.Cleanup(func() { srv.Shutdown(context.Background()) })

	var resp *http.Response
	for i := 0; i < 50; i++ {
		var err error
		resp, err = http.Get("http://" + addr + "/nonexistent")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NotNil(t, resp)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestShutdown(t *testing.T) {
	addr := freePort(t)
	srv := New(addr)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start() }()

	// Wait for it to be up
	for i := 0; i < 50; i++ {
		if _, err := http.Get("http://" + addr + "/healthz"); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, srv.Shutdown(ctx))

	// Start should have returned nil (ErrServerClosed is swallowed)
	err := <-errCh
	assert.NoError(t, err)
}
