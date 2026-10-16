package splitkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const maxMessageBytes = 1 << 20

// connectOnce dials the Split Kit socket for guid and reads until the
// connection fails. handshook is true once the Engine.IO session was opened.
func (w *Watcher) connectOnce(ctx context.Context, guid string) (handshook bool, err error) {
	rawURL, err := w.socketURL(guid)
	if err != nil {
		return false, err
	}
	conn, _, err := websocket.Dial(ctx, rawURL, nil)
	if err != nil {
		return false, err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxMessageBytes)

	extra, err := readOpen(ctx, conn)
	if err != nil {
		return false, err
	}
	if err := writeText(ctx, conn, "40/event,"); err != nil {
		return false, err
	}
	handshook = true
	for _, pkt := range extra {
		if err := w.handlePacket(ctx, conn, guid, pkt); err != nil {
			return true, err
		}
	}
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return true, err
		}
		for _, pkt := range splitPackets(string(data)) {
			if err := w.handlePacket(ctx, conn, guid, pkt); err != nil {
				return true, err
			}
		}
	}
}

func (w *Watcher) socketURL(guid string) (string, error) {
	return socketURL(w.host, guid)
}

func socketURL(host, guid string) (string, error) {
	u, err := url.Parse(host)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	case "wss", "ws":
	default:
		return "", fmt.Errorf("unsupported split kit scheme %q", u.Scheme)
	}
	u.Path = "/socket.io/"
	q := url.Values{}
	q.Set("EIO", "4")
	q.Set("transport", "websocket")
	q.Set("event_id", guid)
	u.RawQuery = q.Encode()
	u.Fragment = ""
	return u.String(), nil
}

func (w *Watcher) handlePacket(ctx context.Context, conn *websocket.Conn, guid, pkt string) error {
	switch {
	case pkt == "":
		return nil
	case pkt == "2":
		return writeText(ctx, conn, "3")
	case pkt == "3":
		return nil
	case strings.HasPrefix(pkt, "41"):
		return fmt.Errorf("namespace disconnected")
	case strings.HasPrefix(pkt, "44"):
		return fmt.Errorf("namespace connect error: %s", truncate(pkt))
	case strings.HasPrefix(pkt, "42"):
		return w.handleEvent(ctx, guid, pkt)
	default:
		return nil
	}
}

func (w *Watcher) handleEvent(ctx context.Context, guid, pkt string) error {
	name, data, err := parseEvent(pkt)
	if err != nil {
		slog.Warn("split kit event", "event", guid, "err", err)
		return nil
	}
	if name != "remoteValue" {
		return nil
	}
	var block Block
	if len(data) > 0 && string(data) != "null" {
		if err := json.Unmarshal(data, &block); err != nil {
			slog.Warn("split kit remoteValue", "event", guid, "err", err)
			return nil
		}
	}
	if block.BlockGUID != "" {
		w.set(guid, block)
		return nil
	}
	w.seed(ctx, guid)
	return nil
}

func readOpen(ctx context.Context, conn *websocket.Conn) ([]string, error) {
	_, data, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	parts := splitPackets(string(data))
	if len(parts) == 0 || !strings.HasPrefix(parts[0], "0") {
		return nil, fmt.Errorf("expected engine.io open packet, got %q", truncate(string(data)))
	}
	var extra []string
	for _, p := range parts[1:] {
		if p != "" {
			extra = append(extra, p)
		}
	}
	return extra, nil
}

func writeText(ctx context.Context, conn *websocket.Conn, msg string) error {
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, []byte(msg))
}

func splitPackets(s string) []string {
	return strings.Split(s, "\x1e")
}

