package server

import (
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/lnsp/oxidize/internal/oxide"
	"github.com/lnsp/oxidize/internal/wsutil"
)

// handleSerialHistory answers GET /v1/instances/{instance}/serial-console with
// an empty buffer. Proxmox keeps no replayable history for the serial console,
// so live output arrives only over the websocket stream.
func (s *Server) handleSerialHistory(w http.ResponseWriter, r *http.Request) {
	oxide.WriteJSON(w, http.StatusOK, map[string]any{
		"data":             "",
		"last_byte_offset": 0,
	})
}

// handleSerialStream bridges the Console's serial-console websocket to Proxmox's
// termproxy + vncwebsocket. It is not wrapped by protected() because it is a
// websocket upgrade; the session cookie is checked manually here.
func (s *Server) handleSerialStream(w http.ResponseWriter, r *http.Request) {
	if !s.validSession(r) {
		oxide.WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	ref, err := s.resolveInstance(r.Context(), r.PathValue("instance"))
	if err != nil || ref == nil {
		oxide.WriteError(w, http.StatusNotFound, "instance not found")
		return
	}

	client, err := wsutil.Accept(w, r)
	if err != nil {
		log.Printf("serial: accept failed: %v", err)
		return
	}
	defer client.Close()

	// Proxmox's serial terminal needs a serial device. Without one, termproxy
	// still returns a ticket but the terminal process emits "unable to find
	// serial interface". Add serial0 if missing (effective on next VM start) and
	// guide the user rather than showing the cryptic Proxmox error.
	if cfg, cerr := s.pve.QemuConfig(r.Context(), ref.node, ref.vmid); cerr == nil && !hasSerial(cfg) {
		form := url.Values{}
		form.Set("serial0", "socket")
		_, addErr := s.pve.UpdateConfig(r.Context(), ref.node, ref.vmid, form)
		msg := "\r\nThis VM has no serial console device (serial0).\r\n"
		if addErr == nil {
			msg += "A serial port was added for you. Stop and start the VM, then reconnect.\r\n" +
				"The guest OS must also expose a console on ttyS0 — cloud-init images do\r\n" +
				"this automatically; VGA-only guests will not produce serial output.\r\n"
		} else {
			msg += fmt.Sprintf("Could not add one automatically: %v\r\n", addErr)
		}
		writeTerm(client, msg)
		return
	}

	// Open a termproxy session on Proxmox.
	tp, err := s.pve.TermProxy(r.Context(), ref.node, ref.vmid)
	if err != nil {
		writeTerm(client, fmt.Sprintf("\r\nfailed to open serial console: %v\r\n", err))
		return
	}

	wsURL := fmt.Sprintf("%s/api2/json/nodes/%s/qemu/%d/vncwebsocket?port=%d&vncticket=%s",
		wsScheme(s.pve.Base()), ref.node, ref.vmid, tp.Port, url.QueryEscape(tp.Ticket))

	header := http.Header{}
	header.Set("Authorization", s.pve.Token())
	header.Set("Sec-WebSocket-Protocol", "binary")

	upstream, err := wsutil.Dial(wsURL, header, &tls.Config{InsecureSkipVerify: s.cfg.InsecureSkipVerify})
	if err != nil {
		writeTerm(client, fmt.Sprintf("\r\nfailed to connect to Proxmox console: %v\r\n", err))
		return
	}
	defer upstream.Close()

	// Proxmox termproxy expects the auth string "<user>:<ticket>\n" first,
	// then an initial terminal size as "1:<cols>:<rows>:".
	if err := upstream.WriteMessage(wsutil.OpBinary, []byte(tp.User+":"+tp.Ticket+"\n")); err != nil {
		writeTerm(client, "\r\nfailed to authenticate to Proxmox console\r\n")
		return
	}
	_ = upstream.WriteMessage(wsutil.OpBinary, []byte("1:80:24:"))

	bridge(client, upstream)
}

// hasSerial reports whether a VM config has any serialN device.
func hasSerial(cfg map[string]string) bool {
	for k := range cfg {
		if strings.HasPrefix(k, "serial") && len(k) > 6 {
			return true
		}
	}
	return false
}

// bridge connects the browser xterm to the Proxmox terminal. Proxmox sends raw
// PTY output, but expects INPUT framed as "0:<byte-len>:<data>" (and resizes as
// "1:<cols>:<rows>:"). The Oxide console speaks raw serial bytes, so we pass
// Proxmox output straight through and frame the browser's keystrokes.
func bridge(client, upstream *wsutil.Conn) {
	done := make(chan struct{}, 2)
	go pumpRaw(upstream, client, done)    // proxmox -> browser (raw)
	go pumpFramed(client, upstream, done) // browser -> proxmox (framed input)
	<-done
}

// pumpRaw forwards messages unchanged.
func pumpRaw(src, dst *wsutil.Conn, done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	for {
		op, data, err := src.ReadMessage()
		if len(data) > 0 {
			if werr := dst.WriteMessage(wsutil.OpBinary, data); werr != nil {
				return
			}
		}
		if err == io.EOF || op == wsutil.OpClose || err != nil {
			return
		}
	}
}

// pumpFramed wraps each client message in Proxmox's "0:<len>:<data>" input frame.
func pumpFramed(src, dst *wsutil.Conn, done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	for {
		op, data, err := src.ReadMessage()
		if len(data) > 0 {
			frame := append([]byte(fmt.Sprintf("0:%d:", len(data))), data...)
			if werr := dst.WriteMessage(wsutil.OpBinary, frame); werr != nil {
				return
			}
		}
		if err == io.EOF || op == wsutil.OpClose || err != nil {
			return
		}
	}
}

func writeTerm(c *wsutil.Conn, msg string) {
	_ = c.WriteMessage(wsutil.OpBinary, []byte(msg))
}

// wsScheme converts an http(s) base URL to the matching ws(s) scheme.
func wsScheme(base string) string {
	if len(base) >= 6 && base[:6] == "https:" {
		return "wss:" + base[6:]
	}
	if len(base) >= 5 && base[:5] == "http:" {
		return "ws:" + base[5:]
	}
	return base
}
