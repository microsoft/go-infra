# Release UI cleanup TODO

## Confirmed cleanup

- [x] Remove unused `goInfraReleaseLabel` and `goInfraWorkflowURL` constants.
- [x] Remove the unreachable `restoredRunMonitor` implementation.
- [x] Remove the no-op `validateProcessExecutionConfiguration` method and its call.
- [x] Remove the unused `.hero-stat` CSS rules.
- [x] Remove unpopulated dashboard `RunID` and `RunLabel` fields and their JavaScript branch.
- [x] Remove the unpopulated workflow description field and UI element.
- [x] Remove `processRunResponse.View`, which the browser does not read.
- [x] Remove unpopulated pipeline-run link fields and their unreachable browser code.
- [x] Remove the always-hidden intent banner and request preview HTML, JavaScript, and CSS.
- [x] Remove the unreachable `fact.href` JavaScript branch.
- [x] Replace the ineffective `sessionId` refresh guard with a `variantId` comparison.
- [x] Remove unused JavaScript locals and stored properties in `workflow.js`.
- [x] Move the test-only version resolver adapter from production code into `rollback_test.go`; retain the `VersionResolver` interface.
- [x] Remove the unused `Document.WithState` method and its dedicated test.
- [x] Remove obsolete `Document.State` and `Document.UpdatedAt`; checkpoints own current workflow state and update time.
- [x] Pass `Document.ExecutionDigest`, rather than the session ID, to the Go-images execution service.
- [x] Remove unused `RunView.Completed` and `RunView.Total`; step progress remains owned by `coordinator.StepProgress`.
- [x] Remove the always-true execution `Eligible` response field and its browser checks.
- [x] Move the test-only Azure work-item description renderer out of production code.

## Architecture

- [ ] Make `releaseui` storage-neutral, then move `azdo/workitem` under `cmd/releaseui/internal/azdo` with the pipeline and repository clients.

## Verify

- [x] Run release UI tests, race tests, vet, Staticcheck, and whole-program dead-code analysis after cleanup.