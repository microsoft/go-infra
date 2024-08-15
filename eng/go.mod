// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// This empty go.mod file causes the directory's contents to be excluded from go commands run in the
// root of the go-infra repository. This approach is from this comment:
// https://github.com/golang/go/issues/30058#issuecomment-543815369
//
// We exclude this directory because the "sync" utility clones repositories into "eng/artifacts".
// After running "sync", running "go build ./..." or other similar commands in the go-infra
// directory would find ".go" files inside the sub-repository and try to build them. Even if it
// succeeds, it is not intentional, and would waste time.
//
// This file contains just enough info to be a valid go.mod file. A blank file is sufficient to
// exclude the directory from Go commands, but IDEs may expect a valid go.mod file.

module unused

go 1.18
