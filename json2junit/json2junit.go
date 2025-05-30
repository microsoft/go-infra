// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package json2junit converts JSON Go test output to JUnit XML format.
package json2junit

import (
	"bytes"
	"cmp"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// Convert reads Go test JSON and writes converted JUnit XML.
func Convert(w io.Writer, r io.Reader) error {
	c := NewConverter(w)
	if _, err := io.Copy(c, r); err != nil {
		return err
	}
	return c.Close()
}

// ConvertFile reads a Go test JSON file and creates a JUnit XML file with converted content.
// Creates the directory containing the target file if necessary.
func ConvertFile(out, in string) error {
	r, err := os.Open(in)
	if err != nil {
		return err
	}
	defer r.Close()

	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}

	w, err := os.Create(out)
	if err != nil {
		return err
	}

	if err := Convert(w, r); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

// A Converter holds the state of a JSON-to-JUnit conversion.
// It implements io.WriteCloser; call Write with JSON test output,
// then Close. The JUnit output is written to the writer w that was
// passed to NewConverter.
//
// The JSON input is buffered, so the caller can write it in arbitrary
// size chunks that don't have to align with the JSON lines.
// The JUnit output is not written immediately, but chunked into test
// suites which are written to w as soon as they are complete.
type Converter struct {
	b      []byte // input buffer
	suites map[string]*junitTestSuite
	w      io.Writer
	xmlEnc *xml.Encoder
}

// NewConverter returns a "JSON to JUnit" converter.
// Writes on the returned writer are written as JUnit to w,
// with minimal delay.
func NewConverter(w io.Writer) *Converter {
	return &Converter{
		w:      w,
		suites: make(map[string]*junitTestSuite),
	}
}

// Write writes a JSON test entry to the writer.
func (c *Converter) Write(b []byte) (int, error) {
	if c.xmlEnc == nil {
		if err := c.openXML(); err != nil {
			return 0, err
		}
	}
	n := len(b)
	for len(b) > 0 {
		// Search for the next newline.
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			// No newline, so just write the buffer.
			c.b = append(c.b, b...)
			break
		}
		data := b[:i]
		if len(c.b) > 0 {
			data = append(c.b, data...)
			// Reset the buffer.
			c.b = c.b[:0]
		}

		// Unmarshal the JSON.
		// If the JSON is invalid, just ignore it.
		var jsonEntry jsonEntry
		if err := json.Unmarshal(data, &jsonEntry); err == nil {
			// Process the JSON entry.
			if err := c.processJSONEntry(jsonEntry); err != nil {
				return 0, fmt.Errorf("failed to process line: %v", err)
			}
		}

		// Skip the newline.
		b = b[i+1:]
	}
	return n, nil
}

// Close marks the end of the go test output.
func (c *Converter) Close() error {
	if err := c.closeXML(); err != nil {
		return fmt.Errorf("failed to close XML: %v", err)
	}
	return nil
}

// jsonEntry is a single entry in the JSON file.
type jsonEntry struct {
	Time    time.Time
	Action  string
	Package string
	Test    string
	Output  string
	Elapsed float64
}

// The following structs definitions are taken from
// https://llg.cubic.org/docs/junit/.

type junitTestSuite struct {
	XMLName   xml.Name         `xml:"testsuite"`
	Name      string           `xml:"name,attr"`
	Tests     int              `xml:"tests,attr"`
	Failures  int              `xml:"failures,attr"`
	Skipped   int              `xml:"skipped,attr"`
	Time      float64          `xml:"time,attr"`
	Timestamp string           `xml:"timestamp,attr"`
	SystemOut *systemOut       `xml:",omitempty"`
	TestCases []*junitTestCase // Empty until written to XML. Will be filled in with testCases.

	testCases map[string]*junitTestCase `xml:"-"` // Map from test name to *junitTestCase.
}

type systemOut struct {
	XMLName xml.Name `xml:"system-out"`
	Content []byte   `xml:",cdata"`
}

type junitTestCase struct {
	XMLName   xml.Name     `xml:"testcase"`
	Classname string       `xml:"classname,attr"`
	Name      string       `xml:"name,attr"`
	Time      float64      `xml:"time,attr"`
	Result    *junitResult `xml:",omitempty"`

	systemOut []byte // Placeholder for the event output.
}

type junitResult struct {
	XMLName xml.Name
	Message string `xml:"message,attr"`
	Content []byte `xml:",cdata"`
}

