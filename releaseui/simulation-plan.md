# Simulation

Simulation is intentionally not part of the release UI contract.

The previous simulation replaced each release step with a delay.
It demonstrated the step graph and progress UI, but it did not exercise process behavior, external request construction, checkpointing, restoration, or failure handling.
That made it a UI animation rather than a useful release check.

Simulation can return when a concrete release process needs it and the behavior can be defined without weakening the execution contract.
A clean integration would model simulation as a separate process in the same `ProcessGroup`.
The simulation process could reuse the production process's input and planning code while returning steps backed by a purpose-built fake service.
It would have its own stable process ID, state format, plan text, and persistence policy.
Releaseui would then run it through the normal `Prepare`, `Load`, `Plan`, `Build`, and checkpoint lifecycle instead of adding a simulation mode to `Run.Build`.

Before adding this, define which production guarantees the simulation is expected to check.
If it cannot validate more than graph rendering, focused contract and browser tests are a better fit.
