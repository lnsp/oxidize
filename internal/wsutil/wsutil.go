// Package wsutil is a minimal RFC 6455 WebSocket implementation (server accept
// + client dial) sufficient to bridge the Console's serial-console stream to
// Proxmox's termproxy/vncwebsocket. It supports text/binary data frames, ping/
// pong, and close. It is intentionally small rather than fully general.
package wsutil

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// Opcodes.
const (
	OpContinuation = 0x0
	OpText         = 0x1
	OpBinary       = 0x2
	OpClose        = 0x8
	OpPing         = 0x9
	OpPong         = 0xA
)

// Conn is a WebSocket connection.
type Conn struct {
	conn     net.Conn
	br       *bufio.Reader
	isClient bool // client connections must mask outgoing frames
}

// Accept performs the server-side handshake by hijacking the HTTP connection.
func Accept(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if !strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") ||
		!strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return nil, errors.New("not a websocket upgrade request")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, errors.New("missing Sec-WebSocket-Key")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("response writer does not support hijacking")
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	accept := acceptKey(key)
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n"
	// Echo a requested subprotocol if present (Proxmox uses "binary").
	if proto := r.Header.Get("Sec-WebSocket-Protocol"); proto != "" {
		first := strings.TrimSpace(strings.Split(proto, ",")[0])
		resp += "Sec-WebSocket-Protocol: " + first + "\r\n"
	}
	resp += "\r\n"
	if _, err := brw.WriteString(resp); err != nil {
		conn.Close()
		return nil, err
	}
	if err := brw.Flush(); err != nil {
		conn.Close()
		return nil, err
	}
	return &Conn{conn: conn, br: brw.Reader, isClient: false}, nil
}

// Dial opens a client WebSocket connection to a wss:// or ws:// URL.
func Dial(rawURL string, header http.Header, tlsCfg *tls.Config) (*Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	host := u.Host
	var conn net.Conn
	switch u.Scheme {
	case "wss":
		if !strings.Contains(host, ":") {
			host += ":443"
		}
		conn, err = tls.Dial("tcp", host, tlsCfg)
	case "ws":
		if !strings.Contains(host, ":") {
			host += ":80"
		}
		conn, err = net.Dial("tcp", host)
	default:
		return nil, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	if err != nil {
		return nil, err
	}

	key := base64.StdEncoding.EncodeToString(randKey())
	path := u.RequestURI()
	var b strings.Builder
	fmt.Fprintf(&b, "GET %s HTTP/1.1\r\n", path)
	fmt.Fprintf(&b, "Host: %s\r\n", u.Host)
	b.WriteString("Upgrade: websocket\r\n")
	b.WriteString("Connection: Upgrade\r\n")
	fmt.Fprintf(&b, "Sec-WebSocket-Key: %s\r\n", key)
	b.WriteString("Sec-WebSocket-Version: 13\r\n")
	for k, vs := range header {
		for _, v := range vs {
			fmt.Fprintf(&b, "%s: %s\r\n", k, v)
		}
	}
	b.WriteString("\r\n")
	if _, err := conn.Write([]byte(b.String())); err != nil {
		conn.Close()
		return nil, err
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return nil, fmt.Errorf("websocket dial: unexpected status %d", resp.StatusCode)
	}
	return &Conn{conn: conn, br: br, isClient: true}, nil
}

// ReadMessage reads a single data message, transparently answering pings and
// returning io.EOF on close. Fragmented messages are reassembled.
func (c *Conn) ReadMessage() (opcode int, payload []byte, err error) {
	var msg []byte
	var msgOp int
	for {
		fin, op, data, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch op {
		case OpPing:
			_ = c.writeFrame(OpPong, data)
			continue
		case OpPong:
			continue
		case OpClose:
			return OpClose, data, io.EOF
		case OpContinuation:
			msg = append(msg, data...)
		default:
			msgOp = op
			msg = append(msg, data...)
		}
		if fin {
			return msgOp, msg, nil
		}
	}
}

// WriteMessage writes a full data message.
func (c *Conn) WriteMessage(opcode int, data []byte) error {
	return c.writeFrame(opcode, data)
}

// Close sends a close frame and closes the underlying connection.
func (c *Conn) Close() error {
	_ = c.writeFrame(OpClose, nil)
	return c.conn.Close()
}

func (c *Conn) readFrame() (fin bool, opcode int, payload []byte, err error) {
	h := make([]byte, 2)
	if _, err = io.ReadFull(c.br, h); err != nil {
		return
	}
	fin = h[0]&0x80 != 0
	opcode = int(h[0] & 0x0f)
	masked := h[1]&0x80 != 0
	length := int64(h[1] & 0x7f)

	switch length {
	case 126:
		ext := make([]byte, 2)
		if _, err = io.ReadFull(c.br, ext); err != nil {
			return
		}
		length = int64(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err = io.ReadFull(c.br, ext); err != nil {
			return
		}
		length = int64(binary.BigEndian.Uint64(ext))
	}

	var maskKey []byte
	if masked {
		maskKey = make([]byte, 4)
		if _, err = io.ReadFull(c.br, maskKey); err != nil {
			return
		}
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return fin, opcode, payload, nil
}

func (c *Conn) writeFrame(opcode int, payload []byte) error {
	var hdr []byte
	b0 := byte(0x80 | opcode) // FIN set
	hdr = append(hdr, b0)

	maskBit := byte(0)
	if c.isClient {
		maskBit = 0x80
	}
	n := len(payload)
	switch {
	case n < 126:
		hdr = append(hdr, maskBit|byte(n))
	case n < 65536:
		hdr = append(hdr, maskBit|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, maskBit|127)
		ext := make([]byte, 8)
		binary.BigEndian.PutUint64(ext, uint64(n))
		hdr = append(hdr, ext...)
	}

	if c.isClient {
		key := randKey()[:4]
		hdr = append(hdr, key...)
		masked := make([]byte, n)
		for i := 0; i < n; i++ {
			masked[i] = payload[i] ^ key[i%4]
		}
		if _, err := c.conn.Write(hdr); err != nil {
			return err
		}
		_, err := c.conn.Write(masked)
		return err
	}

	if _, err := c.conn.Write(hdr); err != nil {
		return err
	}
	if n > 0 {
		_, err := c.conn.Write(payload)
		return err
	}
	return nil
}

func randKey() []byte {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return b
}

func acceptKey(key string) string {
	h := sha1.New()
	h.Write([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}
