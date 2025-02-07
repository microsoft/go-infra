// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

const description = `
json2junit converts a JSON file with Go test output to a JUnit XML file.
`

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "\nUsage:\n")
		flag.PrintDefaults()
		fmt.Fprintf(flag.CommandLine.Output(), "%s\n\n", description)
	}
	in := flag.String("in", "", "input file")
	out := flag.String("out", "", "output file")
	flag.Parse()

	if *in == "" || *out == "" {
		flag.Usage()
		os.Exit(1)
	}
	if err := run(*in, *out); err != nil {
		log.Fatalln(err)
	}
}

func run(in, out string) (err error) {
	entries, err := parseJson(in)
	if err != nil {
		return fmt.Errorf("failed to parse JSON: %v", err)
	}
	junit, err := convertJsonToJUnit(entries)
	if err != nil {
		return fmt.Errorf("failed to convert to JUnit: %v", err)
	}
	f, err := os.Create(out)
	if err != nil {
		return fmt.Errorf("failed to create output file: %v", err)
	}
	defer func() {
		if errClose := f.Close(); err == nil && errClose != nil {
			err = fmt.Errorf("failed to close output file: %v", errClose)
		}
	}()
	if err := encodeJUnit(f, junit); err != nil {
		return fmt.Errorf("failed to encode JUnit: %v", err)
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

type junitTestSuites struct {
	XMLName    xml.Name `xml:"testsuites"`
	Tests      int      `xml:"tests,attr"`
	Failures   int      `xml:"failures,attr"`
	Skipped    int      `xml:"skipped,attr"`
	TestSuites []*junitTestSuite
}

type junitTestSuite struct {
	XMLName   xml.Name   `xml:"testsuite"`
	Name      string     `xml:"name,attr"`
	ID        string     `xml:"id,attr"`
	Tests     int        `xml:"tests,attr"`
	Failures  int        `xml:"failures,attr"`
	Skipped   int        `xml:"skipped,attr"`
	Time      float64    `xml:"time,attr"`
	Timestamp string     `xml:"timestamp,attr"`
	SystemOut *systemOut `xml:",omitempty"`
	TestCases []*junitTestCase
}

func (s junitTestSuite) testCase(name string) *junitTestCase {
	for _, tc := range s.TestCases {
		if tc.Name == name {
			return tc
		}
	}
	return nil
}

type systemOut struct {
	XMLName xml.Name `xml:"system-out"`
	Content string   `xml:",cdata"`
}

type junitTestCase struct {
	XMLName   xml.Name    `xml:"testcase"`
	Name      string      `xml:"name,attr"`
	Classname string      `xml:"classname,attr"`
	Time      float64     `xml:"time,attr"`
	Event     *junitEvent `xml:",omitempty"`
	systemOut string      // Placeholder for the event output.
}

type junitEvent struct {
	XMLName xml.Name
	Message string `xml:"message,attr"`
	Content string `xml:",cdata"`
}

// parseJson parses a JSON file with Go test output.
func parseJson(path string) ([]jsonEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var entries []jsonEntry
	decoder := json.NewDecoder(f)
	for {
		var entry jsonEntry
		if err := decoder.Decode(&entry); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// convertJsonToJUnit converts a slice of JSON entries to a JUnit XML test suites.
func convertJsonToJUnit(entries []jsonEntry) (*junitTestSuites, error) {
	cache := make(map[string]*junitTestSuite)
	var suites junitTestSuites
	for _, entry := range entries {
		switch entry.Action {
		case "start":
			// start a new test suite.
			if _, ok := cache[entry.Package]; ok {
				return nil, fmt.Errorf("duplicate start entry for %v", entry.Package)
			}
			suite := &junitTestSuite{
				Name:      entry.Package,
				ID:        strconv.Itoa(len(cache)),
				Timestamp: entry.Time.Format(time.RFC3339),
			}
			cache[entry.Package] = suite
			suites.TestSuites = append(suites.TestSuites, suite)
		case "run":
			// start a new test case.
			suite, ok := cache[entry.Package]
			if !ok {
				return nil, fmt.Errorf("no start entry for %v", entry.Package)
			}
			testCase := suite.testCase(entry.Test)
			if testCase == nil {
				testCase = &junitTestCase{
					Name:      entry.Test,
					Classname: entry.Package,
				}
				suite.TestCases = append(suite.TestCases, testCase)
			}
		case "output":
			// append output to the current test case or suite.
			suite, ok := cache[entry.Package]
			if !ok {
				return nil, fmt.Errorf("no start entry for %v", entry.Package)
			}
			out, ok := strings.CutPrefix(entry.Output, "    ")
			if !ok {
				// Ignore all lines that don't start with "    " (4 spaces),
				// because they are not part of the test output but to the
				// Go test runner.
				break
			}
			if entry.Test == "" {
				if suite.SystemOut == nil {
					suite.SystemOut = new(systemOut)
				}
				suite.SystemOut.Content += out + "\n"
				break
			}
			testCase := suite.testCase(entry.Test)
			if testCase == nil {
				return nil, fmt.Errorf("no run entry for %v", entry.Test)
			}
			testCase.systemOut += out + "\n"
		case "pass", "skip", "fail":
			// finish the current test case or suite.
			suite, ok := cache[entry.Package]
			if !ok {
				return nil, fmt.Errorf("no start entry for %v", entry.Package)
			}
			if entry.Test == "" {
				suite.Time = entry.Elapsed
				break
			}
			testCase := suite.testCase(entry.Test)
			if testCase == nil {
				return nil, fmt.Errorf("no run entry for %v", entry.Test)
			}
			testCase.Time = entry.Elapsed
			switch entry.Action {
			case "skip":
				testCase.Event = &junitEvent{
					XMLName: xml.Name{Space: "", Local: "skipped"},
					Message: "skipped",
					Content: testCase.systemOut,
				}
			case "fail":
				testCase.Event = &junitEvent{
					XMLName: xml.Name{Space: "", Local: "failure"},
					Message: "failed",
					Content: testCase.systemOut,
				}
			}
			// Clear systemOut, because it's already in the event.
			// In case of success, we don't care about the output.
			testCase.systemOut = ""
		case "pause", "cont":
			// Ignore.
		default:
			return nil, fmt.Errorf("unknown action: %v", entry.Action)
		}
	}

	// Calculate stats.
	for _, suite := range suites.TestSuites {
		for _, tc := range suite.TestCases {
			suites.Tests++
			suite.Tests++
			if tc.Event == nil {
				continue
			}
			switch tc.Event.Message {
			case "failed":
				suites.Failures++
				suite.Failures++
				tc.Event.Content = strings.TrimSpace(tc.Event.Content)
			case "skipped":
				suites.Skipped++
				suite.Skipped++
				tc.Event.Content = strings.TrimSpace(tc.Event.Content)
			default:
				panic("unknown event message: " + tc.Event.Message)
			}
		}
		if suite.SystemOut != nil {
			if suite.Failures == 0 && suite.Skipped == 0 {
				// No failures or skips, so we don't care about the system-out.
				suite.SystemOut = nil
			} else {
				// Remove the last newline.
				suite.SystemOut.Content = strings.TrimSpace(suite.SystemOut.Content)
			}
		}

	}
	return &suites, nil
}

// encodeJUnit encodes a JUnit XML test suites to the given writer.
func encodeJUnit(w io.Writer, suites *junitTestSuites) error {
	_, err := w.Write([]byte(xml.Header))
	if err != nil {
		return err
	}
	encoder := xml.NewEncoder(w)
	encoder.Indent("", "\t")
	return encoder.Encode(suites)
}
