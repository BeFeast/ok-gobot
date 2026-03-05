package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"

	controlserver "ok-gobot/internal/control"
)

// wsConn wraps a raw WebSocket connection.
type wsConn struct {
	conn net.Conn
}

// dialWS establishes a WebSocket connection to the control server.
func dialWS(addr string) (*wsConn, error) {
	url := fmt.Sprintf("ws://%s/ws", addr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, _, err := ws.DefaultDialer.Dial(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("could not connect to ok-gobot server at %s — is it running? (%w)", addr, err)
	}
	return &wsConn{conn: conn}, nil
}

// send serialises and sends a ClientMsg to the server.
func (c *wsConn) send(msg controlserver.ClientMsg) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return wsutil.WriteClientText(c.conn, data)
}

// readMsg reads and deserialises the next ServerMsg.
func (c *wsConn) readMsg() (controlserver.ServerMsg, error) {
	data, _, err := wsutil.ReadServerData(c.conn)
	if err != nil {
		return controlserver.ServerMsg{}, err
	}
	var msg controlserver.ServerMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("[tui] bad server message: %v", err)
		return controlserver.ServerMsg{}, err
	}
	return msg, nil
}

// close closes the WebSocket connection.
func (c *wsConn) close() {
	c.conn.Close()
}
