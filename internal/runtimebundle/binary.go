// GeneralsX @feature Codex 10/10/2026 Inspect runtime binary architectures and imports without executing downloaded code.
package runtimebundle

import (
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ValidateRuntime checks portable binary headers and direct imports for every
// bundled executable/library. Platform exporters also validate native loader
// resolution and ABI requirements before creating a published package.
func ValidateRuntime(root, target string) error {
	var binaries []string
	available := map[string]bool{}
	err := filepath.WalkDir(root, func(full string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, full)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if err := validatePath(rel); err != nil {
			return err
		}
		if err := validateRuntimePath(rel, target); err != nil {
			return err
		}
		if strings.HasPrefix(rel, "licenses/") || strings.HasPrefix(rel, "fontconfig/") || rel == "build-metadata.json" {
			return nil
		}
		lower := strings.ToLower(rel)
		if rel == "GeneralsXZH" || rel == "generalszh.exe" || strings.HasPrefix(rel, "lib/") || strings.HasSuffix(lower, ".dll") {
			available[strings.ToLower(path.Base(rel))] = true
			if entry.Type()&os.ModeSymlink == 0 {
				binaries = append(binaries, full)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	engine := "GeneralsXZH"
	if target == "windows-x86" {
		engine = "generalszh.exe"
	}
	if !available[strings.ToLower(engine)] {
		return errors.New("runtime engine executable is missing")
	}
	for _, binary := range binaries {
		imports, err := inspectBinary(binary, target)
		if err != nil {
			return fmt.Errorf("runtime binary %s: %w", filepath.Base(binary), err)
		}
		for _, imported := range imports {
			if target == "linux-amd64" && strings.ContainsAny(imported, "/\\") {
				return fmt.Errorf("runtime %s imports an absolute or nested ELF path %s", filepath.Base(binary), imported)
			}
			if systemImport(imported, target) {
				continue
			}
			if !available[strings.ToLower(path.Base(imported))] {
				return fmt.Errorf("runtime %s has unbundled dependency %s", filepath.Base(binary), imported)
			}
		}
	}
	return nil
}

// ValidateRetailWindowsLibraries closes the two retail-only DLL exceptions
// after local assembly. Public runtime validation deliberately cannot inspect
// these files because they must never be uploaded in the runtime package.
func ValidateRetailWindowsLibraries(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	available := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".dll") {
			available[strings.ToLower(entry.Name())] = entry.Name()
		}
	}
	for _, retail := range []string{"binkw32.dll", "mss32.dll"} {
		name, exists := available[retail]
		if !exists {
			return fmt.Errorf("required retail Windows x86 library %s is missing", retail)
		}
		imports, err := inspectBinary(filepath.Join(root, name), "windows-x86")
		if err != nil {
			return fmt.Errorf("retail library %s: %w", retail, err)
		}
		for _, dependency := range imports {
			if systemImport(dependency, "windows-x86") {
				continue
			}
			if _, exists := available[strings.ToLower(dependency)]; !exists {
				return fmt.Errorf("retail library %s needs unbundled Windows dependency %s", retail, dependency)
			}
		}
	}
	return nil
}

func inspectBinary(filename, target string) ([]string, error) {
	switch target {
	case "windows-x86":
		binary, err := pe.Open(filename)
		if err != nil {
			return nil, err
		}
		defer binary.Close()
		if binary.Machine != pe.IMAGE_FILE_MACHINE_I386 {
			return nil, errors.New("expected Windows x86 PE image")
		}
		if _, ok := binary.OptionalHeader.(*pe.OptionalHeader32); !ok {
			return nil, errors.New("expected PE32 optional header")
		}
		return peLibraries(binary)
	case "linux-amd64":
		binary, err := elf.Open(filename)
		if err != nil {
			return nil, err
		}
		defer binary.Close()
		if binary.Class != elf.ELFCLASS64 || binary.Machine != elf.EM_X86_64 || binary.Data != elf.ELFDATA2LSB || binary.Type != elf.ET_DYN && binary.Type != elf.ET_EXEC {
			return nil, errors.New("expected Linux AMD64 ELF image")
		}
		return binary.ImportedLibraries()
	case "macos-arm64":
		binary, err := macho.Open(filename)
		if err == nil {
			defer binary.Close()
			if binary.Cpu != macho.CpuArm64 {
				return nil, errors.New("expected ARM64 Mach-O image")
			}
			return binary.ImportedLibraries()
		}
		fat, fatErr := macho.OpenFat(filename)
		if fatErr != nil {
			return nil, err
		}
		defer fat.Close()
		for _, arch := range fat.Arches {
			if arch.Cpu == macho.CpuArm64 {
				return arch.File.ImportedLibraries()
			}
		}
		return nil, errors.New("Mach-O image has no ARM64 slice")
	default:
		return nil, fmt.Errorf("unsupported runtime target %q", target)
	}
}

// debug/pe.ImportedLibraries is unimplemented in Go 1.25. Read import and
// delay-import descriptors directly, including DLLs imported only by ordinal.
func peLibraries(file *pe.File) ([]string, error) {
	header := file.OptionalHeader.(*pe.OptionalHeader32)
	readRVA := func(address uint32, size uint32) ([]byte, error) {
		for _, section := range file.Sections {
			if address >= section.VirtualAddress && uint64(address-section.VirtualAddress)+uint64(size) <= uint64(section.Size) {
				data := make([]byte, size)
				_, err := section.ReadAt(data, int64(address-section.VirtualAddress))
				return data, err
			}
		}
		return nil, errors.New("PE import RVA escapes file sections")
	}
	var libraries []string
	for _, index := range []int{1, 13} {
		directory := header.DataDirectory[index]
		if directory.VirtualAddress == 0 {
			continue
		}
		width, nameOffset := uint32(20), 4*3
		if index == 13 {
			width, nameOffset = 32, 4
		}
		terminated := false
		for offset := uint32(0); uint64(offset)+uint64(width) <= uint64(directory.Size) && offset/width < 4096; offset += width {
			if uint64(directory.VirtualAddress)+uint64(offset) > 0xffffffff {
				return nil, errors.New("PE import RVA overflow")
			}
			record, err := readRVA(directory.VirtualAddress+offset, width)
			if err != nil {
				return nil, err
			}
			allZero := true
			for _, value := range record {
				if value != 0 {
					allZero = false
					break
				}
			}
			if allZero {
				terminated = true
				break
			}
			address := binary.LittleEndian.Uint32(record[nameOffset : nameOffset+4])
			if index == 13 && binary.LittleEndian.Uint32(record[:4])&1 == 0 {
				if address < header.ImageBase {
					return nil, errors.New("invalid delay import address")
				}
				address -= header.ImageBase
			}
			var name []byte
			for i := uint32(0); i < 260; i++ {
				if uint64(address)+uint64(i) > 0xffffffff {
					return nil, errors.New("PE DLL name RVA overflow")
				}
				value, err := readRVA(address+i, 1)
				if err != nil {
					return nil, err
				}
				if value[0] == 0 {
					break
				}
				name = append(name, value[0])
			}
			if len(name) == 0 || len(name) == 260 || strings.ContainsAny(string(name), "/\\:") {
				return nil, errors.New("invalid PE imported DLL name")
			}
			libraries = append(libraries, string(name))
		}
		if !terminated {
			return nil, errors.New("unterminated PE import table")
		}
	}
	return libraries, nil
}

func systemImport(name, target string) bool {
	switch target {
	case "linux-amd64":
		switch name {
		case "libc.so.6", "libm.so.6", "libpthread.so.0", "librt.so.1", "libdl.so.2", "ld-linux-x86-64.so.2":
			return true
		}
	case "macos-arm64":
		return strings.HasPrefix(name, "/System/Library/") || strings.HasPrefix(name, "/usr/lib/")
	case "windows-x86":
		name = strings.ToLower(name)
		if strings.HasPrefix(name, "api-ms-win-") || strings.HasPrefix(name, "ext-ms-win-") {
			return true
		}
		// Bink and Miles are required from the user's retail installation during
		// local assembly. They are deliberately absent from public packages.
		for _, system := range strings.Fields("binkw32.dll mss32.dll kernel32.dll kernelbase.dll ntdll.dll user32.dll gdi32.dll gdi32full.dll advapi32.dll shell32.dll shlwapi.dll ole32.dll oleaut32.dll combase.dll comdlg32.dll comctl32.dll uuid.dll rpcrt4.dll ws2_32.dll wsock32.dll winmm.dll version.dll imm32.dll winspool.drv crypt32.dll cryptbase.dll cryptnet.dll bcrypt.dll bcryptprimitives.dll ncrypt.dll secur32.dll sspicli.dll normaliz.dll winhttp.dll wininet.dll wldap32.dll iphlpapi.dll dnsapi.dll dhcpcsvc.dll netapi32.dll userenv.dll uxtheme.dll dwmapi.dll setupapi.dll cfgmgr32.dll powrprof.dll shcore.dll psapi.dll dbghelp.dll avifil32.dll avicap32.dll msacm32.dll msvfw32.dll dinput.dll dinput8.dll d3d8.dll d3d9.dll d3d11.dll dxgi.dll dsound.dll opengl32.dll msvcrt.dll ucrtbase.dll") {
			if name == system {
				return true
			}
		}
	}
	return false
}
