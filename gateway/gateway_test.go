package main

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeEngine is an MLLP listener that records what it receives and ACKs it.
type fakeEngine struct {
	ln   net.Listener
	mu   sync.Mutex
	msgs []string
	code string
}

func startFakeEngine(t *testing.T, addr, code string) *fakeEngine {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	e := &fakeEngine{ln: ln, code: code}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					msg, err := readFrame(r)
					if err != nil {
						return
					}
					e.mu.Lock()
					e.msgs = append(e.msgs, string(msg))
					e.mu.Unlock()
					h, _ := parseHeader(msg)
					writeFrame(c, buildACK(h, e.code, "", time.Now()))
				}
			}(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return e
}

func (e *fakeEngine) received() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.msgs...)
}

func msg(typ, trigger, id string) []byte {
	return []byte("MSH|^~\\&|SIMHOSP|SFAC|RAPP|RFAC|20260923120000||" + typ + "^" + trigger + "|" + id + "|T|2.3\rPID|1||MRN" + id + "\r")
}

func send(t *testing.T, conn net.Conn, r *bufio.Reader, m []byte) string {
	t.Helper()
	if err := writeFrame(conn, m); err != nil {
		t.Fatal(err)
	}
	ack, err := readFrame(r)
	if err != nil {
		t.Fatal(err)
	}
	return ackCode(ack)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestRoutingArchiveAndOrdering(t *testing.T) {
	adt := startFakeEngine(t, "127.0.0.1:0", "AA")
	oru := startFakeEngine(t, "127.0.0.1:0", "AE")
	dir := t.TempDir()

	cfg := config{
		archiveDir: dir,
		routes:     map[string]string{"ADT": adt.ln.Addr().String(), "ORU": oru.ln.Addr().String()},
		queueSize:  10,
		ackTimeout: 2 * time.Second,
	}
	g := newGateway(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.serve(ctx, ln)

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := bufio.NewReader(conn)

	for _, m := range [][]byte{msg("ADT", "A01", "1"), msg("ORU", "R01", "2"), msg("ADT", "A03", "3")} {
		if code := send(t, conn, r, m); code != "AA" {
			t.Fatalf("gateway ACK = %s, want AA", code)
		}
	}
	if code := send(t, conn, r, msg("SIU", "S12", "4")); code != "AR" {
		t.Fatalf("unrouted ACK = %s, want AR", code)
	}

	waitFor(t, func() bool { return len(adt.received()) == 2 && len(oru.received()) == 1 })
	got := adt.received()
	if !strings.Contains(got[0], "ADT^A01") || !strings.Contains(got[1], "ADT^A03") {
		t.Fatalf("ADT feed out of order: %q", got)
	}
	waitFor(t, func() bool { return g.stats()["ORU"].Rejected == 1 })

	day := time.Now().Format("2006-01-02") + ".hl7"
	for _, sub := range []string{"adt", "oru", "unrouted"} {
		if _, err := os.Stat(filepath.Join(dir, sub, day)); err != nil {
			t.Errorf("missing archive for %s: %v", sub, err)
		}
	}
}

func TestRetriesUntilEngineComesUp(t *testing.T) {
	// Reserve a port, then close it so the first connect attempts fail.
	tmp, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := tmp.Addr().String()
	tmp.Close()

	cfg := config{routes: map[string]string{"ADT": addr}, queueSize: 10, ackTimeout: time.Second}
	g := newGateway(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.serve(ctx, ln)

	conn, _ := net.Dial("tcp", ln.Addr().String())
	defer conn.Close()
	if code := send(t, conn, bufio.NewReader(conn), msg("ADT", "A01", "9")); code != "AA" {
		t.Fatalf("ACK = %s, want AA even with engine down", code)
	}
	time.Sleep(300 * time.Millisecond)
	eng := startFakeEngine(t, addr, "AA")
	waitFor(t, func() bool { return len(eng.received()) == 1 })
}

func TestParseHeader(t *testing.T) {
	h, err := parseHeader(msg("ORU", "R01", "abc"))
	if err != nil {
		t.Fatal(err)
	}
	if h.messageType != "ORU" || h.triggerEvent != "R01" || h.controlID != "abc" || h.version != "2.3" || h.sendingApp != "SIMHOSP" {
		t.Fatalf("unexpected header %+v", h)
	}
	ack := buildACK(h, "AA", "", time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC))
	if !strings.Contains(string(ack), "ACK^R01^ACK") || ackCode(ack) != "AA" || !strings.Contains(string(ack), "MSA|AA|abc") {
		t.Fatalf("bad ack %q", ack)
	}
}
