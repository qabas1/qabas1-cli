package relay

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

type harness struct {
	fp       string
	srvAddr  string
	wg       *net.UDPConn // stands in for the writer's WireGuard
	cancel   context.CancelFunc
	tokenFor map[string]string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	fp, err := GenerateCert(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := GenerateCert(dir); err != nil || again != fp {
		t.Fatalf("GenerateCert not idempotent: %s vs %s (%v)", again, fp, err)
	}
	wg, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{fp: fp, wg: wg, tokenFor: map[string]string{"home": "secret-token"}}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	ready := make(chan net.Addr, 1)
	go func() {
		_ = Serve(ctx, ServerConfig{
			Listen: "127.0.0.1:0", CertFile: CertFile(dir), KeyFile: KeyFile(dir), WGAddr: wg.LocalAddr().String(),
			Token: func(node string) (string, error) {
				if v, ok := h.tokenFor[node]; ok {
					return v, nil
				}
				return "", errors.New("unknown")
			},
			Ready: func(a net.Addr) { ready <- a },
		})
	}()
	select {
	case a := <-ready:
		h.srvAddr = a.String()
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}
	t.Cleanup(func() { cancel(); wg.Close() })
	return h
}

func (h *harness) client(t *testing.T, cfg ClientConfig) net.Addr {
	t.Helper()
	cfg.Server, cfg.Listen = h.srvAddr, "127.0.0.1:0"
	if cfg.CertSHA256 == "" {
		cfg.CertSHA256 = h.fp
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ready := make(chan net.Addr, 1)
	go func() {
		_ = Connect(ctx, ClientConfig{Server: cfg.Server, Listen: cfg.Listen, CertSHA256: cfg.CertSHA256, Node: cfg.Node, Token: cfg.Token, Ready: func(a net.Addr) { ready <- a }})
	}()
	select {
	case a := <-ready:
		return a
	case <-time.After(5 * time.Second):
		t.Fatal("client did not start")
	}
	return nil
}

func TestRoundTrip(t *testing.T) {
	h := newHarness(t)
	listen := h.client(t, ClientConfig{Node: "home", Token: "secret-token"})

	local, err := net.DialUDP("udp", nil, listen.(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()

	buf := make([]byte, MaxFrame)
	var from *net.UDPAddr
	deadline := time.Now().Add(10 * time.Second)
	for from == nil && time.Now().Before(deadline) {
		// The session may not be up yet; WireGuard retransmits, so do we.
		_, _ = local.Write([]byte("handshake-init"))
		_ = h.wg.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		n, addr, err := h.wg.ReadFromUDP(buf)
		if err == nil {
			if !bytes.Equal(buf[:n], []byte("handshake-init")) {
				t.Fatalf("server delivered %q", buf[:n])
			}
			from = addr
		}
	}
	if from == nil {
		t.Fatal("datagram never reached the writer side")
	}
	if !from.IP.IsLoopback() {
		t.Fatalf("writer saw source %s, want loopback", from)
	}
	big := bytes.Repeat([]byte{0xab}, 1500)
	if _, err := h.wg.WriteToUDP(big, from); err != nil {
		t.Fatal(err)
	}
	_ = local.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := local.Read(buf)
	if err != nil || !bytes.Equal(buf[:n], big) {
		t.Fatalf("reply: %d bytes, err %v", n, err)
	}
}

func TestRejectsBadCredentialsAndPin(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cases := []struct {
		name string
		cfg  ClientConfig
		auth bool
	}{
		{"wrong token", ClientConfig{Node: "home", Token: "nope", CertSHA256: h.fp}, true},
		{"unknown node", ClientConfig{Node: "ghost", Token: "secret-token", CertSHA256: h.fp}, true},
		{"wrong pin", ClientConfig{Node: "home", Token: "secret-token", CertSHA256: "00" + h.fp[2:]}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.cfg.Server = h.srvAddr
			conn, _, err := Dial(ctx, c.cfg)
			if err == nil {
				conn.Close()
				t.Fatal("accepted")
			}
			if c.auth != errors.Is(err, ErrAuth) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	if _, _, err := Dial(ctx, ClientConfig{Server: h.srvAddr, Node: "home", Token: "secret-token", CertSHA256: h.fp}); err != nil {
		t.Fatalf("valid credentials rejected: %v", err)
	}
}

func TestSecondSessionReplacesFirst(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := ClientConfig{Server: h.srvAddr, Node: "home", Token: "secret-token", CertSHA256: h.fp}
	first, br, err := Dial(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, _, err := Dial(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	_ = first.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := ReadFrame(br, make([]byte, MaxFrame)); err == nil {
		t.Fatal("first session still open")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("first session was not closed by the server")
	}
}

func TestFraming(t *testing.T) {
	var b bytes.Buffer
	if err := WriteFrame(&b, make([]byte, MaxFrame+1)); err == nil {
		t.Fatal("oversized datagram framed")
	}
	if err := WriteFrame(&b, nil); err == nil {
		t.Fatal("empty datagram framed")
	}
	if err := WriteFrame(&b, []byte("abc")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2)
	if _, err := ReadFrame(bytes.NewReader(b.Bytes()), buf); err == nil {
		t.Fatal("frame larger than buffer accepted")
	}
	buf = make([]byte, MaxFrame)
	n, err := ReadFrame(bytes.NewReader(b.Bytes()), buf)
	if err != nil || string(buf[:n]) != "abc" {
		t.Fatalf("%q %v", buf[:n], err)
	}
}

func TestLoopbackOnly(t *testing.T) {
	if err := Serve(context.Background(), ServerConfig{WGAddr: "10.0.0.1:51820", Token: func(string) (string, error) { return "", nil }}); err == nil {
		t.Fatal("server accepted a non-loopback WireGuard target")
	}
	if err := Connect(context.Background(), ClientConfig{Listen: "0.0.0.0:51821"}); err == nil {
		t.Fatal("client accepted a non-loopback listen address")
	}
}
