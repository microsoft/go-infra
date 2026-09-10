// Tests for the Azure DevOps work item client.
package azdoworkitem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/microsoft/azure-devops-go-api/azuredevops"
	"github.com/microsoft/azure-devops-go-api/azuredevops/identity"
	"github.com/microsoft/azure-devops-go-api/azuredevops/location"
	"github.com/microsoft/azure-devops-go-api/azuredevops/webapi"
	"github.com/microsoft/azure-devops-go-api/azuredevops/workitemtracking"
)

type staticToken string

func (t staticToken) Token(context.Context) (string, error) { return string(t), nil }

type fakeLocationClient struct {
	get func(context.Context, location.GetConnectionDataArgs) (*location.ConnectionData, error)
}

func (f fakeLocationClient) GetConnectionData(ctx context.Context, args location.GetConnectionDataArgs) (*location.ConnectionData, error) {
	return f.get(ctx, args)
}

type fakeClient struct {
	create func(context.Context, workitemtracking.CreateWorkItemArgs) (*workitemtracking.WorkItem, error)
	get    func(context.Context, workitemtracking.GetWorkItemArgs) (*workitemtracking.WorkItem, error)
	gets   func(context.Context, workitemtracking.GetWorkItemsBatchArgs) (*[]workitemtracking.WorkItem, error)
	query  func(context.Context, workitemtracking.QueryByWiqlArgs) (*workitemtracking.WorkItemQueryResult, error)
	update func(context.Context, workitemtracking.UpdateWorkItemArgs) (*workitemtracking.WorkItem, error)
}

func (f *fakeClient) CreateWorkItem(ctx context.Context, args workitemtracking.CreateWorkItemArgs) (*workitemtracking.WorkItem, error) {
	return f.create(ctx, args)
}

func (f *fakeClient) GetWorkItem(ctx context.Context, args workitemtracking.GetWorkItemArgs) (*workitemtracking.WorkItem, error) {
	return f.get(ctx, args)
}

func (f *fakeClient) GetWorkItemsBatch(ctx context.Context, args workitemtracking.GetWorkItemsBatchArgs) (*[]workitemtracking.WorkItem, error) {
	return f.gets(ctx, args)
}

func (f *fakeClient) QueryByWiql(ctx context.Context, args workitemtracking.QueryByWiqlArgs) (*workitemtracking.WorkItemQueryResult, error) {
	return f.query(ctx, args)
}

func (f *fakeClient) UpdateWorkItem(ctx context.Context, args workitemtracking.UpdateWorkItemArgs) (*workitemtracking.WorkItem, error) {
	return f.update(ctx, args)
}

