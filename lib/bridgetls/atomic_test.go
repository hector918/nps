package bridgetls

import (
	"os"
	"path/filepath"
	"testing"
)

// A start that died between the two files must not leave the server unable
// to start: the stray key is replaced, not trusted.
func TestStrayKeyWithoutCertificateIsReplaced(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bridge.key"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, fp, err := LoadOrCreate(dir); err != nil || fp == "" {
		t.Fatalf("LoadOrCreate over a stray key: %q %v", fp, err)
	}
	// and no temporary files are left behind
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.Name() != "bridge.key" && e.Name() != "bridge.crt" {
			t.Errorf("stray file %s", e.Name())
		}
	}
	if fi, _ := os.Stat(filepath.Join(dir, "bridge.key")); fi.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v, want 0600", fi.Mode().Perm())
	}
}