// processLine converts a single JSON test line.
func (c *Converter) processJSONEntry(entry jsonEntry) error {
	switch entry.Action {
	case "start":
		// start a new test suite.
		if _, ok := c.suites[entry.Package]; ok {
			return fmt.Errorf("duplicate start entry for %v", entry.Package)
		}
		c.suites[entry.Package] = &junitTestSuite{
			Name:      entry.Package,
			Timestamp: entry.Time.Format(time.RFC3339),
			testCases: make(map[string]*junitTestCase),
		}
	case "run":
		// start a new test case.
		suite, ok := c.suites[entry.Package]
		if !ok {
			return fmt.Errorf("no start entry for %v", entry.Package)
		}
		if _, ok = suite.testCases[entry.Test]; ok {
			return fmt.Errorf("duplicate run entry for package %v test %v", entry.Package, entry.Test)
		}
		suite.Tests++
		suite.testCases[entry.Test] = &junitTestCase{
			Name:      entry.Test,
			Classname: entry.Package,
		}
	case "output":
		// append output to the current test case or suite.
		suite, ok := c.suites[entry.Package]
		if !ok {
			return fmt.Errorf("no start entry for %v", entry.Package)
		}
		if entry.Test == "" {
			if suite.SystemOut == nil {
				suite.SystemOut = new(systemOut)
			}
			suite.SystemOut.Content = append(suite.SystemOut.Content, []byte(entry.Output)...)
		} else {
			testCase, ok := suite.testCases[entry.Test]
			if !ok {
				return fmt.Errorf("no run entry for %v", entry.Test)
			}
			testCase.systemOut = append(testCase.systemOut, []byte(entry.Output)...)
		}
	case "pass", "skip", "fail":
		// finish the current test case or suite.
		suite, ok := c.suites[entry.Package]
		if !ok {
			return fmt.Errorf("no start entry for %v", entry.Package)
		}
		if entry.Test == "" {
			suite.Time = entry.Elapsed
			if entry.Action == "pass" {
				// In case of success, we don't care about the output.
				suite.SystemOut = nil
			} else if suite.SystemOut != nil {
				suite.SystemOut.Content = bytes.TrimSuffix(suite.SystemOut.Content, []byte{'\n'})
			}
			err := c.writeXMLTestSuite(suite)
			if err != nil {
				return fmt.Errorf("failed to write test suite: %v", err)
			}
			// Remove the suite, we are done with it.
			delete(c.suites, entry.Package)
			return nil
		}
		testCase, ok := suite.testCases[entry.Test]
		if !ok {
			return fmt.Errorf("no run entry for %v", entry.Test)
		}
		testCase.Time = entry.Elapsed
		testCase.systemOut = bytes.TrimSuffix(testCase.systemOut, []byte{'\n'})
		switch entry.Action {
		case "skip":
			suite.Skipped++
			testCase.Result = &junitResult{
				XMLName: xml.Name{Space: "", Local: "skipped"},
				Message: "skipped",
				Content: testCase.systemOut,
			}
		case "fail":
			suite.Failures++
			testCase.Result = &junitResult{
				XMLName: xml.Name{Space: "", Local: "failure"},
				Message: "failed",
				Content: testCase.systemOut,
			}
		}
		// Clear systemOut, it's already in the event.
		// In case of success, we don't care about the output.
		testCase.systemOut = nil
	case "pause", "cont":
		// Ignore.
	default:
		// Not a JSON test entry, ignore.
	}
	return nil
}

// writeXMLTestSuite writes a single test suite to the writer.
// Test cases are sorted by name.
func (c *Converter) writeXMLTestSuite(suite *junitTestSuite) error {
	suite.TestCases = make([]*junitTestCase, 0, len(suite.testCases))
	for _, testCase := range suite.testCases {
		suite.TestCases = append(suite.TestCases, testCase)
	}
	slices.SortFunc(suite.TestCases, func(a, b *junitTestCase) int {
		return cmp.Compare(a.Name, b.Name)
	})
	return c.xmlEnc.Encode(suite)
}

func (c *Converter) openXML() error {
	if c.xmlEnc != nil {
		panic("xmlEnc already open")
	}
	_, err := c.w.Write([]byte(xml.Header))
	if err != nil {
		return err
	}
	_, err = c.w.Write([]byte("<testsuites>\n"))
	if err != nil {
		return err
	}
	c.xmlEnc = xml.NewEncoder(c.w)
	c.xmlEnc.Indent("", "\t")
	return nil
}

func (c *Converter) closeXML() error {
	if c.xmlEnc == nil {
		// Write an empty <testsuites> element
		// if there are no test suites.
		if err := c.openXML(); err != nil {
			return err
		}
	}
	_, err := c.w.Write([]byte("\n</testsuites>\n"))
	return err
}
