// Package relay carries WireGuard datagrams over TLS for replicas whose network drops
// WireGuard. The writer runs Serve (TCP, self-signed certificate pinned by SHA-256);
// a replica runs Connect, which exposes a loopback UDP port its WireGuard peer endpoint
// points at. The server only ever forwards to the local WireGuard port.
//
// Wire format after TLS: the client sends "QRLY1 <node> <token>\n", the server answers
// "OK\n" or closes. Then each datagram is a 2-byte big-endian length plus payload.
package relay

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	magic = "QRLY1"
	// MaxFrame is the largest datagram carried (the length prefix is 16 bits).
	MaxFrame = 65535

	authTimeout = 10 * time.Second
	// IdleTimeout drops a session with no traffic; WireGuard keepalives (25s) keep
	// healthy sessions busy.
	IdleTimeout = 3 * time.Minute
	keepAlive   = 30 * time.Second
	alpn        = "h2"
)

// ErrAuth is returned by the client when the server rejects the node or token.
var ErrAuth = errors.New("relay rejected the node credentials")

// CertFile and KeyFile are the certificate paths inside a relay directory.
func CertFile(dir string) string { return filepath.Join(dir, "cert.pem") }
func KeyFile(dir string) string  { return filepath.Join(dir, "key.pem") }

// GenerateCert writes a self-signed ECDSA P-256 certificate and key (0600) into dir
// unless both already exist, and returns the certificate fingerprint.
func GenerateCert(dir string) (string, error) {
	if fp, err := FingerprintFile(CertFile(dir)); err == nil {
		if _, err := os.Stat(KeyFile(dir)); err == nil {
			return fp, nil
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return "", err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "qabas-relay"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(dir, 0o700)
	if err := writeAtomic(KeyFile(dir), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})); err != nil {
		return "", err
	}
	if err := writeAtomic(CertFile(dir), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
		return "", err
	}
	return Fingerprint(der), nil
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Fingerprint is the lowercase hex SHA-256 of a DER certificate.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// FingerprintFile reads a PEM certificate and returns its fingerprint.
func FingerprintFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", fmt.Errorf("%s: no PEM certificate", path)
	}
	return Fingerprint(block.Bytes), nil
}

// ReadFrame reads one length-prefixed datagram into buf (len(buf) >= MaxFrame).
func ReadFrame(r io.Reader, buf []byte) (int, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if n == 0 {
		return 0, errors.New("relay: empty frame")
	}
	if n > len(buf) {
		return 0, fmt.Errorf("relay: frame of %d bytes exceeds buffer", n)
	}
	_, err := io.ReadFull(r, buf[:n])
	return n, err
}

// WriteFrame writes one length-prefixed datagram in a single Write.
func WriteFrame(w io.Writer, p []byte) error {
	if len(p) == 0 || len(p) > MaxFrame {
		return fmt.Errorf("relay: datagram of %d bytes cannot be framed", len(p))
	}
	b := make([]byte, 2+len(p))
	binary.BigEndian.PutUint16(b, uint16(len(p)))
	copy(b[2:], p)
	_, err := w.Write(b)
	return err
}

// ServerConfig configures the writer side.
type ServerConfig struct {
	Listen   string // ":8443"
	CertFile string
	KeyFile  string
	// WGAddr is the local WireGuard UDP address (127.0.0.1:51820); nothing else is reachable.
	WGAddr string
	// Token returns the expected token for a node, or an error for unknown nodes.
	Token func(node string) (string, error)
	Logf  func(format string, args ...any)
	// Ready, when set, receives the bound listener address once accepting.
	Ready func(addr net.Addr)
}

type server struct {
	cfg    ServerConfig
	wg     *net.UDPAddr
	mu     sync.Mutex
	active map[string]net.Conn
}

