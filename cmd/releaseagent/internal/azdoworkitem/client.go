// Client operations for release work items.
package azdoworkitem

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/microsoft/azure-devops-go-api/azuredevops"
	"github.com/microsoft/azure-devops-go-api/azuredevops/location"
	"github.com/microsoft/azure-devops-go-api/azuredevops/webapi"
	"github.com/microsoft/azure-devops-go-api/azuredevops/workitemtracking"
)

const (
	AreaPath         = `DevDiv\GoLang`
	SelectorTag      = "releaseagent"
	TestTag          = "test"
	legacyTestTag    = "releaseagent-test"
	browserProject   = "DevDiv"
	descriptionField = "System.Description"
	maxQueryResults  = 200
	sdkTimeout       = 3 * time.Minute
)

var ErrRevisionConflict = errors.New("release work item revision changed")

type TokenProvider interface {
	Token(context.Context) (string, error)
}

type Config struct {
	BaseURL      string
	Project      string
	WorkItemType string
}

type Client struct {
	baseURL      string
	project      string
	workItemType string
	tokens       TokenProvider
	newClient    func(context.Context) (workItemClient, string, error)
	newLocation  func(context.Context) (locationClient, string, error)
}

type WorkItem struct {
	ID        int
	Revision  int
	URL       string
	Title     string
	State     string
	ChangedAt time.Time
	Snapshot  *Snapshot
}

type workItemClient interface {
	CreateWorkItem(context.Context, workitemtracking.CreateWorkItemArgs) (*workitemtracking.WorkItem, error)
	GetWorkItem(context.Context, workitemtracking.GetWorkItemArgs) (*workitemtracking.WorkItem, error)
	GetWorkItems(context.Context, workitemtracking.GetWorkItemsArgs) (*[]workitemtracking.WorkItem, error)
	QueryByWiql(context.Context, workitemtracking.QueryByWiqlArgs) (*workitemtracking.WorkItemQueryResult, error)
	UpdateWorkItem(context.Context, workitemtracking.UpdateWorkItemArgs) (*workitemtracking.WorkItem, error)
}

type locationClient interface {
	GetConnectionData(context.Context, location.GetConnectionDataArgs) (*location.ConnectionData, error)
}

func NewClient(config Config, tokens TokenProvider) (*Client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" && parsed.Scheme != "http" || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {

		return nil, fmt.Errorf("invalid Azure DevOps base URL %q", config.BaseURL)
	}
	project := strings.TrimSpace(config.Project)
	if project == "" {
		return nil, errors.New("azure DevOps project must not be empty")
	}
	workItemType := strings.TrimSpace(config.WorkItemType)
	if workItemType == "" {
		return nil, errors.New("azure DevOps work item type must not be empty")
	}
	if tokens == nil {
		return nil, errors.New("azure DevOps token provider must not be nil")
	}
	client := &Client{
		baseURL: baseURL, project: project, workItemType: workItemType,
		tokens: tokens,
	}
	client.newClient = client.createClient
	client.newLocation = client.createLocationClient
	return client, nil
}

// CurrentUser returns the identity used to assign a release work item.
func (c *Client) CurrentUser(ctx context.Context) (string, error) {
	sdk, token, err := c.newLocation(ctx)
	if err != nil {
		return "", fmt.Errorf("create Azure DevOps location client: %w", redactError(err, token))
	}
	data, err := sdk.GetConnectionData(ctx, location.GetConnectionDataArgs{})
	if err != nil {
		return "", fmt.Errorf("get authenticated Azure DevOps user: %w", redactError(err, token))
	}
	if data == nil || data.AuthenticatedUser == nil || data.AuthenticatedUser.ProviderDisplayName == nil ||
		strings.TrimSpace(*data.AuthenticatedUser.ProviderDisplayName) == "" {

		return "", errors.New("azure DevOps returned no authenticated user name")
	}
	return *data.AuthenticatedUser.ProviderDisplayName, nil
}

