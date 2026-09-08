// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
)

const (
	manifestResourceType           = 24
	manifestResourceID             = 1
	manifestLanguageID             = 1033
	manifestDataOffset             = 88
	resourceDataEntryOffset        = 72
	resourceAlignment              = 8
	resourceSectionCharacteristics = 0x40000040
	subdirectoryOffset             = 0x80000000
	staticSymbolStorageClass       = 3
	relocationI386Dir32NB          = 7
	relocationAMD64Addr32NB        = 3
	relocationARM64Addr32NB        = 2
	assemblyNamespace              = "urn:schemas-microsoft-com:asm.v1"
	trustNamespace                 = "urn:schemas-microsoft-com:asm.v3"
)

var supportedArchitectures = []string{"386", "amd64", "arm64"}

type resourceDirectoryTable struct {
	Characteristics     uint32
	TimeDateStamp       uint32
	MajorVersion        uint16
	MinorVersion        uint16
	NumberOfNameEntries uint16
	NumberOfIDEntries   uint16
}

type resourceDirectoryEntry struct {
	ID     uint32
	Offset uint32
}

type resourceDataEntry struct {
	DataRVA  uint32
	Size     uint32
	Codepage uint32
	Reserved uint32
}

func main() {
	manifestPath := flag.String("manifest", "", "path to the application manifest")
	architectures := flag.String("arch", "", "comma-separated target architectures; defaults to 386,amd64,arm64")
	outputPrefix := flag.String("output-prefix", "rsrc", "output prefix, or a directory ending in a path separator")
	flag.Parse()

	if err := generate(*manifestPath, *architectures, *outputPrefix); err != nil {
		log.Fatal(err)
	}
}

func generate(manifestPath, architectureList, outputPrefix string) error {
	if manifestPath == "" {
		return fmt.Errorf("-manifest is required")
	}
	outputPrefix, err := normalizeOutputPrefix(outputPrefix)
	if err != nil {
		return err
	}

	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("reading manifest: %w", err)
	}
	manifest = canonicalizeManifest(manifest)
	if err := validateManifest(manifest); err != nil {
		return err
	}

	architectures, err := parseArchitectures(architectureList)
	if err != nil {
		return err
	}
	type output struct {
		path string
		data []byte
	}
	outputs := make([]output, 0, len(architectures))
	for _, architecture := range architectures {
		object, err := buildObject(manifest, architecture)
		if err != nil {
			return err
		}
		outputs = append(outputs, output{outputPath(outputPrefix, architecture), object})
	}
	if err := os.MkdirAll(filepath.Dir(outputPrefix), 0o777); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}
	for _, output := range outputs {
		if err := os.WriteFile(output.path, output.data, 0o666); err != nil {
			return fmt.Errorf("writing %s: %w", output.path, err)
		}
	}
	return nil
}

func canonicalizeManifest(manifest []byte) []byte {
	manifest = bytes.ReplaceAll(manifest, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(manifest, []byte("\r"), []byte("\n"))
}

func normalizeOutputPrefix(prefix string) (string, error) {
	if strings.TrimSpace(prefix) == "" {
		return "", fmt.Errorf("-output-prefix must not be empty")
	}
	if os.IsPathSeparator(prefix[len(prefix)-1]) {
		prefix = filepath.Join(prefix, "rsrc")
	}
	if strings.HasSuffix(strings.ToLower(prefix), ".syso") {
		return "", fmt.Errorf("-output-prefix must not end in .syso")
	}
	return prefix, nil
}

func parseArchitectures(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return append([]string(nil), supportedArchitectures...), nil
	}

	parts := strings.Split(value, ",")
	architectures := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		architecture := strings.TrimSpace(part)
		if architecture == "" {
			return nil, fmt.Errorf("-arch contains an empty architecture")
		}
		if _, _, err := architectureValues(architecture); err != nil {
			return nil, err
		}
		if seen[architecture] {
			return nil, fmt.Errorf("-arch contains duplicate architecture %q", architecture)
		}
		seen[architecture] = true
		architectures = append(architectures, architecture)
	}
	return architectures, nil
}

func outputPath(prefix, architecture string) string {
	return fmt.Sprintf("%s_windows_%s.syso", prefix, architecture)
}