// Serve accepts relay sessions until ctx is cancelled.
func Serve(ctx context.Context, cfg ServerConfig) error {
	if cfg.Token == nil {
		return errors.New("relay: no token source")
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	wgAddr, err := net.ResolveUDPAddr("udp", cfg.WGAddr)
	if err != nil {
		return fmt.Errorf("relay: wireguard address: %w", err)
	}
	if !wgAddr.IP.IsLoopback() {
		return fmt.Errorf("relay: wireguard address %s must be loopback", cfg.WGAddr)
	}
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return fmt.Errorf("relay: load certificate: %w", err)
	}
	lc := net.ListenConfig{KeepAlive: keepAlive}
	ln, err := lc.Listen(ctx, "tcp", cfg.Listen)
	if err != nil {
		return err
	}
	tln := tls.NewListener(ln, &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{alpn},
	})
	defer tln.Close()
	go func() {
		<-ctx.Done()
		_ = tln.Close()
	}()
	if cfg.Ready != nil {
		cfg.Ready(ln.Addr())
	}
	s := &server{cfg: cfg, wg: wgAddr, active: map[string]net.Conn{}}
	cfg.Logf("relay listening on %s, forwarding to %s", ln.Addr(), wgAddr)
	for {
		c, err := tln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				s.closeAll()
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		go s.handle(c)
	}
}

func (s *server) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.active {
		_ = c.Close()
	}
}

func (s *server) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(authTimeout))
	br := bufio.NewReaderSize(c, MaxFrame+2)
	node, ok := s.auth(br)
	if !ok {
		return
	}
	s.mu.Lock()
	if old := s.active[node]; old != nil {
		_ = old.Close()
	}
	s.active[node] = c
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.active[node] == c {
			delete(s.active, node)
		}
		s.mu.Unlock()
	}()
	if _, err := io.WriteString(c, "OK\n"); err != nil {
		return
	}
	_ = c.SetDeadline(time.Time{})

	u, err := net.DialUDP("udp", nil, s.wg)
	if err != nil {
		s.cfg.Logf("relay %s: dial wireguard: %v", node, err)
		return
	}
	defer u.Close()
	s.cfg.Logf("relay %s connected from %s", node, c.RemoteAddr())
	defer s.cfg.Logf("relay %s disconnected", node)

	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, MaxFrame)
		for {
			n, err := u.Read(buf)
			if err != nil {
				return
			}
			if err := WriteFrame(c, buf[:n]); err != nil {
				return
			}
		}
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, MaxFrame)
		for {
			_ = c.SetReadDeadline(time.Now().Add(IdleTimeout))
			n, err := ReadFrame(br, buf)
			if err != nil {
				return
			}
			if _, err := u.Write(buf[:n]); err != nil {
				return
			}
		}
	}()
	<-done
}

func (s *server) auth(br *bufio.Reader) (string, bool) {
	line, err := readLine(br, 512)
	if err != nil {
		return "", false
	}
	f := strings.Fields(line)
	if len(f) != 3 || f[0] != magic {
		return "", false
	}
	want, err := s.cfg.Token(f[1])
	if err != nil || want == "" {
		s.cfg.Logf("relay: rejected unknown node %q", f[1])
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(want), []byte(f[2])) != 1 {
		s.cfg.Logf("relay: rejected bad token for %q", f[1])
		return "", false
	}
	return f[1], true
}

func readLine(br *bufio.Reader, max int) (string, error) {
	var b []byte
	for len(b) <= max {
		c, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		if c == '\n' {
			return string(b), nil
		}
		b = append(b, c)
	}
	return "", errors.New("relay: line too long")
}

// ClientConfig configures the replica side.
type ClientConfig struct {
	Server     string // writer host:port
	CertSHA256 string // pinned fingerprint (hex)
	Node       string
	Token      string
	// Listen is the loopback UDP address WireGuard sends to (127.0.0.1:51821).
	Listen string
	Logf   func(format string, args ...any)
	// Ready, when set, receives the bound UDP address.
	Ready func(addr net.Addr)
}

