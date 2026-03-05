package tui

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws"
)

func TestDialWSUsesConnectionTimeout(t *testing.T) {
	originalDial := wsDialContext
	t.Cleanup(func() {
		wsDialContext = originalDial
	})

	wsDialContext = func(ctx context.Context, url string) (net.Conn, *bufio.Reader, ws.Handshake, error) {
		if url != "ws://127.0.0.1:8787/ws" {
			t.Fatalf("dialed unexpected url: %s", url)
		}

		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("expected dial context deadline")
		}

		remaining := time.Until(deadline)
		if remaining < 4*time.Second || remaining > 6*time.Second {
			t.Fatalf("expected deadline around %s, got %s", dialWSTimeout, remaining)
		}

		return nil, nil, ws.Handshake{}, errors.New("dial failed")
	}

	_, err := dialWS("127.0.0.1:8787")
	if err == nil {
		t.Fatal("expected dialWS to return an error")
	}
}

func TestRunReturnsFriendlyConnectionError(t *testing.T) {
	originalDial := wsDialContext
	t.Cleanup(func() {
		wsDialContext = originalDial
	})

	wsDialContext = func(context.Context, string) (net.Conn, *bufio.Reader, ws.Handshake, error) {
		return nil, nil, ws.Handshake{}, errors.New("connection refused")
	}

	err := Run(Options{ServerAddr: "127.0.0.1:8787"})
	if err == nil {
		t.Fatal("expected Run to fail when dial fails")
	}

	want := "Could not connect to ok-gobot server at 127.0.0.1:8787 — is it running?"
	if err.Error() != want {
		t.Fatalf("unexpected error message:\nwant: %q\ngot:  %q", want, err.Error())
	}
	if strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("expected friendly error without raw transport details, got: %q", err.Error())
	}
}
