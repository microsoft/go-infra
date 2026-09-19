// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package contract defines the boundary between the release UI and a release implementation.
package contract

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/microsoft/go-infra/releaseui/coordinator"
	"github.com/microsoft/go-infra/releaseui/releaseflag"
)

// ErrInvalidInput marks an error caused by operator-controlled process input. When
// [Process.Prepare] returns an error of this type, it is shown as an input validation error. Other
// preparation errors are shown differently.
var ErrInvalidInput = errors.New("invalid release input")

// ProcessGroup represents a collection of closely related release processes. For example, go-images
// might want to rebuild all images, or it might be a build that rolls back to an earlier set of
// images.
//
// Processes in a group may have significantly different sets of steps and significantly different
// inputs, so there may be very little shared between them despite the shared topic.
type ProcessGroup interface {
	// Processes returns the list of release processes in the group.
	//
	// A struct may implement both ProcessGroup and Process and return itself as the sole element of
	// the list, as a shortcut for single-process groups.
	//
	// The slice is presented in order, with the first considered the default if necessary.
	Processes() []Process
}

// ProcessGroupIdentity is implemented by a ProcessGroup if it has an identity that is distinct from
// the identity of the default (first) process in the group.
type ProcessGroupIdentity interface {
	// ProcessGroupIdentity returns the identity of the process group. Not nil.
	ProcessGroupIdentity() *Identity
}

// Process describes one kind of release, such as a Go-images test release.
//
// A Process validates browser input, prepares new runs, and restores persisted runs. Its methods
// must not mutate an external service. External mutations belong in the steps returned by
// [Run.Build].
type Process interface {
	// Definition returns the process metadata information shown by the release UI.
	Definition() ProcessDefinition

	// Preflight performs non-mutating readiness checks for preparing and starting runs for this
	// type of process. If this fails with a blocking error, the user is not permitted to start a
	// new release. The user can always resume an existing run, although the UI advises against it
	// if Preflight returns an error. A warning result doesn't block creating a new release but does
	// advise against it.
	Preflight(context.Context) (warning error, blocking error)

	// InputForm binds a process-specific input struct to the given InputSet and returns the input
	// struct. The fn is called multiple times, and each call must produce unrelated instances.
	//
	// The output of this function may be passed back into [Process.Prepare].
	InputForm(*releaseflag.InputSet) any

	// Prepare validates the user's selections and returns a StateSnapshot of a run that has not yet
	// started.
	//
	// If the user chooses to start this prepared run, releaseui will pass it to [Load] to create
	// the [Run].
	//
	// inputFormResult contains the parsed results from the inputs. It is the same type returned by
	// the [InputForm] method on this instance. Use a type assertion to convert it to the expected
	// type.
	Prepare(ctx context.Context, inputFormResult any) (*StateSnapshot, error)

	// Load examines state from a snapshot and constructs its [Run] without starting it.
	//
	// Load may return an error instead of a Run to indicate it wasn't able to load the snapshot.
	// Consider making [Load] more lenient than [Prepare], because the user can't necessarily
	// correct errors in a persisted snapshot.
	Load(*StateSnapshot) (Run, error)
}

// Run represents one release throughout its entire lifecycle. It carries the run's current state,
// which is expected to change over time.
type Run interface {
	// TakeSnapshot returns a snapshot of the run's state at this point in time. The returned
	// snapshot must not be modified after being returned.
	//
	// TakeSnapshot may be called concurrently with step execution or another TakeSnapshot call.
	// The Run implementation must synchronize access to its state.
	//
	// Run state is expected to be stored in the Run itself. It's initialized when the Run is
	// created. When the steps run, they mutate the state. [TakeSnapshot] is expected to be a copy
	// of that mutated state.
	TakeSnapshot() *StateSnapshot

	// TakeView returns a read-only view of the run's current state. The returned view must not be
	// modified after being returned. Views are not persisted.
	//
	// TakeView may be called concurrently with step execution or another Run method. The Run
	// implementation must synchronize access to its state.
	//
	// This view contains extra details that are useful for displaying on the UI.
	TakeView() *RunView

	// Plan returns the human-reviewable plan for the run. It may return nil if there is no useful
	// information to review. A nil plan produces an empty confirmation page.
	//
	// Plan does not change the Run state or mutate an external service.
	Plan() *Plan

	// Build sets up the executable graph for the run.
	//
	// Build does not change the Run state or mutate an external service. Only execution of the
	// returned steps may do so.
	//
	// This method is called at most once per instance of [Run].
	Build(context.Context, CheckpointFunc) ([]*coordinator.Step, error)
}

