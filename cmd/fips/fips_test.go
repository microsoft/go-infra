package main_test

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestFips(t *testing.T) {
	cmd := exec.Command("go", "run", ".", "./testdata")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("fips output:\n%s", output)
		t.Fatal(err)
	}
	got := strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n")
	got = got[:len(got)-1] // remove last empty line jump
	want := []string{
		"fips: {fips/testdata F1 true [A]}",
		"fips: {fips/testdata F2 true [A B C]}",
		"fips: {fips/testdata F3 true []}",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("\nwant: %v\ngot:  %v", want, got)
	}
}
