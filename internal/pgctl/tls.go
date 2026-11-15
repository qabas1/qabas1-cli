package pgctl

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"

	"github.com/qabas1/qabasone-cli/internal/nodeexec"
)

// SelfSignedCert returns a server certificate for replication TLS (traffic already
// rides WireGuard; TLS adds defence in depth and satisfies sslmode=require).
func SelfSignedCert(cn string, ips ...string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn, Organization: []string{"QabasOne DR"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{cn},
	}
	for _, ip := range ips {
		if parsed := net.ParseIP(ip); parsed != nil {
			tpl.IPAddresses = append(tpl.IPAddresses, parsed)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), nil
}

// ChownTLSScript gives the TLS files to the image's postgres user (key must be 0600
// and owned by the server user or PostgreSQL refuses to start).
func ChownTLSScript(image, tlsDir string) string {
	return fmt.Sprintf(`set -eu
uid=$(docker run --rm --entrypoint id %[1]s -u postgres)
gid=$(docker run --rm --entrypoint id %[1]s -g postgres)
chown "$uid:$gid" %[2]s/server.crt %[2]s/server.key
chmod 0644 %[2]s/server.crt
chmod 0600 %[2]s/server.key
`, image, nodeexec.Quote(tlsDir))
}
