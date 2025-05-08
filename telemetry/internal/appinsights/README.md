# appinsights

This package is a trimmed down version of https://github.com/microsoft/ApplicationInsights-Go.
It is tailored for the use of the [Microsoft build of Go](https://github.com/microsoft/go).

These are the changes made to the original package:

- Remove all external dependencies.
- Remove all telemetry types except for `Event`.
- Simplify implementation.
- Modernize the code.
- Improve testing.
