package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveFromEnvironmentOrFile(t *testing.T) {
	t.Setenv("TEST_SECRET", "env-value")
	t.Setenv("TEST_SECRET_FILE", "")
	value, err := Resolve("TEST_SECRET", "TEST_SECRET_FILE")
	if err != nil || value != "env-value" {
		t.Fatalf("env value=%q err=%v", value, err)
	}

	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("file-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_SECRET", "")
	t.Setenv("TEST_SECRET_FILE", path)
	value, err = Resolve("TEST_SECRET", "TEST_SECRET_FILE")
	if err != nil || value != "file-value" {
		t.Fatalf("file value=%q err=%v", value, err)
	}
}

func TestResolveRejectsAmbiguousAndInsecureFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("private"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_SECRET", "value")
	t.Setenv("TEST_SECRET_FILE", path)
	if _, err := Resolve("TEST_SECRET", "TEST_SECRET_FILE"); err == nil || !strings.Contains(err.Error(), "不能同时配置") {
		t.Fatalf("ambiguous err=%v", err)
	}
	t.Setenv("TEST_SECRET", "")
	if _, err := Resolve("TEST_SECRET", "TEST_SECRET_FILE"); err == nil || !strings.Contains(err.Error(), "权限过宽") {
		t.Fatalf("permissions err=%v", err)
	}
}