func TestCreateUsesFixedMetadata(t *testing.T) {
	snapshot := testSnapshot(StatusStarting)
	sdk := &fakeClient{create: func(_ context.Context, args workitemtracking.CreateWorkItemArgs) (*workitemtracking.WorkItem, error) {
		if args.Project == nil || *args.Project != "project" || args.Type == nil || *args.Type != "Issue" || args.Document == nil ||
			args.Expand == nil || *args.Expand != workitemtracking.WorkItemExpandValues.Links {

			t.Fatalf("create args = %#v", args)
		}
		assertPatch(t, *args.Document, 0, webapi.OperationValues.Add, "/fields/System.Title", "Go images test release")
		assertPatch(t, *args.Document, 1, webapi.OperationValues.Add, "/fields/System.AreaPath", AreaPath)
		assertPatch(t, *args.Document, 2, webapi.OperationValues.Add, "/fields/System.Tags", SelectorTag+"; go-images")
		assertPatch(t, *args.Document, 3, webapi.OperationValues.Add, "/fields/System.AssignedTo", "Release Operator")
		assertPatch(t, *args.Document, 4, webapi.OperationValues.Add, "/fields/System.State", "Active")
		assertPatch(t, *args.Document, 5, webapi.OperationValues.Add, "/fields/System.Description", mustRenderDescription(t, snapshot))
		return sdkWorkItem(t, 42, 1, snapshot), nil
	}}
	client := newTestClient(t, sdk, "test-token")
	item, err := client.Create(context.Background(), "Go images test release", "Release Operator", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if item.ID != 42 || item.Revision != 1 || item.Snapshot.Status != StatusStarting {
		t.Fatalf("work item = %#v", item)
	}
}

func TestCreateAddsTestTag(t *testing.T) {
	snapshot := testSnapshot(StatusStarting)
	snapshot.Test = true
	sdk := &fakeClient{create: func(_ context.Context, args workitemtracking.CreateWorkItemArgs) (*workitemtracking.WorkItem, error) {
		if args.Document == nil {
			t.Fatal("create document is nil")
		}
		assertPatch(t, *args.Document, 2, webapi.OperationValues.Add, "/fields/System.Tags", SelectorTag+"; "+TestTag+"; go-images")
		return sdkWorkItem(t, 42, 1, snapshot), nil
	}}
	client := newTestClient(t, sdk, "test-token")
	item, err := client.Create(context.Background(), "Go images test release", "Release Operator", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !item.Snapshot.Test {
		t.Fatal("created work item lost test classification")
	}
}

func TestGetRejectsMismatchedTestTag(t *testing.T) {
	snapshot := testSnapshot(StatusStarting)
	snapshot.Test = true
	item := sdkWorkItem(t, 42, 1, snapshot)
	(*item.Fields)["System.Tags"] = SelectorTag
	sdk := &fakeClient{get: func(context.Context, workitemtracking.GetWorkItemArgs) (*workitemtracking.WorkItem, error) {
		return item, nil
	}}
	client := newTestClient(t, sdk, "test-token")
	if _, err := client.Get(context.Background(), 42); err == nil || !strings.Contains(err.Error(), "test tag") {
		t.Fatalf("error = %v, want mismatched test tag error", err)
	}
}

func TestGetAcceptsLegacyTestTag(t *testing.T) {
	snapshot := testSnapshot(StatusStarting)
	snapshot.Test = true
	item := sdkWorkItem(t, 42, 1, snapshot)
	(*item.Fields)["System.Tags"] = SelectorTag + "; " + legacyTestTag
	sdk := &fakeClient{get: func(context.Context, workitemtracking.GetWorkItemArgs) (*workitemtracking.WorkItem, error) {
		return item, nil
	}}
	client := newTestClient(t, sdk, "test-token")
	if _, err := client.Get(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
}

func TestGetAcceptsAzureTagCasing(t *testing.T) {
	snapshot := testSnapshot(StatusStarting)
	snapshot.Test = true
	item := sdkWorkItem(t, 42, 1, snapshot)
	(*item.Fields)["System.Tags"] = "ReleaseAgent; Test; go-images"
	sdk := &fakeClient{get: func(context.Context, workitemtracking.GetWorkItemArgs) (*workitemtracking.WorkItem, error) {
		return item, nil
	}}
	client := newTestClient(t, sdk, "test-token")
	if _, err := client.Get(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
}

func TestGetAcceptsClosedAttentionStatus(t *testing.T) {
	for _, status := range []Status{StatusFailed, StatusCanceled, StatusUncertain} {
		t.Run(string(status), func(t *testing.T) {
			snapshot := testSnapshot(status)
			item := sdkWorkItem(t, 42, 1, snapshot)
			(*item.Fields)["System.State"] = "Closed"
			sdk := &fakeClient{get: func(context.Context, workitemtracking.GetWorkItemArgs) (*workitemtracking.WorkItem, error) {
				return item, nil
			}}
			client := newTestClient(t, sdk, "test-token")
			got, err := client.Get(context.Background(), 42)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != "Closed" || got.Snapshot.Status != status {
				t.Fatalf("work item = %#v", got)
			}
		})
	}
}

func TestGetRejectsIncompatibleWorkflowState(t *testing.T) {
	for _, test := range []struct {
		name   string
		status Status
		state  string
	}{
		{name: "closed running", status: StatusRunning, state: "Closed"},
		{name: "active succeeded", status: StatusSucceeded, state: "Active"},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := sdkWorkItem(t, 42, 1, testSnapshot(test.status))
			(*item.Fields)["System.State"] = test.state
			sdk := &fakeClient{get: func(context.Context, workitemtracking.GetWorkItemArgs) (*workitemtracking.WorkItem, error) {
				return item, nil
			}}
			client := newTestClient(t, sdk, "test-token")
			if _, err := client.Get(context.Background(), 42); err == nil || !strings.Contains(err.Error(), "incompatible") {
				t.Fatalf("error = %v, want incompatible state error", err)
			}
		})
	}
}

func TestWorkItemURLUsesSDKBrowserLink(t *testing.T) {
	item := sdkWorkItem(t, 3062459, 1, testSnapshot(StatusRunning))
	apiURL := "https://devdiv.visualstudio.com/_apis/wit/workItems/3062459"
	item.Url = &apiURL
	const want = "https://dev.azure.com/devdiv/project/_workitems/edit/3062459"
	item.Links = map[string]any{"html": map[string]any{"href": want}}
	sdk := &fakeClient{get: func(_ context.Context, args workitemtracking.GetWorkItemArgs) (*workitemtracking.WorkItem, error) {
		if args.Expand == nil || *args.Expand != workitemtracking.WorkItemExpandValues.Links {
			t.Fatalf("expand = %v, want Links", args.Expand)
		}
		return item, nil
	}}
	client, err := NewClient(Config{
		BaseURL: "https://dev.azure.com/devdiv", Project: "DEVDIV", WorkItemType: "Issue",
	}, staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	client.newClient = func(context.Context) (workItemClient, string, error) {
		return sdk, "test-token", nil
	}
	got, err := client.Get(context.Background(), 3062459)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != want {
		t.Fatalf("work item URL = %q, want %q", got.URL, want)
	}
}

func TestGetRejectsMissingBrowserLink(t *testing.T) {
	item := sdkWorkItem(t, 42, 1, testSnapshot(StatusRunning))
	item.Links = nil
	apiURL := "https://devdiv.visualstudio.com/_apis/wit/workItems/42"
	item.Url = &apiURL
	sdk := &fakeClient{get: func(context.Context, workitemtracking.GetWorkItemArgs) (*workitemtracking.WorkItem, error) {
		return item, nil
	}}
	client := newTestClient(t, sdk, "test-token")
	if _, err := client.Get(context.Background(), 42); err == nil || !strings.Contains(err.Error(), "browser URL") {
		t.Fatalf("error = %v, want missing browser URL error", err)
	}
}

func TestCurrentUser(t *testing.T) {
	name := "Release Operator"
	client := newTestClient(t, &fakeClient{}, "test-token")
	client.newLocation = func(context.Context) (locationClient, string, error) {
		return fakeLocationClient{get: func(context.Context, location.GetConnectionDataArgs) (*location.ConnectionData, error) {
			return &location.ConnectionData{AuthenticatedUser: &identity.Identity{ProviderDisplayName: &name}}, nil
		}}, "test-token", nil
	}
	got, err := client.CurrentUser(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != name {
		t.Fatalf("CurrentUser = %q, want %q", got, name)
	}
}

func TestUpdateTestsRevisionFirst(t *testing.T) {
	snapshot := testSnapshot(StatusSucceeded)
	snapshot.Test = true
	sdk := &fakeClient{update: func(_ context.Context, args workitemtracking.UpdateWorkItemArgs) (*workitemtracking.WorkItem, error) {
		if args.Id == nil || *args.Id != 42 || args.Document == nil || len(*args.Document) != 4 ||
			args.Expand == nil || *args.Expand != workitemtracking.WorkItemExpandValues.Links {

			t.Fatalf("update args = %#v", args)
		}
		assertPatch(t, *args.Document, 0, webapi.OperationValues.Test, "/rev", 7)
		assertPatch(t, *args.Document, 1, webapi.OperationValues.Add, "/fields/System.State", "Closed")
		assertPatch(t, *args.Document, 2, webapi.OperationValues.Replace, "/fields/System.Tags", SelectorTag+"; "+TestTag+"; go-images")
		return sdkWorkItem(t, 42, 8, snapshot), nil
	}}
	client := newTestClient(t, sdk, "test-token")
	item, err := client.Update(context.Background(), &WorkItem{ID: 42, Revision: 7}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if item.Revision != 8 || item.Snapshot.Status != StatusSucceeded {
		t.Fatalf("updated work item = %#v", item)
	}
}

func TestUpdatePreservesClosedAttentionStatus(t *testing.T) {
	snapshot := testSnapshot(StatusCanceled)
	sdk := &fakeClient{update: func(_ context.Context, args workitemtracking.UpdateWorkItemArgs) (*workitemtracking.WorkItem, error) {
		if args.Document == nil {
			t.Fatal("update document is nil")
		}
		assertPatch(t, *args.Document, 0, webapi.OperationValues.Test, "/rev", 7)
		assertPatch(t, *args.Document, 1, webapi.OperationValues.Add, "/fields/System.State", "Closed")
		item := sdkWorkItem(t, 42, 8, snapshot)
		(*item.Fields)["System.State"] = "Closed"
		return item, nil
	}}
	client := newTestClient(t, sdk, "test-token")
	item, err := client.Update(context.Background(), &WorkItem{ID: 42, Revision: 7, State: "Closed"}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if item.State != "Closed" || item.Snapshot.Status != StatusCanceled {
		t.Fatalf("updated work item = %#v", item)
	}
}

func TestWorkItemTagsIncludeProcess(t *testing.T) {
	for _, test := range []struct {
		name      string
		processID string
		test      bool
		want      string
	}{
		{name: "release", processID: "go-images", want: "releaseagent; go-images"},
		{name: "test", processID: "go-images", test: true, want: "releaseagent; test; go-images"},
		{name: "dry run", processID: "go-infra", test: true, want: "releaseagent; test; go-infra"},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := testSnapshot(StatusStarting)
			snapshot.ProcessID = test.processID
			snapshot.Test = test.test
			if got := workItemTags(snapshot); got != test.want {
				t.Fatalf("work item tags = %q, want %q", got, test.want)
			}
		})
	}
}

func TestUpdateDoesNotRetryRevisionConflict(t *testing.T) {
	var calls atomic.Int32
	typeName := "WorkItemRevisionMismatchException"
	message := "stale secret-token"
	sdk := &fakeClient{update: func(context.Context, workitemtracking.UpdateWorkItemArgs) (*workitemtracking.WorkItem, error) {
		calls.Add(1)
		return nil, azuredevops.WrappedError{TypeName: &typeName, Message: &message}
	}}
	client := newTestClient(t, sdk, "secret-token")
	_, err := client.Update(context.Background(), &WorkItem{ID: 42, Revision: 7}, testSnapshot(StatusRunning))
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("error = %v, want ErrRevisionConflict", err)
	}
	if calls.Load() != 1 || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("calls = %d, error = %v", calls.Load(), err)
	}
}

func TestCreateRejectsChangedSnapshot(t *testing.T) {
	sdk := &fakeClient{create: func(context.Context, workitemtracking.CreateWorkItemArgs) (*workitemtracking.WorkItem, error) {
		return sdkWorkItem(t, 42, 1, testSnapshot(StatusRunning)), nil
	}}
	client := newTestClient(t, sdk, "test-token")
	if _, err := client.Create(context.Background(), "Go images test release", "Release Operator", testSnapshot(StatusStarting)); err == nil || !strings.Contains(err.Error(), "different created release snapshot") {
		t.Fatalf("error = %v, want changed snapshot error", err)
	}
}

func TestGetRejectsWrongWorkItem(t *testing.T) {
	sdk := &fakeClient{get: func(context.Context, workitemtracking.GetWorkItemArgs) (*workitemtracking.WorkItem, error) {
		return sdkWorkItem(t, 43, 1, testSnapshot(StatusRunning)), nil
	}}
	client := newTestClient(t, sdk, "test-token")
	if _, err := client.Get(context.Background(), 42); err == nil || !strings.Contains(err.Error(), "expected 42") {
		t.Fatalf("error = %v, want mismatched ID error", err)
	}
}

func TestQueryReturnsWIQLOrder(t *testing.T) {
	references := []workitemtracking.WorkItemReference{{Id: pointer(2)}, {Id: pointer(1)}}
	responses := []workitemtracking.WorkItem{
		*sdkWorkItem(t, 1, 3, testSnapshot(StatusRunning)),
		*sdkWorkItem(t, 2, 4, testSnapshot(StatusFailed)),
	}
	sdk := &fakeClient{
		query: func(_ context.Context, args workitemtracking.QueryByWiqlArgs) (*workitemtracking.WorkItemQueryResult, error) {
			if args.Top == nil || *args.Top != 20 || args.Wiql == nil || args.Wiql.Query == nil {
				t.Fatalf("query args = %#v", args)
			}
			for _, clause := range []string{"[System.WorkItemType] = 'Issue'", "[System.AreaPath] = 'DevDiv\\GoLang'", "[System.Tags] CONTAINS 'releaseagent'", "[System.State] <> 'Closed'"} {
				if !strings.Contains(*args.Wiql.Query, clause) {
					t.Fatalf("WIQL %q does not contain %q", *args.Wiql.Query, clause)
				}
			}
			return &workitemtracking.WorkItemQueryResult{WorkItems: &references}, nil
		},
		gets: func(_ context.Context, args workitemtracking.GetWorkItemsBatchArgs) (*[]workitemtracking.WorkItem, error) {
			request := args.WorkItemGetRequest
			if request == nil || request.Ids == nil || !slices.Equal(*request.Ids, []int{2, 1}) ||
				request.Expand == nil || *request.Expand != workitemtracking.WorkItemExpandValues.Links {

				t.Fatalf("get work items args = %#v", args)
			}
			return &responses, nil
		},
	}
	client := newTestClient(t, sdk, "test-token")
	items, err := client.Query(context.Background(), false, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := []int{items[0].ID, items[1].ID}; !slices.Equal(got, []int{2, 1}) {
		t.Fatalf("work item order = %v, want [2 1]", got)
	}
}

func TestQueryClosedWorkItems(t *testing.T) {
	references := []workitemtracking.WorkItemReference{}
	sdk := &fakeClient{query: func(_ context.Context, args workitemtracking.QueryByWiqlArgs) (*workitemtracking.WorkItemQueryResult, error) {
		if args.Wiql == nil || args.Wiql.Query == nil || !strings.Contains(*args.Wiql.Query, "[System.State] = 'Closed'") {
			t.Fatalf("query args = %#v", args)
		}
		return &workitemtracking.WorkItemQueryResult{WorkItems: &references}, nil
	}}
	client := newTestClient(t, sdk, "test-token")
	items, err := client.Query(context.Background(), true, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("items = %#v, want none", items)
	}
}

func TestSDKErrorRedactsToken(t *testing.T) {
	sdk := &fakeClient{get: func(context.Context, workitemtracking.GetWorkItemArgs) (*workitemtracking.WorkItem, error) {
		return nil, errors.New("secret-token denied")
	}}
	client := newTestClient(t, sdk, "secret-token")
	_, err := client.Get(context.Background(), 42)
	if err == nil || strings.Contains(err.Error(), "secret-token") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("error = %v, want redacted error", err)
	}
}

func newTestClient(t *testing.T, sdk workItemClient, token string) *Client {
	t.Helper()
	client, err := NewClient(Config{
		BaseURL: "https://example.invalid", Project: "project", WorkItemType: "Issue",
	}, staticToken(token))
	if err != nil {
		t.Fatal(err)
	}
	client.newClient = func(context.Context) (workItemClient, string, error) { return sdk, token, nil }
	return client
}

func testSnapshot(status Status) *Snapshot {
	return &Snapshot{
		SchemaVersion: CurrentSchemaVersion,
		ProcessID:     "go-images",
		Status:        status,
		IntentDigest:  testDigest,
		Payload:       json.RawMessage(`{"buildId":"42"}`),
	}
}

func sdkWorkItem(t *testing.T, id, revision int, snapshot *Snapshot) *workitemtracking.WorkItem {
	t.Helper()
	fields := map[string]any{
		"System.WorkItemType": "Issue",
		"System.Title":        "Go images release",
		"System.State":        workItemState(snapshot.Status),
		"System.AreaPath":     AreaPath,
		"System.Tags":         "other; " + workItemTags(snapshot),
		"System.ChangedDate":  "2026-09-09T12:00:00Z",
		"System.Description":  mustRenderDescription(t, snapshot),
	}
	return &workitemtracking.WorkItem{
		Id: &id, Rev: &revision, Fields: &fields,
		Links: map[string]any{"html": map[string]any{"href": fmt.Sprintf("https://example.invalid/workitems/%d", id)}},
	}
}

func mustRenderDescription(t *testing.T, snapshot *Snapshot) string {
	t.Helper()
	description, err := RenderDescription(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return description
}

func assertPatch(
	t *testing.T,
	document []webapi.JsonPatchOperation,
	index int,
	operation webapi.Operation,
	path string,
	value any,
) {
	t.Helper()
	if len(document) <= index || document[index].Op == nil || *document[index].Op != operation ||
		document[index].Path == nil || *document[index].Path != path || document[index].Value != value {

		t.Fatalf("patch %d = %#v, want %s %s %#v", index, document[index], operation, path, value)
	}
}

func pointer[T any](value T) *T { return &value }
