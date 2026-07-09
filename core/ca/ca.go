package ca

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// Manager CA 证书管理器（只读加载，不修改系统状态）
type Manager struct {
	cert        *x509.Certificate
	pem         []byte
	fingerprint string
	dir         string

	detector    Detector
	distributor Distributor
}

// NewManager 加载已有 CA 证书
func NewManager(dir string) (*Manager, error) {
	if dir == "" {
		dir = "certs"
	}
	certPath := filepath.Join(dir, "ca.crt")
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("load CA cert: %w", err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("invalid CA PEM: %s", certPath)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA cert: %w", err)
	}
	fpHash := sha256.Sum256(cert.Raw)
	return &Manager{
		cert:        cert,
		pem:         pemBytes,
		fingerprint: fmt.Sprintf("SHA256:%x", fpHash[:8]),
		dir:         dir,
		detector:    newDetector(),
		distributor: newDistributor(),
	}, nil
}

// GenerateCA 生成新 CA 并写入文件
// seed: 确定性的 Ed25519 种子（nil 用随机 ECDSA）
func GenerateCA(dir string, seed []byte) (*Manager, error) {
	if dir == "" {
		dir = "certs"
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create dir: %w", err)
	}

	var cert *x509.Certificate
	var pemBytes []byte
	if len(seed) > 0 {
		cert, pemBytes = generateEd25519(seed)
	} else {
		cert, pemBytes = generateECDSA()
	}

	certPath := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(certPath, pemBytes, 0644); err != nil {
		return nil, fmt.Errorf("write CA cert: %w", err)
	}

	fpHash := sha256.Sum256(cert.Raw)
	return &Manager{
		cert:        cert,
		pem:         pemBytes,
		fingerprint: fmt.Sprintf("SHA256:%x", fpHash[:8]),
		dir:         dir,
		detector:    newDetector(),
		distributor: newDistributor(),
	}, nil
}

// Fingerprint 返回 CA 指纹（短格式，用于展示）
func (m *Manager) Fingerprint() string { return m.fingerprint }

// PEM 返回 CA 证书的 PEM 字节
func (m *Manager) PEM() []byte { return m.pem }

// Cert 返回解析后的证书对象
func (m *Manager) Cert() *x509.Certificate { return m.cert }

// CheckTrust 检测所有已知信任存储
func (m *Manager) CheckTrust() []TrustStatus {
	return m.detector.CheckAll(m.fingerprint)
}

// MobileConfig 生成 macOS 配置描述文件
func (m *Manager) MobileConfig() ([]byte, error) {
	return m.distributor.MobileConfig(m.pem)
}

// ========================================
// 内部：CA 生成
// ========================================

func generateEd25519(seed []byte) (*x509.Certificate, []byte) {
	h := sha256.Sum256(seed)
	priv := ed25519.NewKeyFromSeed(h[:])

	tmpl := caTemplate()
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, priv.Public(), priv)
	if err != nil {
		panic(fmt.Sprintf("generate Ed25519 CA: %v", err))
	}
	cert, _ := x509.ParseCertificate(certDER)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return cert, pemBytes
}

func generateECDSA() (*x509.Certificate, []byte) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(fmt.Sprintf("generate ECDSA key: %v", err))
	}
	tmpl := caTemplate()
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		panic(fmt.Sprintf("generate ECDSA CA: %v", err))
	}
	cert, _ := x509.ParseCertificate(certDER)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return cert, pemBytes
}

func caTemplate() *x509.Certificate {
	return &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkixName(),
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		MaxPathLen:   0,
	}
}

func pkixName() pkix.Name {
	var n pkix.Name
	n.Organization = []string{"mitm-proxy CA"}
	n.CommonName = "mitm-proxy Root CA"
	return n
}
