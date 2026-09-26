// Command gateway sits between Simulated Hospital and the integration engine.
//
// Simulated Hospital models one hospital and sends every message type down a
// single MLLP connection. Real hospitals don't work that way: registration
// (ADT), order entry (ORM), the lab system (ORU) and transcription (MDM) each
// have their own interface. The gateway accepts the combined stream, ACKs it,
// archives every message, and fans it out to one MLLP connection per feed.
//
// Each feed has its own FIFO queue and worker, so ordering is preserved within
// a feed but NOT across feeds -- exactly like production. An ORU can reach the
// engine before the ADT^A01 for the same patient, and the engine has to cope.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type config struct {
	listenAddr string
	httpAddr   string
	archiveDir string
	routes     map[string]string // message type -> host:port
	queueSize  int
	ackTimeout time.Duration
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func loadConfig() (config, error) {
	c := config{
		listenAddr: env("LISTEN_ADDR", ":2575"),
		httpAddr:   env("HTTP_ADDR", ":8081"),
		archiveDir: env("ARCHIVE_DIR", "/archive"),
		routes:     map[string]string{},
	}
	var err error
	if c.queueSize, err = strconv.Atoi(env("QUEUE_SIZE", "10000")); err != nil {
		return c, fmt.Errorf("QUEUE_SIZE: %w", err)
	}
	if c.ackTimeout, err = time.ParseDuration(env("ACK_TIMEOUT", "30s")); err != nil {
		return c, fmt.Errorf("ACK_TIMEOUT: %w", err)
	}
	// ROUTES=ADT=engine:6661,ORU=engine:6663
	for _, r := range strings.Split(env("ROUTES", ""), ",") {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		k, v, ok := strings.Cut(r, "=")
		if !ok || k == "" || v == "" {
			return c, fmt.Errorf("bad route %q, want TYPE=host:port", r)
		}
		c.routes[strings.ToUpper(k)] = v
	}
	if len(c.routes) == 0 {
		return c, errors.New("ROUTES is empty")
	}
	return c, nil
}

// feed is one outbound interface: a queue plus a worker holding an MLLP connection.
type feed struct {
	name  string
	dest  string
	queue chan []byte

	received  atomic.Int64
	delivered atomic.Int64
	rejected  atomic.Int64 // engine answered AE/AR
	retries   atomic.Int64
	connected atomic.Bool

	mu      sync.Mutex
	lastErr string
}

func (f *feed) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		f.lastErr = ""
	} else {
		f.lastErr = err.Error()
	}
}

