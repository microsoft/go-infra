// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package json2junit

import (
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/go-infra/goldentest"
)

func TestConverterRepeatedTests(t *testing.T) {
	const input = `{"Action":"start","Package":"crypto/rand"}
{"Action":"run","Package":"crypto/rand","Test":"TestAllocations"}
{"Action":"output","Package":"crypto/rand","Test":"TestAllocations","Output":"first run\n"}
{"Action":"fail","Package":"crypto/rand","Test":"TestAllocations","Elapsed":1}
{"Action":"run","Package":"crypto/rand","Test":"TestAllocations"}
{"Action":"output","Package":"crypto/rand","Test":"TestAllocations","Output":"second run\n"}
{"Action":"skip","Package":"crypto/rand","Test":"TestAllocations","Elapsed":2}
{"Action":"run","Package":"crypto/rand","Test":"TestAllocations"}
{"Action":"output","Package":"crypto/rand","Test":"TestAllocations","Output":"third run\n"}
{"Action":"pass","Package":"crypto/rand","Test":"TestAllocations","Elapsed":3}
{"Action":"fail","Package":"crypto/rand","Elapsed":6}
`
	for _, includePackage := range []bool{false, true} {
		name := "without_package"
		prefix := ""
		if includePackage {
			name = "with_package"
			prefix = "crypto/rand."
		}
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if err := ConvertWithOptions(&out, strings.NewReader(input), &Options{
				IncludePackageInTestName: includePackage,
			}); err != nil {
				t.Fatal(err)
			}
			type testResult struct {
				Content string `xml:",cdata"`
			}
			var result struct {
				Suites []struct {
					Tests     int `xml:"tests,attr"`
					Failures  int `xml:"failures,attr"`
					Skipped   int `xml:"skipped,attr"`
					TestCases []struct {
						Name      string      `xml:"name,attr"`
						Classname string      `xml:"classname,attr"`
						Time      float64     `xml:"time,attr"`
						Failure   *testResult `xml:"failure"`
						Skipped   *testResult `xml:"skipped"`
					} `xml:"testcase"`
				} `xml:"testsuite"`
			}
			if err := xml.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Suites) != 1 {
				t.Fatalf("got %d suites, want 1", len(result.Suites))
			}
			suite := result.Suites[0]
			if suite.Tests != 3 || suite.Failures != 1 || suite.Skipped != 1 {
				t.Errorf("got tests=%d failures=%d skipped=%d, want 3, 1, 1", suite.Tests, suite.Failures, suite.Skipped)
			}
			if len(suite.TestCases) != 3 {
				t.Fatalf("got %d test cases, want 3", len(suite.TestCases))
			}
			for i, wantName := range []string{"TestAllocations", "TestAllocations [2]", "TestAllocations [3]"} {
				got := suite.TestCases[i]
				if got.Name != prefix+wantName || got.Classname != "crypto/rand" || got.Time != float64(i+1) {
					t.Errorf("test case %d: got name=%q classname=%q time=%v", i, got.Name, got.Classname, got.Time)
				}
				if i == 2 {
					if got.Failure != nil || got.Skipped != nil {
						t.Errorf("passing run has failure=%+v skipped=%+v", got.Failure, got.Skipped)
					}
					continue
				}
				gotResult, otherResult, wantOutput := got.Failure, got.Skipped, "first run"
				if i == 1 {
					gotResult, otherResult, wantOutput = got.Skipped, got.Failure, "second run"
				}
				if gotResult == nil || gotResult.Content != wantOutput || otherResult != nil {
					t.Errorf("test case %d: got result=%+v other=%+v, want output %q", i, gotResult, otherResult, wantOutput)
				}
			}
		})
	}
}

func TestConverter(t *testing.T) {
	dir := filepath.Join("testdata", "inputs", "good")
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		fileName := file.Name()
		fileNameNoExt := fileName[:len(fileName)-len(filepath.Ext(fileName))]
		t.Run(fileNameNoExt, func(t *testing.T) {
			in := filepath.Join(dir, fileName)
			tmpOut := filepath.Join(t.TempDir(), "output.xml")
			if err := ConvertFile(tmpOut, in); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(tmpOut)
			if err != nil {
				t.Fatal(err)
			}
			goldentest.Check(t, fileNameNoExt+".xml", string(data))
		})
	}
}

func TestConverterIncludePackage(t *testing.T) {
	in := filepath.Join("testdata", "inputs", "good", "pass.jsonl")
	tmpOut := filepath.Join(t.TempDir(), "output.xml")
	if err := ConvertFileWithOptions(tmpOut, in, &Options{
		IncludePackageInTestName: true,
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(tmpOut)
	if err != nil {
		t.Fatal(err)
	}
	goldentest.Check(t, "pass.xml", string(data))
}

func TestConverterJobAttempt(t *testing.T) {
	in := filepath.Join("testdata", "inputs", "good", "pass.jsonl")

	tests := []struct {
		name          string
		opts          Options
		wantSuiteName string
	}{
		{"with_job_attempt", Options{JobAttempt: "3"}, "cmd/go [attempt 3]"},
		{"no_job_attempt", Options{}, "cmd/go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpOut := filepath.Join(t.TempDir(), "output.xml")
			if err := ConvertFileWithOptions(tmpOut, in, &tt.opts); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(tmpOut)
			if err != nil {
				t.Fatal(err)
			}
			// The suite name appears in the XML as: <testsuite name="...">
			want := `name="` + tt.wantSuiteName + `"`
			if !strings.Contains(string(data), want) {
				t.Errorf("expected suite name %q in output, got:\n%s", tt.wantSuiteName, data)
			}
		})
	}
}

func TestConverterErrors(t *testing.T) {
	dir := filepath.Join("testdata", "inputs", "bad")
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		fileName := file.Name()
		fileNameNoExt := fileName[:len(fileName)-len(filepath.Ext(fileName))]
		t.Run(fileNameNoExt, func(t *testing.T) {
			in, err := os.Open(filepath.Join(dir, fileName))
			if err != nil {
				t.Fatal(err)
			}
			err = Convert(io.Discard, in)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
