package embedded

import (
	"bytes"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"fmt"
	"runtime"
)

// ValidateArchitecture checks that the embedded hooks binary matches the
// current runtime OS and architecture. Returns an error with a descriptive
// message if there is a mismatch (e.g., an x86_64 binary on an arm64 host).
func ValidateArchitecture() error {
	if len(HooksBinary) < 4 {
		return fmt.Errorf("embedded binary too small to validate")
	}

	reader := bytes.NewReader(HooksBinary)

	// Try Mach-O (macOS)
	if f, err := macho.NewFile(reader); err == nil {
		return validateMachO(f)
	}

	// Try ELF (Linux)
	reader.Reset(HooksBinary)
	if f, err := elf.NewFile(reader); err == nil {
		return validateELF(f)
	}

	// Try PE (Windows)
	reader.Reset(HooksBinary)
	if f, err := pe.NewFile(reader); err == nil {
		return validatePE(f)
	}

	return fmt.Errorf("embedded binary has unrecognized format (not Mach-O, ELF, or PE)")
}

func validateMachO(f *macho.File) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("embedded binary is a macOS Mach-O binary but runtime OS is %s", runtime.GOOS)
	}

	binaryArch := machoArch(f.Cpu)
	if binaryArch != runtime.GOARCH {
		return archMismatchError(binaryArch)
	}
	return nil
}

func validateELF(f *elf.File) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("embedded binary is a Linux ELF binary but runtime OS is %s", runtime.GOOS)
	}

	binaryArch := elfArch(f.Machine)
	if binaryArch != runtime.GOARCH {
		return archMismatchError(binaryArch)
	}
	return nil
}

func validatePE(f *pe.File) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("embedded binary is a Windows PE binary but runtime OS is %s", runtime.GOOS)
	}

	binaryArch := peArch(f.FileHeader.Machine)
	if binaryArch != runtime.GOARCH {
		return archMismatchError(binaryArch)
	}
	return nil
}

func machoArch(cpu macho.Cpu) string {
	switch cpu {
	case macho.CpuAmd64:
		return "amd64"
	case macho.CpuArm64:
		return "arm64"
	default:
		return fmt.Sprintf("unknown(0x%x)", cpu)
	}
}

func elfArch(machine elf.Machine) string {
	switch machine {
	case elf.EM_X86_64:
		return "amd64"
	case elf.EM_AARCH64:
		return "arm64"
	default:
		return fmt.Sprintf("unknown(0x%x)", machine)
	}
}

func peArch(machine uint16) string {
	switch machine {
	case pe.IMAGE_FILE_MACHINE_AMD64:
		return "amd64"
	case pe.IMAGE_FILE_MACHINE_ARM64:
		return "arm64"
	default:
		return fmt.Sprintf("unknown(0x%x)", machine)
	}
}

func archMismatchError(binaryArch string) error {
	return fmt.Errorf(
		"architecture mismatch: embedded hooks binary is %s/%s but this CLI is %s/%s — "+
			"this is a build error, please report it at https://github.com/asymptote-labs/agent-beacon/issues",
		runtime.GOOS, binaryArch, runtime.GOOS, runtime.GOARCH,
	)
}
