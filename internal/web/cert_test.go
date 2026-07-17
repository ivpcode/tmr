package web

import (
	"bytes"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseAddr(t *testing.T) {
	good := map[string]string{
		"9000":               ":9000",
		"127.0.0.1:9000":     "127.0.0.1:9000",
		"192.168.1.234:8443": "192.168.1.234:8443",
		":8443":              ":8443",
		"[::1]:9000":         "[::1]:9000",
	}
	for in, want := range good {
		got, err := ParseAddr(in)
		if err != nil || got != want {
			t.Errorf("ParseAddr(%q) = %q, %v — want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "127.0.0.1", "0", "70000", "host:", "host:abc"} {
		if got, err := ParseAddr(bad); err == nil {
			t.Errorf("ParseAddr(%q) = %q, want error", bad, got)
		}
	}
}

func TestCertGenerationAndPersistence(t *testing.T) {
	dir := t.TempDir()

	cert1, path1, err := loadOrCreateCert(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path1) != dir {
		t.Errorf("cert path %q not in %q", path1, dir)
	}
	// The private key must not be world-readable.
	info, err := os.Stat(filepath.Join(dir, "web-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key permissions = %v, want 0600", info.Mode().Perm())
	}

	// The certificate must be valid for localhost for years.
	parsed, err := x509.ParseCertificate(cert1.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := parsed.VerifyHostname("localhost"); err != nil {
		t.Errorf("cert does not cover localhost: %v", err)
	}
	if parsed.NotAfter.Before(time.Now().AddDate(9, 0, 0)) {
		t.Errorf("cert expires too soon: %v", parsed.NotAfter)
	}

	// A second load must reuse the same certificate, not mint a new one.
	cert2, _, err := loadOrCreateCert(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cert1.Certificate[0], cert2.Certificate[0]) {
		t.Error("second load generated a different certificate")
	}
}
