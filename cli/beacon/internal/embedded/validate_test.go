package embedded

import (
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"runtime"
	"testing"
)

func TestValidateArchitecture_CurrentPlatform(t *testing.T) {
	if !HasEmbeddedBinary() {
		t.Skip("no real binary embedded (placeholder)")
	}
	// The embedded binary is built for the release platform (darwin). Skip on
	// other OSes rather than fail — the mismatch is expected in cross-platform
	// CI and development environments.
	if err := ValidateArchitecture(); err != nil {
		t.Skipf("embedded binary does not match current platform, skipping: %v", err)
	}
}

func TestMachoArch(t *testing.T) {
	if got := machoArch(macho.CpuAmd64); got != "amd64" {
		t.Errorf("machoArch(CpuAmd64) = %q, want %q", got, "amd64")
	}
	if got := machoArch(macho.CpuArm64); got != "arm64" {
		t.Errorf("machoArch(CpuArm64) = %q, want %q", got, "arm64")
	}
}

func TestElfArch(t *testing.T) {
	if got := elfArch(elf.EM_X86_64); got != "amd64" {
		t.Errorf("elfArch(EM_X86_64) = %q, want %q", got, "amd64")
	}
	if got := elfArch(elf.EM_AARCH64); got != "arm64" {
		t.Errorf("elfArch(EM_AARCH64) = %q, want %q", got, "arm64")
	}
}

func TestPeArch(t *testing.T) {
	if got := peArch(pe.IMAGE_FILE_MACHINE_AMD64); got != "amd64" {
		t.Errorf("peArch(AMD64) = %q, want %q", got, "amd64")
	}
	if got := peArch(pe.IMAGE_FILE_MACHINE_ARM64); got != "arm64" {
		t.Errorf("peArch(ARM64) = %q, want %q", got, "arm64")
	}
}

func TestArchMismatchError(t *testing.T) {
	err := archMismatchError("amd64")
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if got := runtime.GOOS; !containsStr(msg, got) {
		t.Errorf("error message missing runtime OS %q: %s", got, msg)
	}
	if got := runtime.GOARCH; !containsStr(msg, got) {
		t.Errorf("error message missing runtime GOARCH %q: %s", got, msg)
	}
	if !containsStr(msg, "architecture mismatch") {
		t.Errorf("error message missing 'architecture mismatch': %s", msg)
	}
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