func (c *Client) Create(ctx context.Context, title, assignedTo string, snapshot *Snapshot) (*WorkItem, error) {
	if strings.TrimSpace(title) == "" {
		return nil, errors.New("release work item title must not be empty")
	}
	if strings.TrimSpace(assignedTo) == "" {
		return nil, errors.New("release work item assignee must not be empty")
	}
	snapshotJSON, err := MarshalSnapshot(snapshot)
	if err != nil {
		return nil, err
	}
	description, err := RenderDescription(snapshot)
	if err != nil {
		return nil, err
	}
	document := []webapi.JsonPatchOperation{
		patch(webapi.OperationValues.Add, "/fields/System.Title", title),
		patch(webapi.OperationValues.Add, "/fields/System.AreaPath", AreaPath),
		patch(webapi.OperationValues.Add, "/fields/System.Tags", workItemTags(snapshot)),
		patch(webapi.OperationValues.Add, "/fields/System.AssignedTo", assignedTo),
		patch(webapi.OperationValues.Add, "/fields/System.State", workItemState(snapshot.Status)),
		patch(webapi.OperationValues.Add, "/fields/"+descriptionField, description),
	}
	sdk, token, err := c.newClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create Azure DevOps work item client: %w", redactError(err, token))
	}
	response, err := sdk.CreateWorkItem(ctx, workitemtracking.CreateWorkItemArgs{
		Document: &document, Project: &c.project, Type: &c.workItemType,
	})
	if err != nil {
		return nil, fmt.Errorf("create release work item: %w", redactError(err, token))
	}
	return c.parseWrittenWorkItem(response, snapshotJSON, "created")
}

func (c *Client) Get(ctx context.Context, id int) (*WorkItem, error) {
	if id <= 0 {
		return nil, errors.New("release work item ID must be positive")
	}
	sdk, token, err := c.newClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create Azure DevOps work item client: %w", redactError(err, token))
	}
	fields := c.fields()
	response, err := sdk.GetWorkItem(ctx, workitemtracking.GetWorkItemArgs{
		Id: &id, Project: &c.project, Fields: &fields,
	})
	if err != nil {
		return nil, fmt.Errorf("get release work item %d: %w", id, redactError(err, token))
	}
	workItem, err := c.parseWorkItem(response)
	if err != nil {
		return nil, err
	}
	if workItem.ID != id {
		return nil, fmt.Errorf("azure DevOps returned work item %d, expected %d", workItem.ID, id)
	}
	return workItem, nil
}

func (c *Client) Update(ctx context.Context, current *WorkItem, snapshot *Snapshot) (*WorkItem, error) {
	if current == nil || current.ID <= 0 || current.Revision <= 0 {
		return nil, errors.New("current release work item is invalid")
	}
	snapshotJSON, err := MarshalSnapshot(snapshot)
	if err != nil {
		return nil, err
	}
	description, err := RenderDescription(snapshot)
	if err != nil {
		return nil, err
	}
	document := []webapi.JsonPatchOperation{
		patch(webapi.OperationValues.Test, "/rev", current.Revision),
		patch(webapi.OperationValues.Add, "/fields/System.State", workItemStateForUpdate(current.State, snapshot.Status)),
		patch(webapi.OperationValues.Replace, "/fields/System.Tags", workItemTags(snapshot)),
		patch(webapi.OperationValues.Add, "/fields/"+descriptionField, description),
	}
	sdk, token, err := c.newClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create Azure DevOps work item client: %w", redactError(err, token))
	}
	response, err := sdk.UpdateWorkItem(ctx, workitemtracking.UpdateWorkItemArgs{
		Document: &document, Id: &current.ID, Project: &c.project,
	})
	if err != nil {
		if isRevisionConflict(err) {
			return nil, fmt.Errorf("%w: work item %d revision %d", ErrRevisionConflict, current.ID, current.Revision)
		}
		return nil, fmt.Errorf("update release work item %d: %w", current.ID, redactError(err, token))
	}
	workItem, err := c.parseWrittenWorkItem(response, snapshotJSON, "updated")
	if err != nil {
		return nil, err
	}
	if workItem.ID != current.ID || workItem.Revision <= current.Revision {
		return nil, errors.New("azure DevOps returned an unexpected work item revision")
	}
	return workItem, nil
}

