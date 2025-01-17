// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package h2c implements the h2c part of HTTP/2.
//
// The h2c protocol is the non-TLS secured version of HTTP/2 which is not
// available from net/http.
package h2c

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"os"
	"strings"

	"github.com/pteich/traefik/log"

	"golang.org/x/net/http/httpguts"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

var (
	http2VerboseLogs bool
)

func init() {
	e := os.Getenv("GODEBUG")
	if strings.Contains(e, "http2debug=1") || strings.Contains(e, "http2debug=2") {
		http2VerboseLogs = true
	}
}

// Server implements net.Handler and enables h2c. Users who want h2c just need
// to provide an http.Server.
type Server struct {
	*http.Server
}

// Serve Put a middleware around the original handler to handle h2c
func (s Server) Serve(l net.Listener) error {
	originalHandler := s.Server.Handler
	if originalHandler == nil {
		originalHandler = http.DefaultServeMux
	}
	s.Server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PRI" && r.URL.Path == "*" && r.Proto == "HTTP/2.0" {
			if http2VerboseLogs {
				log.Debugf("Attempting h2c with prior knowledge.")
			}
			conn, err := initH2CWithPriorKnowledge(w, s.closeHijackedConnQuietly)
			if err != nil {
				if http2VerboseLogs {
					log.Debugf("Error h2c with prior knowledge: %v", err)
				}
				return
			}
			defer s.closeHijackedConnQuietly(conn)
			h2cSrv := &http2.Server{}
			h2cSrv.ServeConn(conn, &http2.ServeConnOpts{Handler: originalHandler})
			return
		}
		if conn, err := h2cUpgrade(w, r, s.closeHijackedConnQuietly); err == nil {
			defer s.closeHijackedConnQuietly(conn)
			h2cSrv := &http2.Server{}
			h2cSrv.ServeConn(conn, &http2.ServeConnOpts{Handler: originalHandler})
			return
		}
		originalHandler.ServeHTTP(w, r)
	})
	return s.Server.Serve(l)
}

func (s Server) closeHijackedConnQuietly(conn net.Conn) {
	connStateKey := conn
	if rwConn, ok := conn.(*rwConn); ok {
		connStateKey = rwConn.Conn
	}

	s.ConnState(connStateKey, http.StateClosed)
	if err := conn.Close(); err != nil {
		log.Debugf("Error closing hijacked connection: %v", err)
	}
}

// initH2CWithPriorKnowledge implements creating a h2c connection with prior
// knowledge (Section 3.4) and creates a net.Conn suitable for http2.ServeConn.
// All we have to do is look for the client preface that is suppose to be part
// of the body, and reforward the client preface on the net.Conn this function
// creates.
func initH2CWithPriorKnowledge(w http.ResponseWriter, onFailureAfterHijack func(conn net.Conn)) (net.Conn, error) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("hijack not supported")
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, fmt.Errorf("hijack failed: %v", err)
	}

	expectedBody := "SM\r\n\r\n"

	buf := make([]byte, len(expectedBody))
	n, err := io.ReadFull(rw, buf)
	if err != nil {
		onFailureAfterHijack(conn)
		return nil, fmt.Errorf("fail to read body: %v", err)
	}

	if bytes.Equal(buf[0:n], []byte(expectedBody)) {
		c := &rwConn{
			Conn:      conn,
			Reader:    io.MultiReader(bytes.NewBuffer([]byte(http2.ClientPreface)), rw),
			BufWriter: rw.Writer,
		}
		return c, nil
	}

	onFailureAfterHijack(conn)
	if http2VerboseLogs {
		log.Printf(
			"Missing the request body portion of the client preface. Wanted: %v Got: %v",
			[]byte(expectedBody),
			buf[0:n],
		)
	}
	return nil, errors.New("invalid client preface")
}

// drainClientPreface reads a single instance of the HTTP/2 client preface from
// the supplied reader.
func drainClientPreface(r io.Reader) error {
	var buf bytes.Buffer
	prefaceLen := int64(len([]byte(http2.ClientPreface)))
	n, err := io.CopyN(&buf, r, prefaceLen)
	if err != nil {
		return err
	}
	if n != prefaceLen || buf.String() != http2.ClientPreface {
		return fmt.Errorf("client never sent: %s", http2.ClientPreface)
	}
	return nil
}

// h2cUpgrade establishes a h2c connection using the HTTP/1 upgrade (Section 3.2).
func h2cUpgrade(w http.ResponseWriter, r *http.Request, onFailureAfterHijack func(conn net.Conn)) (net.Conn, error) {
	if !isH2CUpgrade(r.Header) {
		return nil, errors.New("non-conforming h2c headers")
	}

	// Initial bytes we put into conn to fool http2 server
	initBytes, _, err := convertH1ReqToH2(r)
	if err != nil {
		return nil, err
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("hijack not supported")
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, fmt.Errorf("hijack failed: %v", err)
	}

	rw.Write([]byte("HTTP/1.1 101 Switching Protocols\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: h2c\r\n\r\n"))
	rw.Flush()

	// A conforming client will now send an H2 client preface which need to drain
	// since we already sent this.
	if err := drainClientPreface(rw); err != nil {
		onFailureAfterHijack(conn)
		return nil, err
	}

	c := &rwConn{
		Conn:      conn,
		Reader:    io.MultiReader(initBytes, rw),
		BufWriter: newSettingsAckSwallowWriter(rw.Writer),
	}
	return c, nil
}

