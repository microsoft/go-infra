// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

var gitGoPatchDir = filepath.Join("..", "..", "cmd", "git-go-patch")

func TestCheckedInResources(t *testing.T) {
	manifest, err := os.ReadFile(filepath.Join(gitGoPatchDir, "windows", "git-go-patch.exe.manifest"))
	if err != nil {
		t.Fatal(err)
	}
	manifest = canonicalizeManifest(manifest)

	tests := []struct {
		architecture   string
		filename       string
		machine        uint16
		relocationType uint16
	}{
		{"386", outputPath("rsrc", "386"), pe.IMAGE_FILE_MACHINE_I386, relocationI386Dir32NB},
		{"amd64", outputPath("rsrc", "amd64"), pe.IMAGE_FILE_MACHINE_AMD64, relocationAMD64Addr32NB},
		{"arm64", outputPath("rsrc", "arm64"), pe.IMAGE_FILE_MACHINE_ARM64, relocationARM64Addr32NB},
	}
	for _, test := range tests {
		t.Run(test.architecture, func(t *testing.T) {
			want, err := buildObject(manifest, test.architecture)
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(gitGoPatchDir, test.filename))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s is stale; run go generate ./cmd/git-go-patch", test.filename)
			}
			verifyObject(t, got, manifest, test.machine, test.relocationType)
		})
	}
}

func TestCanonicalizeManifest(t *testing.T) {
	got := canonicalizeManifest([]byte("first\r\nsecond\rthird\n"))
	want := []byte("first\nsecond\nthird\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("canonicalized manifest = %q, want %q", got, want)
	}
}

func TestGenerateArchitectureSelection(t *testing.T) {
	manifestPath := filepath.Join(gitGoPatchDir, "windows", "git-go-patch.exe.manifest")
	tests := []struct {
		name          string
		value         string
		architectures []string
	}{
		{"default", "", []string{"386", "amd64", "arm64"}},
		{"comma-separated", "arm64, 386", []string{"arm64", "386"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prefix := filepath.Join(t.TempDir(), "nested", "resources", "custom")
			if err := generate(manifestPath, test.value, prefix); err != nil {
				t.Fatal(err)
			}
			want := make(map[string]bool, len(test.architectures))
			for _, architecture := range test.architectures {
				want[architecture] = true
			}
			for _, architecture := range supportedArchitectures {
				_, err := os.Stat(outputPath(prefix, architecture))
				if want[architecture] && err != nil {
					t.Errorf("expected %s output: %v", architecture, err)
				}
				if !want[architecture] && !os.IsNotExist(err) {
					t.Errorf("unexpected %s output", architecture)
				}
			}
		})
	}
}

func TestGenerateRejectsAmbiguousOutputPrefix(t *testing.T) {
	manifestPath := filepath.Join(gitGoPatchDir, "windows", "git-go-patch.exe.manifest")
	if err := generate(manifestPath, "amd64", filepath.Join(t.TempDir(), "resource.syso")); err == nil {
		t.Fatal("expected an error for an output prefix ending in .syso")
	}
}

func TestGenerateDirectoryOutputPrefix(t *testing.T) {
	manifestPath := filepath.Join(gitGoPatchDir, "windows", "git-go-patch.exe.manifest")
	outputDir := filepath.Join(t.TempDir(), "nested", "resources")
	if err := generate(manifestPath, "amd64", outputDir+string(os.PathSeparator)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "rsrc_windows_amd64.syso")); err != nil {
		t.Fatalf("expected default rsrc prefix under output directory: %v", err)
	}
}