func (c *Client) Query(ctx context.Context, closed bool, limit int) ([]*WorkItem, error) {
	if limit <= 0 || limit > maxQueryResults {
		return nil, fmt.Errorf("release work item query limit must be between 1 and %d", maxQueryResults)
	}
	stateOperator := "<>"
	if closed {
		stateOperator = "="
	}
	wiql := fmt.Sprintf(
		"SELECT [System.Id] FROM WorkItems WHERE [System.WorkItemType] = '%s' "+
			"AND [System.AreaPath] = '%s' AND [System.Tags] CONTAINS '%s' AND [System.State] %s 'Closed' "+
			"ORDER BY [System.ChangedDate] DESC",
		escapeWIQL(c.workItemType), escapeWIQL(AreaPath), escapeWIQL(SelectorTag), stateOperator,
	)
	sdk, token, err := c.newClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create Azure DevOps work item client: %w", redactError(err, token))
	}
	result, err := sdk.QueryByWiql(ctx, workitemtracking.QueryByWiqlArgs{
		Wiql: &workitemtracking.Wiql{Query: &wiql}, Project: &c.project, Top: &limit,
	})
	if err != nil {
		return nil, fmt.Errorf("query release work items: %w", redactError(err, token))
	}
	if result == nil || result.WorkItems == nil || len(*result.WorkItems) == 0 {
		return []*WorkItem{}, nil
	}
	if len(*result.WorkItems) > limit {
		return nil, fmt.Errorf("azure DevOps query returned more than %d work items", limit)
	}

	ids := make([]int, len(*result.WorkItems))
	positions := make(map[int]int, len(ids))
	for index, reference := range *result.WorkItems {
		if reference.Id == nil || *reference.Id <= 0 {
			return nil, errors.New("azure DevOps query returned an invalid work item ID")
		}
		if _, exists := positions[*reference.Id]; exists {
			return nil, fmt.Errorf("azure DevOps query returned duplicate work item %d", *reference.Id)
		}
		ids[index] = *reference.Id
		positions[*reference.Id] = index
	}
	fields := c.fields()
	responses, err := sdk.GetWorkItems(ctx, workitemtracking.GetWorkItemsArgs{
		Ids: &ids, Project: &c.project, Fields: &fields,
	})
	if err != nil {
		return nil, fmt.Errorf("read release work item query results: %w", redactError(err, token))
	}
	if responses == nil {
		return nil, errors.New("azure DevOps returned no work item query results")
	}
	items := make([]*WorkItem, len(ids))
	for index := range *responses {
		item, err := c.parseWorkItem(&(*responses)[index])
		if err != nil {
			return nil, err
		}
		position, ok := positions[item.ID]
		if !ok {
			return nil, fmt.Errorf("azure DevOps returned unrequested work item %d", item.ID)
		}
		if items[position] != nil {
			return nil, fmt.Errorf("azure DevOps returned duplicate work item %d", item.ID)
		}
		items[position] = item
	}
	for _, item := range items {
		if item == nil {
			return nil, fmt.Errorf("azure DevOps returned %d of %d requested work items", len(*responses), len(ids))
		}
	}
	return items, nil
}

func (c *Client) createClient(ctx context.Context) (workItemClient, string, error) {
	connection, token, err := c.createConnection(ctx)
	if err != nil {
		return nil, token, err
	}
	client, err := workitemtracking.NewClient(ctx, connection)
	return client, token, err
}

func (c *Client) createLocationClient(ctx context.Context) (locationClient, string, error) {
	connection, token, err := c.createConnection(ctx)
	if err != nil {
		return nil, token, err
	}
	return location.NewClient(ctx, connection), token, nil
}

func (c *Client) createConnection(ctx context.Context) (*azuredevops.Connection, string, error) {
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("acquire Azure DevOps token: %w", err)
	}
	if token == "" {
		return nil, "", errors.New("azure DevOps token provider returned an empty token")
	}
	connection := azuredevops.NewAnonymousConnection(c.baseURL)
	connection.AuthorizationString = "Bearer " + token
	timeout := sdkTimeout
	connection.Timeout = &timeout
	return connection, token, nil
}

func (c *Client) parseWrittenWorkItem(response *workitemtracking.WorkItem, want []byte, action string) (*WorkItem, error) {
	workItem, err := c.parseWorkItem(response)
	if err != nil {
		return nil, err
	}
	got, err := MarshalSnapshot(workItem.Snapshot)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(got, want) {
		return nil, fmt.Errorf("azure DevOps returned a different %s release snapshot", action)
	}
	return workItem, nil
}

