// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package contract defines the boundary between the release UI and a release implementation.
package contract

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/microsoft/go-infra/releaseui/coordinator"
	"github.com/microsoft/go-infra/releaseui/releaseinput"
)

// ErrInvalidInput marks an error caused by operator-controlled process input. When
// [Process.Prepare] or [Process.Validate] returns an error of this type during preparation, it is
// shown beside the input form. Other preparation errors are shown differently.
var ErrInvalidInput = errors.New("invalid release input")

// ProcessGroup represents a collection of closely related release processes. For example, go-images
// might want to rebuild all images, or it might be a build that rolls back to an earlier set of
// images.
//
// Processes in a group may have significantly different sets of steps and significantly different
// inputs, so there may be very little shared between them despite the shared topic.
type ProcessGroup interface {
	// Identity returns the metadata shown for the process group. Not nil.
	Identity() *Identity

	// Processes returns the list of release processes in the group.
	//
	// The slice is presented in order, with the first considered the default if necessary.
	Processes() []Process
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

	// InputForm binds a process-specific input struct to the given [releaseinput.Set] and returns
	// the input struct. The fn is called multiple times, and each call must produce unrelated
	// instances.
	//
	// InputForm must register the same fields in the same order each time. releaseui assigns field
	// IDs by registration order.
	//
	// The output of this function may be passed back into [Process.Prepare].
	InputForm(*releaseinput.Set) any

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

	// Validate is called by releaseui to check the snapshot's input and state before releaseui
	// accepts prepared state, loads a run, or saves a captured snapshot.
	//
	// Be careful. If Validate fails, it interrupts the whole process. Validate must accept all
	// valid intermediate states that could occur during a run. It's generally better to be lenient.
	// It's acceptable for Validate to call [snapshot.Decode] and simply return Decode's error value
	// without any further inspection of the decoded data if there are no states that would cause a
	// catastrophic problem.
	//
	// During preparation, Validate may return [ErrInvalidInput] to indicate that operator input
	// is not valid. Releaseui shows this error beside the input form rather than as a general error.
	//
	// Validate must be deterministic and must not modify the snapshot or use an external service.
	Validate(*StateSnapshot) error

	// Load decodes a snapshot and constructs its [Run] without starting it.
	//
	// Load may return an error instead of a Run to indicate it wasn't able to load the snapshot.
	// Releaseui calls [Validate] before calling this method, so [Load] should not call [Validate].
	Load(*StateSnapshot) (Run, error)
}

// Run represents one release throughout its entire lifecycle. It carries the run's current state,
// which is expected to change over time.
type Run interface {
	// TakeSnapshot returns a snapshot of the run's state at this point in time, or an error if
	// capture or encoding fails. The returned snapshot must not be modified after being returned.
	//
	// The host serializes TakeSnapshot and TakeView with state changes made through
	// [coordinator.StepControl.Update]. The Run doesn't need its own state lock.
	//
	// Run state is expected to be stored in the Run itself. It's initialized when the Run is
	// created. When the steps run, they mutate the state. [TakeSnapshot] is expected to be a copy
	// of that mutated state.
	TakeSnapshot() (*StateSnapshot, error)

	// TakeView returns a read-only view of the run's current state. The returned view must not be
	// modified after being returned. Views are not persisted.
	//
	// The host serializes TakeView with state changes made through
	// [coordinator.StepControl.Update]. The Run doesn't need its own state lock.
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
	// returned steps may do so. Steps must change run state through
	// [coordinator.StepControl.Update], which persists the state before returning.
	//
	// This method is called at most once per instance of [Run]. It always constructs executable
	// steps. Observers use the runner's published display data and never call Build.
	Build() ([]*coordinator.Step, error)
}

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

	// ID is a stable identifier stored in release records and used in URLs. It must be kebab-case
	// (lowercase alphanumeric with "-" permitted in the middle as a separator). It must be unique
	// among all types of release process tracked by releaseui through all time. When releaseui
	// unmarshals a release record, it uses this ID to select the process that owns it.
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

	// RunURL links to the external pipeline or workflow run. Empty until a run is known.
	RunURL string

	// Some status indicators are derived from the steps or other server state by releaseui, and are
	// not included here:
	//
	//   - Started: this indicates that this instance is actually running the steps.
	//   - Complete: this indicates that the run has reached a terminal result.
	//   - UpdatedAt: the host records when it last published the view, including heartbeats.
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