// CheckpointFunc must be called by a Run's steps after changing state. This allows releaseui to
// persist the state and, when useful, refresh the view.
//
// The CheckpointFunc may schedule [Run.TakeSnapshot] and [Run.TakeView] to be called from another
// goroutine. These calls do not happen in the current goroutine, and CheckpointFunc doesn't block
// to wait for persistence to complete.
//
// Multiple steps may call the CheckpointFunc concurrently. If checkpointing or persistence fails,
// releaseui cancels the context passed to [Run.Build], just as it would for any other cancellation.
// The step should cooperate with cancellation by checking the context after the func returns.
type CheckpointFunc func()

// Identity contains the basic identity metadata for a release process or group.
type Identity struct {
	// Name is the process name shown to an operator. Required.
	Name string

	// Mark is the short process label shown on dashboard cards. Optional.
	Mark string

	// Description summarizes what the process releases. Optional.
	Description string

	// DocumentationURL links to the canonical release instruction documentation. Optional.
	DocumentationURL string
}

// ProcessDefinition contains the process metadata and input form shown by the release UI.
type ProcessDefinition struct {
	Identity

	// ID is a stable identifier stored in work items and used in URLs. It must be kebab-case
	// (lowercase alphanumeric with "-" permitted in the middle as a separator). It must be unique
	// among all types of release process tracked by releaseui through all time, because this ID is
	// used to distinguish them as stored in an external location (work items).
	//
	// Change ID when a [StateSnapshot] becomes incompatible with previous versions. This prevents
	// misinterpretation of state stored by older versions.
	ID string

	// InputPreamble provides general information to the release runner that may be important to
	// know when filling in input fields but isn't clear from the individual input field interfaces.
	// Optional.
	InputPreamble string

	// Notice explains to the user any important constraints that apply to this process that warrant
	// a specific callout. Optional.
	Notice *Notice

	// InputSubmitLabel labels the button that prepares the Run. Optional.
	InputSubmitLabel string
}

type Notice struct {
	// Title labels the notice. Required.
	Title string

	// Message contains the notice content. Optional.
	Message string
}

// StateSnapshot is the process-specific state of one release run.
//
// This state should be enough to reconstruct the Run state accurately, and it should contain no
// information that is not necessary for reconstructing the run.
type StateSnapshot struct {
	// Input is the input used to prepare the run, such as input from the browser form.
	Input json.RawMessage

	// State is the process-specific state that changes through the course of the Run.
	State json.RawMessage
}

type RunView struct {
	// Test indicates whether the run is a test run, e.g. a dry run.
	Test bool

	// Summary is the short status shown by the UI.
	Summary string

	// Detail adds process-specific status information.
	Detail string

	// UpdatedAt is the time of the last update that is considered relevant by the [Run]. Some steps
	// may make progress or poll a resource without committing state updates, and this field
	// provides the [Run] with a way to positively indicate to the user that the process is still
	// working.
	UpdatedAt time.Time

	// Completed is the number of completed units out of [Total]. A process may use this to report
	// quantifiable progress.
	Completed int

	// Total is the total number of units that must complete, or 0 to omit a progress indicator.
	Total int

	// Some status indicators are derived from the steps or other server state by releaseui, and are
	// not included here:
	//
	//   - Started: this indicates that this instance is actually running the steps.
	//   - Complete: this indicates that the run has reached a terminal result.
}

// Plan contains review content for the user, to help them notice if they don't really want to
// execute a Run.
type Plan struct {
	// Subtitle summarizes the content.
	Subtitle string

	// Facts lists a series of facts about what the release will do.
	Facts []PlanFact

	// ExecutionButtonLabel labels the final command that starts the run.
	ExecutionButtonLabel string
}

// PlanFact is one resolved value shown while reviewing a plan.
type PlanFact struct {
	// Label names the fact.
	Label string

	// Value is the primary resolved value.
	Value string

	// Detail adds supporting information. Include HTML-formatted links if relevant.
	// Process implementations are trusted, so releaseui renders this content as trusted HTML.
	Detail string
}