// run delivers queued messages in order. A message is retried until the
// engine returns any ACK; AE/AR are logged and counted, then the feed moves
// on so one bad message can't stall the interface.
func (f *feed) run(ctx context.Context, ackTimeout time.Duration, log *slog.Logger) {
	var conn net.Conn
	var reader *bufio.Reader
	closeConn := func() {
		if conn != nil {
			conn.Close()
			conn = nil
		}
		f.connected.Store(false)
	}
	defer closeConn()

	for {
		var msg []byte
		select {
		case <-ctx.Done():
			return
		case msg = <-f.queue:
		}
		backoff := time.Second
		for {
			if conn == nil {
				c, err := net.DialTimeout("tcp", f.dest, 5*time.Second)
				if err != nil {
					f.setErr(err)
					log.Warn("connect failed, will retry", "feed", f.name, "dest", f.dest, "err", err, "in", backoff)
					if !sleep(ctx, backoff) {
						return
					}
					backoff = min(backoff*2, 30*time.Second)
					continue
				}
				conn, reader = c, bufio.NewReader(c)
				f.connected.Store(true)
				log.Info("connected", "feed", f.name, "dest", f.dest)
			}
			_ = conn.SetDeadline(time.Now().Add(ackTimeout))
			err := writeFrame(conn, msg)
			var ack []byte
			if err == nil {
				ack, err = readFrame(reader)
			}
			if err != nil {
				f.retries.Add(1)
				f.setErr(err)
				log.Warn("send failed, reconnecting", "feed", f.name, "err", err, "in", backoff)
				closeConn()
				if !sleep(ctx, backoff) {
					return
				}
				backoff = min(backoff*2, 30*time.Second)
				continue
			}
			f.setErr(nil)
			switch code := ackCode(ack); code {
			case "AA", "CA":
				f.delivered.Add(1)
			default:
				f.rejected.Add(1)
				h, _ := parseHeader(msg)
				log.Error("engine rejected message", "feed", f.name, "ack", code, "control_id", h.controlID)
			}
			break
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// archive appends each message to <dir>/<feed>/<yyyy-mm-dd>.hl7, one blank
// line between messages. These files are the input half of the golden corpus.
type archive struct {
	dir string
	mu  sync.Mutex
}

func (a *archive) write(feedName string, msg []byte, now time.Time) error {
	if a.dir == "" {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	d := filepath.Join(a.dir, strings.ToLower(feedName))
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	fh, err := os.OpenFile(filepath.Join(d, now.Format("2006-01-02")+".hl7"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer fh.Close()
	_, err = fmt.Fprintf(fh, "%s\n\n", readable(msg))
	return err
}

type gateway struct {
	cfg     config
	feeds   map[string]*feed
	archive *archive
	log     *slog.Logger
}

func newGateway(cfg config, log *slog.Logger) *gateway {
	g := &gateway{cfg: cfg, feeds: map[string]*feed{}, archive: &archive{dir: cfg.archiveDir}, log: log}
	for typ, dest := range cfg.routes {
		g.feeds[typ] = &feed{name: typ, dest: dest, queue: make(chan []byte, cfg.queueSize)}
	}
	return g
}

// handleConn serves one inbound MLLP connection (Simulated Hospital keeps one open).
func (g *gateway) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	g.log.Info("sender connected", "remote", conn.RemoteAddr())
	r := bufio.NewReader(conn)
	for {
		msg, err := readFrame(r)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				g.log.Warn("read failed", "remote", conn.RemoteAddr(), "err", err)
			}
			return
		}
		ack := g.accept(ctx, msg)
		if err := writeFrame(conn, ack); err != nil {
			g.log.Warn("ack write failed", "err", err)
			return
		}
	}
}

// accept archives and enqueues a message, returning the ACK for the sender.
// If the feed's queue is full this blocks, which delays the ACK and pushes
// back on the sender -- the same thing a real engine does under load.
func (g *gateway) accept(ctx context.Context, msg []byte) []byte {
	now := time.Now()
	h, err := parseHeader(msg)
	if err != nil {
		g.log.Error("unparseable message", "err", err)
		_ = g.archive.write("unparseable", msg, now)
		return buildACK(header{}, "AR", "unparseable message", now)
	}
	f, ok := g.feeds[h.messageType]
	if !ok {
		g.log.Warn("no route for message type", "type", h.messageType, "control_id", h.controlID)
		_ = g.archive.write("unrouted", msg, now)
		return buildACK(h, "AR", "no route for "+h.messageType, now)
	}
	if err := g.archive.write(f.name, msg, now); err != nil {
		g.log.Error("archive write failed", "err", err)
	}
	select {
	case f.queue <- msg:
		f.received.Add(1)
		return buildACK(h, "AA", "", now)
	case <-ctx.Done():
		return buildACK(h, "AE", "gateway shutting down", now)
	}
}

type feedStats struct {
	Destination string `json:"destination"`
	Connected   bool   `json:"connected"`
	Queued      int    `json:"queued"`
	Received    int64  `json:"received"`
	Delivered   int64  `json:"delivered"`
	Rejected    int64  `json:"rejected"`
	Retries     int64  `json:"retries"`
	LastError   string `json:"last_error,omitempty"`
}

func (g *gateway) stats() map[string]feedStats {
	out := map[string]feedStats{}
	for name, f := range g.feeds {
		f.mu.Lock()
		le := f.lastErr
		f.mu.Unlock()
		out[name] = feedStats{
			Destination: f.dest,
			Connected:   f.connected.Load(),
			Queued:      len(f.queue),
			Received:    f.received.Load(),
			Delivered:   f.delivered.Load(),
			Rejected:    f.rejected.Load(),
			Retries:     f.retries.Load(),
			LastError:   le,
		}
	}
	return out
}

func (g *gateway) httpHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/stats", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.Encode(g.stats())
	})
	return mux
}

func (g *gateway) serve(ctx context.Context, ln net.Listener) {
	for name, f := range g.feeds {
		g.log.Info("feed configured", "type", name, "dest", f.dest)
		go f.run(ctx, g.cfg.ackTimeout, g.log)
	}
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			g.log.Warn("accept failed", "err", err)
			continue
		}
		go g.handleConn(ctx, conn)
	}
}

// healthcheck lets the distroless image (no curl, no shell) check itself:
// the compose healthcheck runs `/gateway -healthcheck`.
func healthcheck(addr string) int {
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get("http://" + addr + "/healthz")
	if err != nil || resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func main() {
	hc := flag.Bool("healthcheck", false, "probe the local /healthz endpoint and exit")
	flag.Parse()
	if *hc {
		os.Exit(healthcheck(env("HTTP_ADDR", ":8081")))
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cfg, err := loadConfig()
	if err != nil {
		log.Error("bad configuration", "err", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	g := newGateway(cfg, log)
	ln, err := net.Listen("tcp", cfg.listenAddr)
	if err != nil {
		log.Error("listen failed", "err", err)
		os.Exit(1)
	}
	srv := &http.Server{Addr: cfg.httpAddr, Handler: g.httpHandler()}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server failed", "err", err)
		}
	}()

	types := make([]string, 0, len(cfg.routes))
	for t := range cfg.routes {
		types = append(types, t)
	}
	sort.Strings(types)
	log.Info("gateway listening", "mllp", cfg.listenAddr, "http", cfg.httpAddr, "feeds", strings.Join(types, ","), "archive", cfg.archiveDir)

	g.serve(ctx, ln)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
	log.Info("gateway stopped")
}
