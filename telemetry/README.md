# telemetry

[![Go Reference](https://pkg.go.dev/badge/github.com/microsoft/go-infra/telemetry.svg)](https://pkg.go.dev/github.com/microsoft/go-infra/telemetry)

This directory, the `telemetry` package, contains the telemetry transmission code for the Microsoft build of Go.
It is specialized to work similarly to the upstream telemetry counters.

The [`appinsights`](appinsights) package is an alternative client that can send more arbitrary telemetry event data to Application Insights.
It only supports a few features of Application Insights that are used in other projects maintained by the Microsoft build of Go team.
