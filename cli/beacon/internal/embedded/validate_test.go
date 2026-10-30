package embedded

import (
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"testing"
)

func TestValidateArchitecture_CurrentPlatform(t *testing.T) {
	if !HasEmbeddedBinary() {
		t.Skip("no real binary embedded (placeholder)")
	}

	if err := ValidateArchitecture(); err != nil {
		t.Fatalf("embedded hooks binary should match current platform: %v", err)
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
