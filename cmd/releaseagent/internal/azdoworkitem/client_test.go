// Tests for the Azure DevOps work item client.
package azdoworkitem

import (
	"context"
	"encoding/json"
	"errors"
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
	gets   func(context.Context, workitemtracking.GetWorkItemsArgs) (*[]workitemtracking.WorkItem, error)
	query  func(context.Context, workitemtracking.QueryByWiqlArgs) (*workitemtracking.WorkItemQueryResult, error)
	update func(context.Context, workitemtracking.UpdateWorkItemArgs) (*workitemtracking.WorkItem, error)
}

func (f *fakeClient) CreateWorkItem(ctx context.Context, args workitemtracking.CreateWorkItemArgs) (*workitemtracking.WorkItem, error) {
	return f.create(ctx, args)
}

func (f *fakeClient) GetWorkItem(ctx context.Context, args workitemtracking.GetWorkItemArgs) (*workitemtracking.WorkItem, error) {
	return f.get(ctx, args)
}

func (f *fakeClient) GetWorkItems(ctx context.Context, args workitemtracking.GetWorkItemsArgs) (*[]workitemtracking.WorkItem, error) {
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
		if args.Project == nil || *args.Project != "project" || args.Type == nil || *args.Type != "Issue" || args.Document == nil {
			t.Fatalf("create args = %#v", args)
		}
		assertPatch(t, *args.Document, 0, webapi.OperationValues.Add, "/fields/System.Title", "Go images test release")
		assertPatch(t, *args.Document, 1, webapi.OperationValues.Add, "/fields/System.AreaPath", AreaPath)
		assertPatch(t, *args.Document, 2, webapi.OperationValues.Add, "/fields/System.Tags", SelectorTag)
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
		assertPatch(t, *args.Document, 2, webapi.OperationValues.Add, "/fields/System.Tags", SelectorTag+"; "+TestTag)
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
	snapshot := testSnapshot(StatusRunning)
	sdk := &fakeClient{update: func(_ context.Context, args workitemtracking.UpdateWorkItemArgs) (*workitemtracking.WorkItem, error) {
		if args.Id == nil || *args.Id != 42 || args.Document == nil || len(*args.Document) != 3 {
			t.Fatalf("update args = %#v", args)
		}
		assertPatch(t, *args.Document, 0, webapi.OperationValues.Test, "/rev", 7)
		assertPatch(t, *args.Document, 1, webapi.OperationValues.Add, "/fields/System.State", "Active")
		return sdkWorkItem(t, 42, 8, snapshot), nil
	}}
	client := newTestClient(t, sdk, "test-token")
	item, err := client.Update(context.Background(), &WorkItem{ID: 42, Revision: 7}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if item.Revision != 8 || item.Snapshot.Status != StatusRunning {
		t.Fatalf("updated work item = %#v", item)
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
			for _, clause := range []string{"[System.WorkItemType] = 'Issue'", "[System.AreaPath] = 'DevDiv\\GoLang'", "[System.Tags] CONTAINS 'releaseagent'"} {
				if !strings.Contains(*args.Wiql.Query, clause) {
					t.Fatalf("WIQL %q does not contain %q", *args.Wiql.Query, clause)
				}
			}
			return &workitemtracking.WorkItemQueryResult{WorkItems: &references}, nil
		},
		gets: func(_ context.Context, args workitemtracking.GetWorkItemsArgs) (*[]workitemtracking.WorkItem, error) {
			if args.Ids == nil || !slices.Equal(*args.Ids, []int{2, 1}) {
				t.Fatalf("get work items args = %#v", args)
			}
			return &responses, nil
		},
	}
	client := newTestClient(t, sdk, "test-token")
	items, err := client.Query(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := []int{items[0].ID, items[1].ID}; !slices.Equal(got, []int{2, 1}) {
		t.Fatalf("work item order = %v, want [2 1]", got)
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
		"System.Description":  mustRenderDescription(t, snapshot),
	}
	return &workitemtracking.WorkItem{
		Id: &id, Rev: &revision, Fields: &fields,
		Links: map[string]any{"html": map[string]any{"href": "https://example.invalid/workitems/42"}},
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
