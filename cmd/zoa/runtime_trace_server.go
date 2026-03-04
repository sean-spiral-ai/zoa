package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"zoa/internal/tracecontrol"
)

func startRuntimeTraceControlServer(addr string, logger *slog.Logger) (*http.Server, string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, "", nil
	}
	if logger == nil {
		logger = slog.Default()
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", fmt.Errorf("listen trace control server on %s: %w", addr, err)
	}
	manager := tracecontrol.NewManager()
	server := &http.Server{
		Handler:           tracecontrol.NewHTTPHandler(manager, logger.With("component", "trace_control")),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
			logger.Error("trace control server stopped", "error", serveErr)
		}
	}()
	return server, listenerBaseURL(listener.Addr()), nil
}

func stopRuntimeTraceControlServer(server *http.Server) error {
	if server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(ctx)
}

func listenerBaseURL(addr net.Addr) string {
	host := "127.0.0.1"
	port := "3008"
	if tcpAddr, ok := addr.(*net.TCPAddr); ok {
		port = fmt.Sprintf("%d", tcpAddr.Port)
	}
	return "http://" + net.JoinHostPort(host, port)
}