func (c *Client) parseWorkItem(response *workitemtracking.WorkItem) (*WorkItem, error) {
	if response == nil || response.Id == nil || *response.Id <= 0 || response.Rev == nil || *response.Rev <= 0 || response.Fields == nil {
		return nil, errors.New("azure DevOps returned an invalid work item")
	}
	fields := *response.Fields
	workItemType, ok := fields["System.WorkItemType"].(string)
	if !ok || workItemType != c.workItemType {
		return nil, fmt.Errorf("work item %d has unexpected type %q", *response.Id, workItemType)
	}
	areaPath, ok := fields["System.AreaPath"].(string)
	if !ok || areaPath != AreaPath {
		return nil, fmt.Errorf("work item %d has unexpected area path %q", *response.Id, areaPath)
	}
	tags, ok := fields["System.Tags"].(string)
	if !ok || !hasTag(tags, SelectorTag) {
		return nil, fmt.Errorf("work item %d is missing tag %q", *response.Id, SelectorTag)
	}
	title, titleOK := fields["System.Title"].(string)
	state, stateOK := fields["System.State"].(string)
	changedText, changedOK := fields["System.ChangedDate"].(string)
	description, snapshotOK := fields[descriptionField].(string)
	if !titleOK || strings.TrimSpace(title) == "" || !stateOK || strings.TrimSpace(state) == "" || !changedOK || !snapshotOK {
		return nil, fmt.Errorf("work item %d has incomplete managed fields", *response.Id)
	}
	changedAt, err := time.Parse(time.RFC3339Nano, changedText)
	if err != nil {
		return nil, fmt.Errorf("work item %d has invalid changed date %q", *response.Id, changedText)
	}
	snapshot, err := ParseDescription(description)
	if err != nil {
		return nil, fmt.Errorf("parse work item %d release snapshot: %w", *response.Id, err)
	}
	testTagged := hasTag(tags, TestTag) || hasTag(tags, legacyTestTag)
	if testTagged != snapshot.Test {
		return nil, fmt.Errorf("work item %d test tag does not match release snapshot", *response.Id)
	}
	if !workItemStateMatches(state, snapshot.Status) {
		return nil, fmt.Errorf("work item %d state %q is incompatible with release status %q", *response.Id, state, snapshot.Status)
	}
	return &WorkItem{
		ID: *response.Id, Revision: *response.Rev, URL: c.workItemURL(*response.Id),
		Title: title, State: state, ChangedAt: changedAt, Snapshot: snapshot,
	}, nil
}

func (c *Client) fields() []string {
	return []string{
		"System.WorkItemType", "System.Title", "System.State", "System.AreaPath", "System.Tags", "System.ChangedDate", descriptionField,
	}
}

func patch(operation webapi.Operation, path string, value any) webapi.JsonPatchOperation {
	return webapi.JsonPatchOperation{Op: &operation, Path: &path, Value: value}
}

func (c *Client) workItemURL(id int) string {
	baseURL := c.baseURL
	project := c.project
	if strings.EqualFold(project, browserProject) {
		project = browserProject
	}
	parsed, _ := url.Parse(baseURL)
	organization := strings.Trim(parsed.Path, "/")
	if parsed.Hostname() == "dev.azure.com" && organization != "" && !strings.Contains(organization, "/") {
		baseURL = parsed.Scheme + "://" + organization + ".visualstudio.com"
	}
	result, _ := url.JoinPath(baseURL, project, "_workitems", "edit", strconv.Itoa(id))
	return result
}

func hasTag(tags, want string) bool {
	for _, tag := range strings.Split(tags, ";") {
		if strings.EqualFold(strings.TrimSpace(tag), want) {
			return true
		}
	}
	return false
}

func escapeWIQL(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

func workItemTags(snapshot *Snapshot) string {
	tags := []string{SelectorTag}
	if snapshot.Test {
		tags = append(tags, TestTag)
	}
	tags = append(tags, snapshot.ProcessID)
	return strings.Join(tags, "; ")
}

func workItemState(status Status) string {
	if status == StatusSucceeded {
		return "Closed"
	}
	return "Active"
}

func workItemStateForUpdate(current string, status Status) string {
	if current == "Closed" && isAttentionStatus(status) {
		return "Closed"
	}
	return workItemState(status)
}

func workItemStateMatches(state string, status Status) bool {
	if state == workItemState(status) {
		return true
	}
	return state == "Closed" && isAttentionStatus(status)
}

func isAttentionStatus(status Status) bool {
	return status == StatusFailed || status == StatusCanceled || status == StatusUncertain
}

func isRevisionConflict(err error) bool {
	var value azuredevops.WrappedError
	if errors.As(err, &value) && value.TypeName != nil && strings.Contains(*value.TypeName, "WorkItemRevisionMismatchException") {
		return true
	}
	var pointer *azuredevops.WrappedError
	return errors.As(err, &pointer) && pointer.TypeName != nil && strings.Contains(*pointer.TypeName, "WorkItemRevisionMismatchException")
}

func redactError(err error, token string) error {
	if err == nil || token == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "[REDACTED]"))
}
