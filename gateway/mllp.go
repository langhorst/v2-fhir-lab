package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// MLLP framing: <VT> message <FS><CR>
const (
	startBlock = 0x0b
	endBlock   = 0x1c
	carriageRt = 0x0d
)

// readFrame reads one MLLP-framed message. Bytes before the start block
// (stray newlines, keep-alive noise) are skipped.
func readFrame(r *bufio.Reader) ([]byte, error) {
	for {
		b, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if b == startBlock {
			break
		}
	}
	payload, err := r.ReadBytes(endBlock)
	if err != nil {
		return nil, fmt.Errorf("reading mllp payload: %w", err)
	}
	payload = payload[:len(payload)-1]
	b, err := r.ReadByte()
	if err != nil {
		return nil, fmt.Errorf("reading mllp trailer: %w", err)
	}
	if b != carriageRt {
		return nil, errors.New("mllp protocol error: missing trailing CR")
	}
	return payload, nil
}

func writeFrame(w io.Writer, msg []byte) error {
	buf := make([]byte, 0, len(msg)+3)
	buf = append(buf, startBlock)
	buf = append(buf, msg...)
	buf = append(buf, endBlock, carriageRt)
	_, err := w.Write(buf)
	return err
}

// header holds the MSH fields the gateway cares about.
type header struct {
	sendingApp      string // MSH-3
	sendingFacility string // MSH-4
	messageType     string // MSH-9.1, e.g. ADT
	triggerEvent    string // MSH-9.2, e.g. A01
	controlID       string // MSH-10
	version         string // MSH-12
	fieldSep        string
	componentSep    string
}

func segments(msg []byte) []string {
	s := strings.ReplaceAll(string(msg), "\r\n", "\r")
	s = strings.ReplaceAll(s, "\n", "\r")
	var out []string
	for _, seg := range strings.Split(s, "\r") {
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

func parseHeader(msg []byte) (header, error) {
	segs := segments(msg)
	if len(segs) == 0 || !strings.HasPrefix(segs[0], "MSH") || len(segs[0]) < 8 {
		return header{}, errors.New("message does not start with an MSH segment")
	}
	msh := segs[0]
	fs := string(msh[3])
	f := strings.Split(msh, fs)
	// f[0]="MSH", f[1]=MSH-2 (encoding chars), so MSH-n lives at f[n-1].
	get := func(n int) string {
		if n-1 < len(f) {
			return f[n-1]
		}
		return ""
	}
	h := header{
		sendingApp:      get(3),
		sendingFacility: get(4),
		controlID:       get(10),
		version:         get(12),
		fieldSep:        fs,
		componentSep:    "^",
	}
	if enc := get(2); enc != "" {
		h.componentSep = string(enc[0])
	}
	parts := strings.Split(get(9), h.componentSep)
	h.messageType = strings.ToUpper(parts[0])
	if len(parts) > 1 {
		h.triggerEvent = parts[1]
	}
	if h.messageType == "" {
		return header{}, errors.New("MSH-9 message type is empty")
	}
	return h, nil
}

// buildACK builds an original-mode acknowledgment for the inbound message.
func buildACK(h header, code, text string, now time.Time) []byte {
	fs, cs := h.fieldSep, h.componentSep
	if fs == "" {
		fs = "|"
	}
	if cs == "" {
		cs = "^"
	}
	version := h.version
	if version == "" {
		version = "2.3"
	}
	ts := now.Format("20060102150405")
	msh := strings.Join([]string{
		"MSH", "^~\\&", "HL7GATEWAY", "LAB",
		h.sendingApp, h.sendingFacility, ts, "",
		"ACK" + cs + h.triggerEvent + cs + "ACK",
		"ACK" + ts, "P", version,
	}, fs)
	msa := strings.Join([]string{"MSA", code, h.controlID}, fs)
	if text != "" {
		msa += fs + text
	}
	return []byte(msh + "\r" + msa + "\r")
}

// ackCode returns MSA-1 from an ACK message, or "" if absent.
func ackCode(msg []byte) string {
	for _, seg := range segments(msg) {
		if strings.HasPrefix(seg, "MSA") && len(seg) > 4 {
			f := strings.Split(seg, string(seg[3]))
			if len(f) > 1 {
				return strings.ToUpper(strings.TrimSpace(f[1]))
			}
		}
	}
	return ""
}

// readable converts HL7 segment terminators to newlines for the archive.
func readable(msg []byte) []byte {
	out := bytes.ReplaceAll(msg, []byte("\r\n"), []byte("\n"))
	out = bytes.ReplaceAll(out, []byte("\r"), []byte("\n"))
	return bytes.TrimRight(out, "\n")
}
