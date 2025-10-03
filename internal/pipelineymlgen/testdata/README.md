This directory contains test files for pipelineymlgen.

## TestEndToEnd

This tests the full behavior of the `pipelineymlgen` command.
It uses `output` to create output files.

Some of these test files look more like real Azure Pipelines code.
They are mostly created by Copilot and should be taken with a grain of salt, but it is sometimes useful to have the "real" pipeline context while examining test cases.

## TestIndividualFileErrors

These test files are evaluated individually, must fail, and the error output is checked.
This helps make sure error messages are relatively readable and stay consistent.
However, they're easy to update if changes are intentional due to being golden tests.

## TestIndividualFiles

These test files are evaluated individually, must succeed, and the output is checked.
They should be as simple as possible (to aid debugging).
Specific behaviors are checked by each one.

It should be easy for a human to review the output and determine whether or not it's the expected result.
Use comments if necessary, or self-explanatory output.

Some behaviors are subtle and may evaluate without emitting an error, but not in a useful way, and these tests should make this easy for a reviewer to catch.
