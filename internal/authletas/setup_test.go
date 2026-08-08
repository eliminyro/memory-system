package authletas

import (
	"strings"
	"testing"
)

func TestLoadMasterKey_MissingEnvErrors(t *testing.T) {
	t.Setenv("AUTHLET_MASTER_KEY", "")

	if _, err := loadMasterKey(); err == nil {
		t.Fatal("expected error for empty AUTHLET_MASTER_KEY")
	} else if !strings.Contains(err.Error(), "AUTHLET_MASTER_KEY") {
		t.Fatalf("err should reference env var, got: %v", err)
	}
}

func TestLoadMasterKey_BadHexErrors(t *testing.T) {
	// "zz" is not valid hex.
	t.Setenv("AUTHLET_MASTER_KEY", "zz")

	if _, err := loadMasterKey(); err == nil {
		t.Fatal("expected error for malformed hex")
	} else if !strings.Contains(err.Error(), "hex") {
		t.Fatalf("err should mention hex, got: %v", err)
	}
}

func TestLoadMasterKey_WrongSizeErrors(t *testing.T) {
	// "deadbeef" decodes to 4 bytes, not 32.
	t.Setenv("AUTHLET_MASTER_KEY", "deadbeef")

	if _, err := loadMasterKey(); err == nil {
		t.Fatal("expected error for 4-byte decoded key")
	} else if !strings.Contains(err.Error(), "32 bytes") {
		t.Fatalf("err should mention 32-byte requirement, got: %v", err)
	}
}

func TestLoadMasterKey_AcceptsValidKey(t *testing.T) {
	// 32 bytes of 0x41 ('A') hex-encoded — "41" × 32 = 64 hex chars.
	t.Setenv("AUTHLET_MASTER_KEY", strings.Repeat("41", 32))

	k, err := loadMasterKey()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(k) != 32 {
		t.Fatalf("got %d bytes, want 32", len(k))
	}
}
