package ca

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateCA_Deterministic(t *testing.T) {
	dir := t.TempDir()
	seed := []byte("mitm-proxy-v1")

	m1, err := GenerateCA(dir, seed)
	if err != nil {
		t.Fatal(err)
	}
	// 重新生成应产生相同指纹
	os.Remove(filepath.Join(dir, "ca.crt"))
	m2, err := GenerateCA(dir, seed)
	if err != nil {
		t.Fatal(err)
	}

	if m1.Fingerprint() != m2.Fingerprint() {
		t.Errorf("deterministic CA should have same fingerprint:\n  %s\n  %s",
			m1.Fingerprint(), m2.Fingerprint())
	}
}

func TestGenerateCA_Random(t *testing.T) {
	dir := t.TempDir()
	m, err := GenerateCA(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Fingerprint() == "" {
		t.Error("fingerprint should not be empty")
	}
	if len(m.PEM()) == 0 {
		t.Error("PEM should not be empty")
	}
	if m.Cert().IsCA != true {
		t.Error("should be a CA certificate")
	}
}

func TestNewManager_LoadExisting(t *testing.T) {
	dir := t.TempDir()
	m1, _ := GenerateCA(dir, []byte("test-seed"))

	m2, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m1.Fingerprint() != m2.Fingerprint() {
		t.Error("loaded CA should have same fingerprint")
	}
}

func TestNewManager_Missing(t *testing.T) {
	_, err := NewManager(t.TempDir())
	if err == nil {
		t.Error("expected error for missing CA file")
	}
}

func TestMobileConfig(t *testing.T) {
	dir := t.TempDir()
	m, _ := GenerateCA(dir, []byte("test"))

	cfg, err := m.MobileConfig()
	if err != nil {
		t.Fatal(err)
	}
	s := string(cfg)
	if !contains(s, "mitm-proxy") {
		t.Error("mobileconfig should contain mitm-proxy")
	}
	if !contains(s, "PayloadContent") {
		t.Error("mobileconfig should contain PayloadContent")
	}
}

func TestFingerprint(t *testing.T) {
	m, _ := GenerateCA(t.TempDir(), []byte("test"))
	fp := m.Fingerprint()
	if len(fp) < 10 {
		t.Errorf("fingerprint too short: %s", fp)
	}
}

func TestCheckTrust(t *testing.T) {
	m, _ := GenerateCA(t.TempDir(), []byte("test"))
	results := m.CheckTrust()
	// 测试环境不应该有信任
	for _, r := range results {
		if r.Trusted {
			t.Logf("unexpected trust in test env: %s", r.Name)
		}
	}
}

func TestCAValidity(t *testing.T) {
	m, _ := GenerateCA(t.TempDir(), []byte("test"))
	cert := m.Cert()
	if cert.NotAfter.Before(cert.NotBefore) {
		t.Error("cert validity range invalid")
	}
	// 应至少 9 年有效期
	if cert.NotAfter.Sub(cert.NotBefore).Hours() < 9*365*24 {
		t.Error("cert validity too short")
	}
}

func TestPEMFormat(t *testing.T) {
	m, _ := GenerateCA(t.TempDir(), []byte("test"))
	pem := m.PEM()
	if len(pem) == 0 {
		t.Fatal("PEM empty")
	}
	// 检查 PEM 头尾
	s := string(pem)
	if !contains(s, "-----BEGIN CERTIFICATE-----") {
		t.Error("missing PEM header")
	}
	if !contains(s, "-----END CERTIFICATE-----") {
		t.Error("missing PEM footer")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && findStr(s, substr)
}

func findStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func BenchmarkGenerateCA(b *testing.B) {
	dir := b.TempDir()
	seed := []byte("bench")
	for i := 0; i < b.N; i++ {
		os.Remove(filepath.Join(dir, "ca.crt"))
		GenerateCA(dir, seed)
	}
}