// Dial opens an authenticated relay session (TLS, pinned certificate, token).
func Dial(ctx context.Context, cfg ClientConfig) (net.Conn, *bufio.Reader, error) {
	host, _, err := net.SplitHostPort(cfg.Server)
	if err != nil {
		return nil, nil, err
	}
	want := strings.ToLower(strings.TrimSpace(cfg.CertSHA256))
	if want == "" {
		return nil, nil, errors.New("relay: no pinned certificate fingerprint")
	}
	tcfg := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // replaced by the fingerprint pin below
		NextProtos:         []string{alpn},
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("relay: server sent no certificate")
			}
			if got := Fingerprint(cs.PeerCertificates[0].Raw); subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				return fmt.Errorf("relay: certificate fingerprint %s does not match the pinned %s", got, want)
			}
			return nil
		},
	}
	if net.ParseIP(host) == nil {
		tcfg.ServerName = host
	}
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 15 * time.Second, KeepAlive: keepAlive}, Config: tcfg}
	c, err := d.DialContext(ctx, "tcp", cfg.Server)
	if err != nil {
		return nil, nil, err
	}
	_ = c.SetDeadline(time.Now().Add(authTimeout))
	if _, err := fmt.Fprintf(c, "%s %s %s\n", magic, cfg.Node, cfg.Token); err != nil {
		c.Close()
		return nil, nil, err
	}
	br := bufio.NewReaderSize(c, MaxFrame+2)
	line, err := readLine(br, 16)
	if err != nil || line != "OK" {
		c.Close()
		return nil, nil, ErrAuth
	}
	_ = c.SetDeadline(time.Time{})
	return c, br, nil
}

// Connect relays between the loopback UDP port and the writer until ctx is cancelled,
// reconnecting with backoff. Authentication failures are retried slowly (the token may
// not be registered yet) but never tight-looped.
func Connect(ctx context.Context, cfg ClientConfig) error {
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	laddr, err := net.ResolveUDPAddr("udp", cfg.Listen)
	if err != nil {
		return err
	}
	if !laddr.IP.IsLoopback() {
		return fmt.Errorf("relay: listen address %s must be loopback", cfg.Listen)
	}
	u, err := net.ListenUDP("udp", laddr)
	if err != nil {
		return err
	}
	defer u.Close()
	if cfg.Ready != nil {
		cfg.Ready(u.LocalAddr())
	}
	go func() {
		<-ctx.Done()
		_ = u.Close()
	}()

	var (
		mu   sync.Mutex
		cur  net.Conn
		peer *net.UDPAddr
	)
	// One reader for the loopback socket across sessions; datagrams with no live
	// session are dropped (WireGuard retransmits).
	go func() {
		buf := make([]byte, MaxFrame)
		for {
			n, from, err := u.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if !from.IP.IsLoopback() {
				continue
			}
			mu.Lock()
			peer = from
			c := cur
			mu.Unlock()
			if c != nil {
				_ = WriteFrame(c, buf[:n])
			}
		}
	}()

	backoff := time.Second
	for ctx.Err() == nil {
		c, br, err := Dial(ctx, cfg)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			wait := backoff
			if errors.Is(err, ErrAuth) {
				wait = 30 * time.Second
			}
			cfg.Logf("relay: connect %s: %v (retry in %s)", cfg.Server, err, wait)
			select {
			case <-ctx.Done():
			case <-time.After(wait):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		cfg.Logf("relay: connected to %s", cfg.Server)
		started := time.Now()
		mu.Lock()
		cur = c
		mu.Unlock()
		stop := context.AfterFunc(ctx, func() { _ = c.Close() })
		buf := make([]byte, MaxFrame)
		for {
			_ = c.SetReadDeadline(time.Now().Add(IdleTimeout))
			n, err := ReadFrame(br, buf)
			if err != nil {
				if ctx.Err() == nil {
					cfg.Logf("relay: session ended: %v", err)
				}
				break
			}
			mu.Lock()
			p := peer
			mu.Unlock()
			if p != nil {
				_, _ = u.WriteToUDP(buf[:n], p)
			}
		}
		stop()
		mu.Lock()
		cur = nil
		mu.Unlock()
		_ = c.Close()
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
	}
	return nil
}
