package control_test

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/gobwas/ws"
)

// freePort returns a free TCP port on the loopback interface.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// wsConnect dials the control server and upgrades to WebSocket.
func wsConnect(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, _, _, err := ws.Dial(context.Background(), "ws://"+addr+"/ws")
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func wsURL(addr string) string {
	return fmt.Sprintf("ws://%s/ws", addr)
}
