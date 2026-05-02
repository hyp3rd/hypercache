package tests

import (
	"context"
	"net"
	"testing"
)

// AllocatePort returns a free TCP loopback address ("127.0.0.1:N") for tests
// that need to bind a server. Listening on :0 lets the kernel pick an unused
// port; we close immediately and return the address. Two tests calling this
// concurrently can in theory collide on the same port if the kernel reissues
// it before either test binds, but in practice the window is too short to
// matter for our serial-package test runs.
//
// Use this instead of hard-coded ports so that -shuffle and -count=N do not
// induce flake from port reuse across tests in the same process.
func AllocatePort(tb testing.TB) string {
	tb.Helper()

	var lc net.ListenConfig

	listener, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("allocate port: %v", err)
	}

	addr := listener.Addr().String()

	closeErr := listener.Close()
	if closeErr != nil {
		tb.Fatalf("close port listener: %v", closeErr)
	}

	return addr
}
