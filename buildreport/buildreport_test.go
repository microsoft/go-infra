package buildreport

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/microsoft/go-infra/goldentest"
)

func Test_parseReportComment(t *testing.T) {
	type args struct {
		body string
	}
	tests := []struct {
		name string
		args args
		want commentBody
	}{
		{
			"no-section",
			args{"Comment body!"},
			commentBody{"Comment body!", "", nil, "", false},
		},
		{
			"no-data",
			args{"Before" + beginDataSectionMarker + "" + endDataSectionMarker + "After"},
			commentBody{"Before", "After", nil, "", false},
		},
		{
			"data",
			args{"Before" + beginDataSectionMarker + beginDataMarker + "[]" + endDataMarker + endDataSectionMarker + "After"},
			commentBody{"Before", "After", make([]State, 0), "", false},
		},
		{
			"null",
			args{"Before" + beginDataSectionMarker + beginDataMarker + "null" + endDataMarker + endDataSectionMarker + "After"},
			commentBody{"Before", "After", nil, "", false},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseReportComment(tt.args.body); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseReportComment() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_commentBody_body(t *testing.T) {
	exampleTime, err := time.Parse(time.RFC3339, "2012-03-28T01:02:03Z")
	if err != nil {
		t.Fatal(err)
	}

	newState := func(version, id, pipeline string, status string) State {
		return State{
			Version:    version,
			ID:         id,
			Name:       pipeline,
			URL:        "https://example.org/",
			Status:     status,
			StartTime:  exampleTime,
			LastUpdate: exampleTime.Add(time.Minute * 5),
		}
	}

	tests := []struct {
		name    string
		reports []State
	}{
		{
			"realistic",
			[]State{
				newState("1.18.2-1", "1234", "microsoft-go-infra-release-build", SymbolSucceeded),
				newState("1.18.2-1", "1238", "microsoft-go-infra-release-build", SymbolInProgress),
				newState("1.18.2-1", "1500", "microsoft-go-infra-release-go-images", SymbolInProgress),
				newState("1.19.1-1", "1900", "microsoft-go-infra-release-build", SymbolNotStarted),
				newState("1.18.2-1-fips", "1239", "microsoft-go-infra-release-build", SymbolFailed),
				newState("1.18.2-1", "1233", "microsoft-go-infra-release-build", SymbolFailed),
				newState("1.18.2-1", "1300", "microsoft-go-infra-release-build", SymbolNotStarted),
				newState("1.18.2-1", "12345", "microsoft-go", SymbolFailed),
			},
		},
		{"none", nil},
		{
			"no-version",
			[]State{newState("", "1234", "microsoft-go-infra-start", SymbolInProgress)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cb := commentBody{
				before:  "Text before the report.",
				after:   "Text after the report.",
				reports: tt.reports,
			}
			cb.wikiURL = "https://example.org/link-to-wiki-data"
			cb.key = true
			got, err := cb.body()
			if err != nil {
				t.Errorf("(r *reportComment) body() error = %v", err)
				return
			}
			goldentest.Check(t, filepath.Join("testdata", "report", "body."+tt.name+".golden.md"), got)
		})
	}
}

func Test_commentBody_body_UpdateExisting(t *testing.T) {
	exampleTime, err := time.Parse(time.RFC3339, "2012-03-28T01:02:03Z")
	if err != nil {
		t.Fatal(err)
	}

	cb := commentBody{
		reports: []State{
			{
				Version: "1.2.3",
				Name:    "microsoft-go",
				ID:      "1234",
				Status:  SymbolInProgress,
				// This test makes sure StartTime isn't updated, but Status and LastUpdate are.
				StartTime:  exampleTime,
				LastUpdate: exampleTime,
			},
		},
	}
	cb.update(State{
		ID:         "1234",
		Status:     SymbolSucceeded,
		LastUpdate: exampleTime.Add(time.Minute * 15),
	})
	got, err := cb.body()
	if err != nil {
		t.Errorf("(r *reportComment) body() error = %v", err)
		return
	}
	goldentest.Check(t, filepath.Join("testdata", "report", "update-existing.golden.md"), got)
}

func Test_State_notificationPreamble(t *testing.T) {
	tests := []struct {
		name         string
		pipelineName string
		version      string
		status       string
	}{
		{"not-started", releaseBuildPipelineName, "1.21.1-1", SymbolNotStarted},
		{"go-new-branch", releaseBuildPipelineName, "1.21.0-1", SymbolSucceeded},
		{"go-servicing", releaseBuildPipelineName, "1.21.3-1", SymbolSucceeded},
		{"images", releaseImagesPipelineName, "1.21.1-1", SymbolSucceeded},
		{"failed", releaseBuildPipelineName, "1.21.1-1", SymbolFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &State{
				Name:    tt.pipelineName,
				Version: tt.version,
				Status:  tt.status,
			}
			goldentest.Check(t, filepath.Join("testdata", "report", "notify."+tt.name+".golden.md"), s.notificationPreamble())
		})
	}
}
