// Package server hosts HTTP endpoints for gateway process observability.
package server

import (
	"context"
	"net/http"
)

// RouteHandler registers an additional HTTP handler on the server.
type RouteHandler struct {
	Pattern string
	Handler http.HandlerFunc
}

// Server wraps the HTTP server used by the gateway process.
type Server struct {
	httpServer *http.Server
}

// New creates an HTTP server instance with built-in health endpoint.
// Additional route handlers can be registered via the variadic parameter.
func New(address string, routes ...RouteHandler) *Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	for _, r := range routes {
		mux.HandleFunc(r.Pattern, r.Handler)
	}

	return &Server{
		httpServer: &http.Server{
			Addr:    address,
			Handler: mux,
		},
	}
}

// Start runs the HTTP server and blocks until it stops.
func (s *Server) Start() error {
	err := s.httpServer.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}
