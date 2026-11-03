// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	azdopipeline "github.com/microsoft/go-infra/cmd/releaseui/internal/azdo/pipeline"
	azdorepo "github.com/microsoft/go-infra/cmd/releaseui/internal/azdo/repo"
)

type fakeSourceRepository struct {
	commit string
	err    error
	reads  int
}

func (r *fakeSourceRepository) GetBranchTip(context.Context, string) (azdorepo.BranchTip, error) {
	r.reads++
	return azdorepo.BranchTip{Name: SourceBranch, ObjectID: r.commit}, r.err
}

func (r *fakeSourceRepository) GetFileAtCommit(context.Context, string, string) ([]byte, error) {
	r.reads++
	return []byte(testPipelineContract), nil
}

func (r *fakeSourceRepository) GetJSONFileAtCommit(_ context.Context, _, _ string, target any) error {
	r.reads++
	return json.Unmarshal([]byte(`{"1.26":{"version":"1.26.5","revision":"2"}}`), target)
}

func (r *fakeSourceRepository) VerifyCommit(context.Context, string) error {
	r.reads++
	return errors.New("unexpected mirror verification")
}

func TestRunServiceRevalidatesSourceAndRollbackAtTheQueueBoundary(t *testing.T) {
	for _, test := range []struct {
		name      string
		mode      Mode
		commit    string
		sourceErr error
		wantErr   string
	}{
		{name: "unchanged source", mode: ModeNormal, commit: queueTestCommit},
		{name: "main advanced", mode: ModeNormal, commit: strings.Repeat("a", 40), wantErr: "microsoft/main changed"},
		{name: "source unavailable", mode: ModeNormal, sourceErr: errors.New("source unavailable"), wantErr: "re-resolve current"},
		{name: "rollback source failed", mode: ModeRollback, commit: queueTestCommit, wantErr: "revalidate rollback source"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodGet {
					t.Errorf("unexpected mutation: %s %s", request.Method, request.URL)
					http.Error(response, "mutations are forbidden in this fixture", http.StatusMethodNotAllowed)
					return
				}
				response.Header().Set("Content-Type", "application/json")
				switch request.URL.Path {
				case "/internal/_apis/build/builds":
					_, _ = response.Write([]byte(`{"count":0,"value":[]}`))
				case "/internal/_apis/build/builds/777":
					_, _ = response.Write([]byte(`{"id":777,"definition":{"id":1023},"status":"completed","result":"failed"}`))
				default:
					t.Errorf("unexpected read: %s", request.URL)
					http.NotFound(response, request)
				}
			}))
			defer server.Close()
			pipelines, err := azdopipeline.NewClient(server.URL, "internal", server.Client(), staticToken("test-token"))
			if err != nil {
				t.Fatal(err)
			}
			repository := &fakeSourceRepository{commit: test.commit, err: test.sourceErr}
			queue := &fakeQueueClient{}
			service := &AzureService{pipelines: pipelines, repository: repository, queue: queue}
			request := RunRequest{
				Mode: test.mode, SessionID: "session", ExecutionDigest: testDigest,
				Versions: []string{"1.26.5-2"}, SourceVersion: queueTestCommit,
			}
			if test.mode == ModeRollback {
				request.SourceBuildID = "777"
			}
			runService, err := service.NewRunService(request)
			if err != nil {
				t.Fatal(err)
			}
			if repository.reads != 0 || queue.calls != 0 {
				t.Fatal("constructing the graph service performed source reads or queued a build")
			}
			parameters, err := PipelineParameters(request.Mode, request.SourceBuildID)
			if err != nil {
				t.Fatal(err)
			}
			buildID, err := runService.QueuePipeline(context.Background(), parameters)
			if test.wantErr == "" {
				if err != nil || buildID != "888" || queue.calls != 1 {
					t.Fatalf("queue result=%q, err=%v, calls=%d", buildID, err, queue.calls)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.wantErr) || queue.calls != 0 {
				t.Fatalf("unsafe source reached the queue: result=%q, err=%v, calls=%d", buildID, err, queue.calls)
			}
		})
	}
}
