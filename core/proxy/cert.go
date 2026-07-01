package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CACert CA 证书管理
type CACert struct {
	caCert    *x509.Certificate
	caKey     *ecdsa.PrivateKey
	certCache sync.Map // hostname -> *tls.Certificate
}

// LoadOrGenerateCA 加载或生成 CA 证书
func LoadOrGenerateCA(certDir string) (*CACert, error) {
	if certDir == "" {
		certDir = "certs"
	}

	certPath := filepath.Join(certDir, "ca.crt")
	keyPath := filepath.Join(certDir, "ca.key")

	// 尝试加载已有证书
	if _, err := os.Stat(certPath); err == nil {
		return loadCA(certPath, keyPath)
	}

	// 生成新的 CA 证书
	return generateCA(certDir, certPath, keyPath)
}

func loadCA(certPath, keyPath string) (*CACert, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("read CA cert: %w", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read CA key: %w", err)
	}

	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, fmt.Errorf("decode CA cert PEM failed")
	}
	caCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA cert: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("decode CA key PEM failed")
	}
	caKey, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA key: %w", err)
	}

	log.Printf("[Cert] Loaded CA cert from %s", certPath)
	return &CACert{caCert: caCert, caKey: caKey}, nil
}

func generateCA(certDir, certPath, keyPath string) (*CACert, error) {
	// 创建目录
	if err := os.MkdirAll(certDir, 0755); err != nil {
		return nil, fmt.Errorf("create cert dir: %w", err)
	}

	// 生成 CA 私钥
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate CA key: %w", err)
	}

	// 生成 CA 证书
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"YunAPI Proxy CA"},
			CommonName:   "YunAPI Proxy Root CA",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour), // 10 年
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("create CA cert: %w", err)
	}

	caCert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("parse CA cert: %w", err)
	}

	// 保存证书
	certFile, err := os.Create(certPath)
	if err != nil {
		return nil, fmt.Errorf("create cert file: %w", err)
	}
	pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	certFile.Close()

	// 保存私钥
	keyBytes, err := x509.MarshalECPrivateKey(caKey)
	if err != nil {
		return nil, fmt.Errorf("marshal CA key: %w", err)
	}
	keyFile, err := os.Create(keyPath)
	if err != nil {
		return nil, fmt.Errorf("create key file: %w", err)
	}
	pem.Encode(keyFile, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	keyFile.Close()

	log.Printf("[Cert] Generated new CA cert: %s", certPath)
	log.Printf("[Cert] ⚠️  Please install and trust this CA cert in your system/browser!")
	log.Printf("[Cert]    macOS: security add-trusted-cert -d -r trustRoot -k ~/Library/Keychains/login.keychain certs/ca.crt")

	return &CACert{caCert: caCert, caKey: caKey}, nil
}

// GenerateCert 为指定域名动态生成 TLS 证书
func (ca *CACert) GenerateCert(hostname string) (*tls.Certificate, error) {
	// 检查缓存
	if cached, ok := ca.certCache.Load(hostname); ok {
		return cached.(*tls.Certificate), nil
	}

	// 生成新证书
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			Organization: []string{"YunAPI Proxy"},
			CommonName:   hostname,
		},
		DNSNames:  []string{hostname},
		NotBefore: time.Now().Add(-1 * time.Hour),
		NotAfter:  time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.caCert, &key.PublicKey, ca.caKey)
	if err != nil {
		return nil, fmt.Errorf("create cert: %w", err)
	}

	tlsCert := &tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  key,
	}

	// 缓存
	ca.certCache.Store(hostname, tlsCert)
	log.Printf("[Cert] Generated cert for: %s", hostname)

	return tlsCert, nil
}
