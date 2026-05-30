package server

import (
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"

	"github.com/lennart/oxidize/internal/oxide"
	"github.com/lennart/oxidize/internal/wsutil"
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

	// Proxmox termproxy expects the auth string "<user>:<ticket>\n" first.
	if err := upstream.WriteMessage(wsutil.OpBinary, []byte(tp.User+":"+tp.Ticket+"\n")); err != nil {
		writeTerm(client, "\r\nfailed to authenticate to Proxmox console\r\n")
		return
	}

	bridge(client, upstream)
}

// bridge pumps bytes in both directions until either side closes.
func bridge(a, b *wsutil.Conn) {
	done := make(chan struct{}, 2)
	go pump(a, b, done) // browser -> proxmox
	go pump(b, a, done) // proxmox -> browser
	<-done
}

func pump(src, dst *wsutil.Conn, done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	for {
		op, data, err := src.ReadMessage()
		if len(data) > 0 {
			if werr := dst.WriteMessage(wsutil.OpBinary, data); werr != nil {
				return
			}
		}
		if err == io.EOF || op == wsutil.OpClose {
			return
		}
		if err != nil {
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
