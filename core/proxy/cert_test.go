package proxy

import (
	"crypto/x509"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	// 证书生成与连接管理都会打印日志，测试中静音
	log.SetOutput(io.Discard)
	code := m.Run()
	log.SetOutput(os.Stderr)
	os.Exit(code)
}

func TestLoadOrGenerateCAGeneratesWhenMissing(t *testing.T) {
	dir := t.TempDir()

	ca, err := LoadOrGenerateCA(dir)
	if err != nil {
		t.Fatalf("LoadOrGenerateCA: %v", err)
	}
	if ca == nil || ca.caCert == nil || ca.caKey == nil {
		t.Fatal("the returned CA is incomplete")
	}

	// 证书与私钥都应落盘，供用户导入系统信任库
	for _, name := range []string{"ca.crt", "ca.key"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s should have been written: %v", name, err)
		}
	}

	if !ca.caCert.IsCA {
		t.Error("the generated certificate must have the CA flag set")
	}
	if ca.caCert.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("the CA cert must be allowed to sign certificates")
	}
	if !ca.caCert.NotAfter.After(time.Now()) {
		t.Error("the generated CA is already expired")
	}
}

func TestLoadOrGenerateCACreatesMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "certs")

	if _, err := LoadOrGenerateCA(dir); err != nil {
		t.Fatalf("LoadOrGenerateCA should create the directory, got %v", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("%s should exist as a directory", dir)
	}
}

func TestLoadOrGenerateCAReusesExisting(t *testing.T) {
	dir := t.TempDir()

	first, err := LoadOrGenerateCA(dir)
	if err != nil {
		t.Fatalf("first LoadOrGenerateCA: %v", err)
	}
	second, err := LoadOrGenerateCA(dir)
	if err != nil {
		t.Fatalf("second LoadOrGenerateCA: %v", err)
	}

	// 复用而非重新生成，否则已导入系统的 CA 会失效
	if !first.caCert.Equal(second.caCert) {
		t.Error("the second call should load the existing CA, not generate a new one")
	}
}

func TestLoadCAErrors(t *testing.T) {
	t.Run("证书文件损坏", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "ca.crt"), []byte("not a pem"), 0644)
		os.WriteFile(filepath.Join(dir, "ca.key"), []byte("not a pem"), 0600)

		if _, err := LoadOrGenerateCA(dir); err == nil {
			t.Error("loading a malformed CA should fail rather than silently regenerate")
		}
	})

	t.Run("私钥缺失", func(t *testing.T) {
		dir := t.TempDir()
		// 先生成一套合法证书，再删掉私钥
		if _, err := LoadOrGenerateCA(dir); err != nil {
			t.Fatalf("seed CA: %v", err)
		}
		os.Remove(filepath.Join(dir, "ca.key"))

		if _, err := LoadOrGenerateCA(dir); err == nil {
			t.Error("a missing private key should surface as an error")
		}
	})
}

func TestGenerateCert(t *testing.T) {
	ca, err := LoadOrGenerateCA(t.TempDir())
	if err != nil {
		t.Fatalf("LoadOrGenerateCA: %v", err)
	}

	t.Run("为域名签发证书", func(t *testing.T) {
		cert, err := ca.GenerateCert("example.com")
		if err != nil {
			t.Fatalf("GenerateCert: %v", err)
		}
		if len(cert.Certificate) == 0 || cert.PrivateKey == nil {
			t.Fatal("the returned TLS certificate is incomplete")
		}

		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			t.Fatalf("parse leaf: %v", err)
		}
		if leaf.Subject.CommonName != "example.com" {
			t.Errorf("CommonName = %q, want example.com", leaf.Subject.CommonName)
		}
		if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "example.com" {
			t.Errorf("DNSNames = %v, want [example.com]", leaf.DNSNames)
		}
	})

	t.Run("签发的证书可被 CA 校验", func(t *testing.T) {
		cert, err := ca.GenerateCert("verify.example.com")
		if err != nil {
			t.Fatalf("GenerateCert: %v", err)
		}
		leaf, _ := x509.ParseCertificate(cert.Certificate[0])

		pool := x509.NewCertPool()
		pool.AddCert(ca.caCert)
		// 这是 MITM 能工作的前提：浏览器信任 CA 后，动态证书就能通过校验
		if _, err := leaf.Verify(x509.VerifyOptions{
			Roots:     pool,
			DNSName:   "verify.example.com",
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}); err != nil {
			t.Errorf("the generated leaf should chain to the CA: %v", err)
		}
	})

	t.Run("同域名命中缓存", func(t *testing.T) {
		first, err := ca.GenerateCert("cached.example.com")
		if err != nil {
			t.Fatalf("GenerateCert: %v", err)
		}
		second, err := ca.GenerateCert("cached.example.com")
		if err != nil {
			t.Fatalf("GenerateCert: %v", err)
		}
		if first != second {
			t.Error("the same hostname should return the cached certificate pointer")
		}
	})

	t.Run("不同域名签发不同证书", func(t *testing.T) {
		a, _ := ca.GenerateCert("a.example.com")
		b, _ := ca.GenerateCert("b.example.com")
		if a == b {
			t.Error("different hostnames must not share a certificate")
		}
	})
}

// TestGenerateCertConcurrent 在 -race 下验证证书缓存的并发访问。
func TestGenerateCertConcurrent(t *testing.T) {
	ca, err := LoadOrGenerateCA(t.TempDir())
	if err != nil {
		t.Fatalf("LoadOrGenerateCA: %v", err)
	}

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				// 混合命中同一域名与不同域名，压一压 sync.Map
				if _, err := ca.GenerateCert("shared.example.com"); err != nil {
					t.Errorf("GenerateCert: %v", err)
					return
				}
				if _, err := ca.GenerateCert(string(rune('a'+g)) + ".example.com"); err != nil {
					t.Errorf("GenerateCert: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}