func verifyObject(t *testing.T, object, manifest []byte, machine, relocationType uint16) {
	t.Helper()

	file, err := pe.NewFile(bytes.NewReader(object))
	if err != nil {
		t.Fatalf("parsing COFF object: %v", err)
	}
	defer file.Close()
	if file.Machine != machine || file.OptionalHeader != nil {
		t.Fatalf("unexpected COFF header: machine %#x, optional header %T", file.Machine, file.OptionalHeader)
	}
	if len(file.Sections) != 1 {
		t.Fatalf("COFF object has %d sections, want 1", len(file.Sections))
	}

	section := file.Sections[0]
	if section.Name != ".rsrc" || section.Characteristics != resourceSectionCharacteristics {
		t.Fatalf("unexpected section %q with characteristics %#x", section.Name, section.Characteristics)
	}
	if len(section.Relocs) != 1 {
		t.Fatalf(".rsrc has %d relocations, want 1", len(section.Relocs))
	}
	relocation := section.Relocs[0]
	if relocation.VirtualAddress != resourceDataEntryOffset || relocation.SymbolTableIndex != 0 || relocation.Type != relocationType {
		t.Fatalf("unexpected relocation: %#v", relocation)
	}

	if len(file.COFFSymbols) != 1 {
		t.Fatalf("COFF object has %d symbols, want 1", len(file.COFFSymbols))
	}
	symbol := file.COFFSymbols[0]
	symbolName, err := symbol.FullName(file.StringTable)
	if err != nil {
		t.Fatal(err)
	}
	if symbolName != ".rsrc" || symbol.Value != 0 || symbol.SectionNumber != 1 || symbol.Type != 0 ||
		symbol.StorageClass != staticSymbolStorageClass || symbol.NumberOfAuxSymbols != 0 {

		t.Fatalf("unexpected COFF symbol: %#v", symbol)
	}

	resourceData, err := section.Data()
	if err != nil {
		t.Fatal(err)
	}
	wantSize := manifestDataOffset + len(manifest)
	wantSize = (wantSize + resourceAlignment - 1) &^ (resourceAlignment - 1)
	if len(resourceData) != wantSize {
		t.Fatalf(".rsrc size is %d, want %d", len(resourceData), wantSize)
	}

	checkDirectory(t, resourceData, 0)
	checkUint32(t, resourceData, 16, manifestResourceType)
	checkUint32(t, resourceData, 20, subdirectoryOffset|24)
	checkDirectory(t, resourceData, 24)
	checkUint32(t, resourceData, 40, manifestResourceID)
	checkUint32(t, resourceData, 44, subdirectoryOffset|48)
	checkDirectory(t, resourceData, 48)
	checkUint32(t, resourceData, 64, manifestLanguageID)
	checkUint32(t, resourceData, 68, resourceDataEntryOffset)
	checkUint32(t, resourceData, 72, manifestDataOffset)
	checkUint32(t, resourceData, 76, uint32(len(manifest)))
	checkUint32(t, resourceData, 80, 0)
	checkUint32(t, resourceData, 84, 0)
	if !bytes.Equal(resourceData[manifestDataOffset:manifestDataOffset+len(manifest)], manifest) {
		t.Fatal("RT_MANIFEST payload does not match the source manifest")
	}
	for offset, value := range resourceData[manifestDataOffset+len(manifest):] {
		if value != 0 {
			t.Fatalf("non-zero padding byte at resource offset %d", manifestDataOffset+len(manifest)+offset)
		}
	}
}

func checkDirectory(t *testing.T, data []byte, offset int) {
	t.Helper()
	for fieldOffset := 0; fieldOffset < 14; fieldOffset += 2 {
		if binary.LittleEndian.Uint16(data[offset+fieldOffset:]) != 0 {
			t.Fatalf("non-zero resource directory field at offset %d", offset+fieldOffset)
		}
	}
	if binary.LittleEndian.Uint16(data[offset+14:]) != 1 {
		t.Fatalf("resource directory at offset %d does not have exactly one ID entry", offset)
	}
}

func checkUint32(t *testing.T, data []byte, offset int, want uint32) {
	t.Helper()
	if got := binary.LittleEndian.Uint32(data[offset:]); got != want {
		t.Fatalf("uint32 at resource offset %d is %#x, want %#x", offset, got, want)
	}
}