// convert the data contained in the HTTP/1 upgrade request into the HTTP/2
// version in byte form.
func convertH1ReqToH2(r *http.Request) (*bytes.Buffer, []http2.Setting, error) {
	h2Bytes := bytes.NewBuffer([]byte((http2.ClientPreface)))
	framer := http2.NewFramer(h2Bytes, nil)
	settings, err := getH2Settings(r.Header)
	if err != nil {
		return nil, nil, err
	}

	if err := framer.WriteSettings(settings...); err != nil {
		return nil, nil, err
	}

	headerBytes, err := getH2HeaderBytes(r, getMaxHeaderTableSize(settings))
	if err != nil {
		return nil, nil, err
	}

	maxFrameSize := int(getMaxFrameSize(settings))
	needOneHeader := len(headerBytes) < maxFrameSize
	err = framer.WriteHeaders(http2.HeadersFrameParam{
		StreamID:      1,
		BlockFragment: headerBytes,
		EndHeaders:    needOneHeader,
	})
	if err != nil {
		return nil, nil, err
	}

	for i := maxFrameSize; i < len(headerBytes); i += maxFrameSize {
		if len(headerBytes)-i > maxFrameSize {
			if err := framer.WriteContinuation(1,
				false, // endHeaders
				headerBytes[i:maxFrameSize]); err != nil {
				return nil, nil, err
			}
		} else {
			if err := framer.WriteContinuation(1,
				true, // endHeaders
				headerBytes[i:]); err != nil {
				return nil, nil, err
			}
		}
	}

	return h2Bytes, settings, nil
}

// getMaxFrameSize returns the SETTINGS_MAX_FRAME_SIZE. If not present default
// value is 16384 as specified by RFC 7540 Section 6.5.2.
func getMaxFrameSize(settings []http2.Setting) uint32 {
	for _, setting := range settings {
		if setting.ID == http2.SettingMaxFrameSize {
			return setting.Val
		}
	}
	return 16384
}

// getMaxHeaderTableSize returns the SETTINGS_HEADER_TABLE_SIZE. If not present
// default value is 4096 as specified by RFC 7540 Section 6.5.2.
func getMaxHeaderTableSize(settings []http2.Setting) uint32 {
	for _, setting := range settings {
		if setting.ID == http2.SettingHeaderTableSize {
			return setting.Val
		}
	}
	return 4096
}

// bufWriter is a Writer interface that also has a Flush method.
type bufWriter interface {
	io.Writer
	Flush() error
}

// rwConn implements net.Conn but overrides Read and Write so that reads and
// writes are forwarded to the provided io.Reader and bufWriter.
type rwConn struct {
	net.Conn
	io.Reader
	BufWriter bufWriter
}

// Read forwards reads to the underlying Reader.
func (c *rwConn) Read(p []byte) (int, error) {
	return c.Reader.Read(p)
}

// Write forwards writes to the underlying bufWriter and immediately flushes.
func (c *rwConn) Write(p []byte) (int, error) {
	n, err := c.BufWriter.Write(p)
	if err := c.BufWriter.Flush(); err != nil {
		return 0, err
	}
	return n, err
}

// settingsAckSwallowWriter is a writer that normally forwards bytes to it's
// underlying Writer, but swallows the first SettingsAck frame that it sees.
type settingsAckSwallowWriter struct {
	Writer     *bufio.Writer
	buf        []byte
	didSwallow bool
}

// newSettingsAckSwallowWriter returns a new settingsAckSwallowWriter.
func newSettingsAckSwallowWriter(w *bufio.Writer) *settingsAckSwallowWriter {
	return &settingsAckSwallowWriter{
		Writer:     w,
		buf:        make([]byte, 0),
		didSwallow: false,
	}
}

// Write implements io.Writer interface. Normally forwards bytes to w.Writer,
// except for the first Settings ACK frame that it sees.
func (w *settingsAckSwallowWriter) Write(p []byte) (int, error) {
	if !w.didSwallow {
		w.buf = append(w.buf, p...)
		// Process all the frames we have collected into w.buf
		for {
			// Append until we get full frame header which is 9 bytes
			if len(w.buf) < 9 {
				break
			}
			// Check if we have collected a whole frame.
			fh, err := http2.ReadFrameHeader(bytes.NewBuffer(w.buf))
			if err != nil {
				// Corrupted frame, fail current Write
				return 0, err
			}
			fSize := fh.Length + 9
			if uint32(len(w.buf)) < fSize {
				// Have not collected whole frame. Stop processing buf, and withhold on
				// forward bytes to w.Writer until we get the full frame.
				break
			}

			// We have now collected a whole frame.
			if fh.Type == http2.FrameSettings && fh.Flags.Has(http2.FlagSettingsAck) {
				// If Settings ACK frame, do not forward to underlying writer, remove
				// bytes from w.buf, and record that we have swallowed Settings Ack
				// frame.
				w.didSwallow = true
				w.buf = w.buf[fSize:]
				continue
			}

			// Not settings ack frame. Forward bytes to w.Writer.
			if _, err := w.Writer.Write(w.buf[:fSize]); err != nil {
				// Couldn't forward bytes. Fail current Write.
				return 0, err
			}
			w.buf = w.buf[fSize:]
		}
		return len(p), nil
	}
	return w.Writer.Write(p)
}

