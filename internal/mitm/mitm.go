// Package mitm provides dynamic per-domain certificate generation and caching for TLS MITM.
package mitm

import (
	"container/list"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"sync"
	"time"

	"github.com/peter-wagstaff/claude-hybrid-router/internal/config"
)

// CertSchemaVersion is bumped whenever the on-disk CA template or
// required leaf extensions change. The launcher rotates the certs dir
// when it finds an older version file (or none at all) so users never
// hit a stale-CA failure after an upgrade.
const CertSchemaVersion = 2

// CertCache generates and caches per-domain TLS certificates signed by a MITM CA.
type CertCache struct {
	caCert   *x509.Certificate
	caDER    []byte
	caKey    *ecdsa.PrivateKey
	maxSize  int
	validity time.Duration

	mu    sync.Mutex
	cache map[string]*list.Element
	order *list.List
}

type cacheEntry struct {
	hostname string
	cert     tls.Certificate
	created  time.Time
}

// NewCertCache creates a CertCache from PEM-encoded CA certificate and key.
func NewCertCache(caCertPEM, caKeyPEM []byte) (*CertCache, error) {
	certBlock, _ := pem.Decode(caCertPEM)
	if certBlock == nil {
		return nil, fmt.Errorf("failed to decode CA certificate PEM")
	}
	caCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA certificate: %w", err)
	}

	keyBlock, _ := pem.Decode(caKeyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("failed to decode CA key PEM")
	}
	rawKey, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		k, err2 := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
		if err2 != nil {
			return nil, fmt.Errorf("parse CA key: %w", err)
		}
		var ok bool
		rawKey, ok = k.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("CA key is not ECDSA")
		}
	}

	return &CertCache{
		caCert:   caCert,
		caDER:    certBlock.Bytes,
		caKey:    rawKey,
		maxSize:  config.MitmCacheMaxSize,
		validity: time.Duration(config.MitmCertValidityHours * float64(time.Hour)),
		cache:    make(map[string]*list.Element),
		order:    list.New(),
	}, nil
}

// CACertificate returns the parsed CA certificate. Callers must not mutate it.
func (c *CertCache) CACertificate() *x509.Certificate { return c.caCert }

// GetTLSConfig returns a *tls.Config with a certificate for the given hostname.
// Results are cached with LRU eviction. The returned tls.Certificate carries
// both the leaf and the CA DER so clients receive a complete chain during the
// handshake — Node.js / undici refuse single-cert responses with
// UNABLE_TO_VERIFY_LEAF_SIGNATURE even when the root is trusted out of band.
func (c *CertCache) GetTLSConfig(hostname string) (*tls.Config, error) {
	c.mu.Lock()
	if el, ok := c.cache[hostname]; ok {
		entry := el.Value.(*cacheEntry)
		if time.Since(entry.created) < c.validity {
			c.order.MoveToFront(el)
			cert := entry.cert
			c.mu.Unlock()
			return c.tlsConfig(cert), nil
		}
		c.order.Remove(el)
		delete(c.cache, hostname)
	}
	c.mu.Unlock()

	cert, err := c.generateCert(hostname)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	entry := &cacheEntry{hostname: hostname, cert: cert, created: time.Now()}
	el := c.order.PushFront(entry)
	c.cache[hostname] = el
	for c.order.Len() > c.maxSize {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.cache, oldest.Value.(*cacheEntry).hostname)
	}
	c.mu.Unlock()

	return c.tlsConfig(cert), nil
}

func (c *CertCache) tlsConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"http/1.1"},
	}
}

func (c *CertCache) generateCert(hostname string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: hostname},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(c.validity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		AuthorityKeyId:        c.caCert.SubjectKeyId,
	}

	if ip := net.ParseIP(hostname); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{hostname}
	}

	leafDER, err := x509.CreateCertificate(rand.Reader, tmpl, c.caCert, &key.PublicKey, c.caKey)
	if err != nil {
		return tls.Certificate{}, err
	}

	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		return tls.Certificate{}, err
	}

	// Intentionally ship only the leaf (no CA in chain). A self-signed CA in
	// the presented chain triggers SELF_SIGNED_CERT_IN_CHAIN on clients that
	// haven't been told the CA is a trust anchor (Node/undici, strict
	// OpenSSL modes) — even when those clients DO trust the CA out of band
	// via NODE_EXTRA_CA_CERTS. mitmproxy / Charles / Proxyman all follow the
	// same pattern: leaf-only, let the client resolve the issuer from its
	// trust store. The caDER is still kept for possible future use (e.g.
	// stapled OCSP responses or a debug chain endpoint).
	_ = c.caDER
	return tls.Certificate{
		Certificate: [][]byte{leafDER},
		PrivateKey:  key,
		Leaf:        leaf,
	}, nil
}

// GenerateCA creates a self-signed CA certificate and key, returned as PEM bytes.
// The CA carries a proper SubjectKeyId derived from SHA-1(SPKI) (RFC 5280
// §4.2.1.2 recommended method) and the extensions modern TLS validators expect.
func GenerateCA() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}

	spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, nil, err
	}
	skid := sha1.Sum(spki)

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "claude-hybrid MITM CA",
			Organization: []string{"claude-hybrid"},
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		SubjectKeyId:          skid[:],
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM, nil
}
