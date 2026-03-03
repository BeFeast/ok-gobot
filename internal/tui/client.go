package tui

import (
	"encoding/json"
	"fmt"
	"log"
	"net"

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
	conn, _, _, err := ws.DefaultDialer.Dial(nil, url)
	if err != nil {
		return nil, fmt.Errorf("dial control server %s: %w", url, err)
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