// Flush calls w.Writer.Flush.
func (w *settingsAckSwallowWriter) Flush() error {
	return w.Writer.Flush()
}

// isH2CUpgrade returns true if the header properly request an upgrade to h2c
// as specified by Section 3.2.
func isH2CUpgrade(h http.Header) bool {
	return httpguts.HeaderValuesContainsToken(h[textproto.CanonicalMIMEHeaderKey("Upgrade")], "h2c") &&
		httpguts.HeaderValuesContainsToken(h[textproto.CanonicalMIMEHeaderKey("Connection")], "HTTP2-Settings")
}

// getH2Settings returns the []http2.Setting that are encoded in the
// HTTP2-Settings header.
func getH2Settings(h http.Header) ([]http2.Setting, error) {
	vals, ok := h[textproto.CanonicalMIMEHeaderKey("HTTP2-Settings")]
	if !ok {
		return nil, errors.New("missing HTTP2-Settings header")
	}
	if len(vals) != 1 {
		return nil, fmt.Errorf("expected 1 HTTP2-Settings. Got: %v", vals)
	}
	settings, err := decodeSettings(vals[0])
	if err != nil {
		return nil, fmt.Errorf("invalid HTTP2-Settings: %q", vals[0])
	}
	return settings, nil
}

// decodeSettings decodes the base64url header value of the HTTP2-Settings
// header. RFC 7540 Section 3.2.1.
func decodeSettings(headerVal string) ([]http2.Setting, error) {
	b, err := base64.RawURLEncoding.DecodeString(headerVal)
	if err != nil {
		return nil, err
	}
	if len(b)%6 != 0 {
		return nil, err
	}
	settings := make([]http2.Setting, 0)
	for i := 0; i < len(b)/6; i++ {
		settings = append(settings, http2.Setting{
			ID:  http2.SettingID(binary.BigEndian.Uint16(b[i*6 : i*6+2])),
			Val: binary.BigEndian.Uint32(b[i*6+2 : i*6+6]),
		})
	}

	return settings, nil
}

// getH2HeaderBytes return the headers in r a []bytes encoded by HPACK.
func getH2HeaderBytes(r *http.Request, maxHeaderTableSize uint32) ([]byte, error) {
	headerBytes := bytes.NewBuffer(nil)
	hpackEnc := hpack.NewEncoder(headerBytes)
	hpackEnc.SetMaxDynamicTableSize(maxHeaderTableSize)

	// Section 8.1.2.3
	err := hpackEnc.WriteField(hpack.HeaderField{
		Name:  ":method",
		Value: r.Method,
	})
	if err != nil {
		return nil, err
	}

	err = hpackEnc.WriteField(hpack.HeaderField{
		Name:  ":scheme",
		Value: "http",
	})
	if err != nil {
		return nil, err
	}

	err = hpackEnc.WriteField(hpack.HeaderField{
		Name:  ":authority",
		Value: r.Host,
	})
	if err != nil {
		return nil, err
	}

	path := r.URL.Path
	if r.URL.RawQuery != "" {
		path = strings.Join([]string{path, r.URL.RawQuery}, "?")
	}
	err = hpackEnc.WriteField(hpack.HeaderField{
		Name:  ":path",
		Value: path,
	})
	if err != nil {
		return nil, err
	}

	// TODO Implement Section 8.3

	for header, values := range r.Header {
		// Skip non h2 headers
		if isNonH2Header(header) {
			continue
		}
		for _, v := range values {
			err := hpackEnc.WriteField(hpack.HeaderField{
				Name:  strings.ToLower(header),
				Value: v,
			})
			if err != nil {
				return nil, err
			}
		}
	}
	return headerBytes.Bytes(), nil
}

// Connection specific headers listed in RFC 7540 Section 8.1.2.2 that are not
// suppose to be transferred to HTTP/2. The Http2-Settings header is skipped
// since already use to create the HTTP/2 SETTINGS frame.
var nonH2Headers = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Connection",
	"Transfer-Encoding",
	"Upgrade",
	"Http2-Settings",
}

// isNonH2Header returns true if header should not be transferred to HTTP/2.
func isNonH2Header(header string) bool {
	for _, nonH2h := range nonH2Headers {
		if header == nonH2h {
			return true
		}
	}
	return false
}