func validateManifest(manifest []byte) error {
	decoder := xml.NewDecoder(bytes.NewReader(manifest))
	assemblyFound := false
	executionLevelFound := false

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("parsing manifest: %w", err)
		}

		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "assembly":
			if assemblyFound || start.Name.Space != assemblyNamespace || attribute(start, "manifestVersion") != "1.0" {
				return fmt.Errorf("manifest must contain one asm.v1 assembly with manifestVersion 1.0")
			}
			assemblyFound = true
		case "requestedExecutionLevel":
			if executionLevelFound || start.Name.Space != trustNamespace {
				return fmt.Errorf("manifest must contain one asm.v3 requestedExecutionLevel")
			}
			if attribute(start, "level") != "asInvoker" || attribute(start, "uiAccess") != "false" {
				return fmt.Errorf("requestedExecutionLevel must use level=asInvoker and uiAccess=false")
			}
			executionLevelFound = true
		}
	}

	if !assemblyFound || !executionLevelFound {
		return fmt.Errorf("manifest is missing the required assembly or requestedExecutionLevel")
	}
	return nil
}

func attribute(element xml.StartElement, name string) string {
	for _, attribute := range element.Attr {
		if attribute.Name.Space == "" && attribute.Name.Local == name {
			return attribute.Value
		}
	}
	return ""
}

func buildObject(manifest []byte, architecture string) ([]byte, error) {
	machine, relocationType, err := architectureValues(architecture)
	if err != nil {
		return nil, err
	}
	if uint64(len(manifest)) > uint64(1<<32-1-manifestDataOffset) {
		return nil, fmt.Errorf("manifest is too large")
	}

	resourceSection, err := buildResourceSection(manifest)
	if err != nil {
		return nil, err
	}
	headerSize := binary.Size(pe.FileHeader{}) + binary.Size(pe.SectionHeader32{})
	relocationOffset := headerSize + len(resourceSection)
	symbolTableOffset := relocationOffset + binary.Size(pe.Reloc{})

	fileHeader := pe.FileHeader{
		Machine:              machine,
		NumberOfSections:     1,
		PointerToSymbolTable: uint32(symbolTableOffset),
		NumberOfSymbols:      1,
	}
	sectionHeader := pe.SectionHeader32{
		Name:                 [8]uint8{'.', 'r', 's', 'r', 'c'},
		SizeOfRawData:        uint32(len(resourceSection)),
		PointerToRawData:     uint32(headerSize),
		PointerToRelocations: uint32(relocationOffset),
		NumberOfRelocations:  1,
		Characteristics:      resourceSectionCharacteristics,
	}
	relocation := pe.Reloc{
		VirtualAddress:   resourceDataEntryOffset,
		SymbolTableIndex: 0,
		Type:             relocationType,
	}
	symbol := pe.COFFSymbol{
		Name:          [8]uint8{'.', 'r', 's', 'r', 'c'},
		SectionNumber: 1,
		StorageClass:  staticSymbolStorageClass,
	}

	var object bytes.Buffer
	for _, value := range []any{fileHeader, sectionHeader} {
		if err := binary.Write(&object, binary.LittleEndian, value); err != nil {
			return nil, err
		}
	}
	object.Write(resourceSection)
	for _, value := range []any{relocation, symbol, uint32(4)} {
		if err := binary.Write(&object, binary.LittleEndian, value); err != nil {
			return nil, err
		}
	}
	return object.Bytes(), nil
}

func architectureValues(architecture string) (uint16, uint16, error) {
	switch architecture {
	case "386":
		return pe.IMAGE_FILE_MACHINE_I386, relocationI386Dir32NB, nil
	case "amd64":
		return pe.IMAGE_FILE_MACHINE_AMD64, relocationAMD64Addr32NB, nil
	case "arm64":
		return pe.IMAGE_FILE_MACHINE_ARM64, relocationARM64Addr32NB, nil
	default:
		return 0, 0, fmt.Errorf("unsupported architecture %q", architecture)
	}
}

func buildResourceSection(manifest []byte) ([]byte, error) {
	directory := resourceDirectoryTable{NumberOfIDEntries: 1}
	entries := []resourceDirectoryEntry{
		{ID: manifestResourceType, Offset: subdirectoryOffset | 24},
		{ID: manifestResourceID, Offset: subdirectoryOffset | 48},
		{ID: manifestLanguageID, Offset: resourceDataEntryOffset},
	}
	dataEntry := resourceDataEntry{DataRVA: manifestDataOffset, Size: uint32(len(manifest))}

	var section bytes.Buffer
	for index, entry := range entries {
		if err := binary.Write(&section, binary.LittleEndian, directory); err != nil {
			return nil, err
		}
		if err := binary.Write(&section, binary.LittleEndian, entry); err != nil {
			return nil, err
		}
		if index == len(entries)-1 {
			if err := binary.Write(&section, binary.LittleEndian, dataEntry); err != nil {
				return nil, err
			}
		}
	}
	if section.Len() != manifestDataOffset {
		return nil, fmt.Errorf("internal resource layout error: manifest offset is %d, want %d", section.Len(), manifestDataOffset)
	}
	section.Write(manifest)
	for section.Len()%resourceAlignment != 0 {
		section.WriteByte(0)
	}
	return section.Bytes(), nil
}
