// This empty go.mod file causes the directory's contents to be excluded from go commands run in the
// root of the go-infra repository. This approach is from this comment:
// https://github.com/golang/go/issues/30058#issuecomment-543815369
//
// Exclusion is useful because the "sync" utility clones repositories into "eng/artifacts", so
// running "go build ./..." or other similar commnads would find ".go" files inside the
// sub-repository. By excluding the directory, this is avoided.