func parseEvent(pkt string) (name string, data json.RawMessage, err error) {
	if !strings.HasPrefix(pkt, "42") {
		return "", nil, fmt.Errorf("not an event packet")
	}
	rest := pkt[2:]
	if strings.HasPrefix(rest, "/") {
		i := strings.IndexByte(rest, ',')
		if i < 0 {
			return "", nil, fmt.Errorf("event missing payload")
		}
		rest = rest[i+1:]
	}
	for len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9' {
		rest = rest[1:]
	}
	if len(rest) == 0 || rest[0] != '[' {
		return "", nil, fmt.Errorf("event payload is not an array")
	}
	var payload []json.RawMessage
	if err := json.Unmarshal([]byte(rest), &payload); err != nil {
		return "", nil, err
	}
	if len(payload) == 0 {
		return "", nil, fmt.Errorf("empty event")
	}
	if err := json.Unmarshal(payload[0], &name); err != nil {
		return "", nil, err
	}
	if len(payload) > 1 {
		data = payload[1]
	}
	return name, data, nil
}

func truncate(s string) string {
	if len(s) > 180 {
		return s[:180] + "..."
	}
	return s
}

// FetchCurrent dials the Split Kit socket and returns the first remoteValue
// that carries a blockGuid. An empty payload or a wait timeout falls back to
// the first block from getblocks.
func FetchCurrent(ctx context.Context, host, guid string, wait time.Duration) (Block, bool) {
	host = strings.TrimRight(strings.TrimSpace(host), "/")
	guid = strings.TrimSpace(guid)
	if host == "" || guid == "" {
		return Block{}, false
	}
	if wait <= 0 {
		wait = 3 * time.Second
	}
	sockCtx, cancel := context.WithTimeout(ctx, wait)
	block, empty, err := readRemoteValue(sockCtx, host, guid)
	timedOut := errors.Is(sockCtx.Err(), context.DeadlineExceeded)
	cancel()
	if err == nil && !empty && block.BlockGUID != "" {
		return block, true
	}
	if ctx.Err() != nil && !timedOut {
		return Block{}, false
	}
	return loadFirstBlock(ctx, host, guid)
}

func readRemoteValue(ctx context.Context, host, guid string) (Block, bool, error) {
	rawURL, err := socketURL(host, guid)
	if err != nil {
		return Block{}, false, err
	}
	conn, _, err := websocket.Dial(ctx, rawURL, nil)
	if err != nil {
		return Block{}, false, err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxMessageBytes)

	extra, err := readOpen(ctx, conn)
	if err != nil {
		return Block{}, false, err
	}
	if err := writeText(ctx, conn, "40/event,"); err != nil {
		return Block{}, false, err
	}
	var (
		found Block
		empty bool
	)
	consume := func(pkt string) (bool, error) {
		switch {
		case pkt == "" || pkt == "3":
			return false, nil
		case pkt == "2":
			return false, writeText(ctx, conn, "3")
		case strings.HasPrefix(pkt, "41"):
			return false, fmt.Errorf("namespace disconnected")
		case strings.HasPrefix(pkt, "44"):
			return false, fmt.Errorf("namespace connect error: %s", truncate(pkt))
		case strings.HasPrefix(pkt, "42"):
			name, data, err := parseEvent(pkt)
			if err != nil || name != "remoteValue" {
				return false, nil
			}
			var block Block
			if len(data) > 0 && string(data) != "null" {
				if err := json.Unmarshal(data, &block); err != nil {
					return false, nil
				}
			}
			if block.BlockGUID == "" {
				empty = true
				return true, nil
			}
			found = block
			return true, nil
		default:
			return false, nil
		}
	}
	for _, pkt := range extra {
		done, err := consume(pkt)
		if err != nil || done {
			return found, empty, err
		}
	}
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return Block{}, false, err
		}
		for _, pkt := range splitPackets(string(data)) {
			done, err := consume(pkt)
			if err != nil || done {
				return found, empty, err
			}
		}
	}
}

func loadFirstBlock(ctx context.Context, host, guid string) (Block, bool) {
	if ctx.Err() != nil {
		return Block{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	blocks, err := fetchBlocks(ctx, nil, host, guid)
	if err != nil || len(blocks) == 0 {
		return Block{}, false
	}
	return blocks[0], true
}
