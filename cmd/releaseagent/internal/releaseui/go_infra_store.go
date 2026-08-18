// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

const (
	goInfraActionSchemaVersion = 1
	maxGoInfraActionSize       = 64 << 10
)

var ErrGoInfraActionNotFound = errors.New("go-infra action not found")

// GoInfraActionStore persists reviewed go-infra action intent without credentials.
type GoInfraActionStore interface {
	Load(context.Context) (*goInfraPlan, error)
	Save(context.Context, *goInfraPlan) error
}

type goInfraActionDocument struct {
	SchemaVersion int         `json:"schemaVersion"`
	Plan          goInfraPlan `json:"plan"`
	UpdatedAt     time.Time   `json:"updatedAt"`
}

// GoInfraActionFileStore atomically persists one go-infra action plan.
type GoInfraActionFileStore struct {
	path string
	mu   sync.Mutex
}

// NewGoInfraActionFileStore creates a file store for the go-infra action journal.
func NewGoInfraActionFileStore(path string) (*GoInfraActionFileStore, error) {
	if path == "" {
		return nil, errors.New("go-infra action file path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("make go-infra action path absolute: %w", err)
	}
	return &GoInfraActionFileStore{path: filepath.Clean(absolute)}, nil
}

func (s *GoInfraActionFileStore) Load(ctx context.Context) (*goInfraPlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.Open(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrGoInfraActionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("open go-infra action: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat go-infra action: %w", err)
	}
	if info.Size() > maxGoInfraActionSize {
		return nil, fmt.Errorf("go-infra action file is %d bytes, maximum is %d", info.Size(), maxGoInfraActionSize)
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxGoInfraActionSize))
	decoder.DisallowUnknownFields()
	var document goInfraActionDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode go-infra action: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("go-infra action file must contain exactly one JSON value")
	}
	if document.SchemaVersion != goInfraActionSchemaVersion {
		return nil, fmt.Errorf("unsupported go-infra action schema %d", document.SchemaVersion)
	}
	if err := validateStoredGoInfraPlan(&document.Plan); err != nil {
		return nil, fmt.Errorf("validate go-infra action: %w", err)
	}
	return cloneGoInfraPlan(&document.Plan), nil
}

func (s *GoInfraActionFileStore) Save(ctx context.Context, plan *goInfraPlan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateStoredGoInfraPlan(plan); err != nil {
		return fmt.Errorf("refuse to save invalid go-infra action: %w", err)
	}
	document := goInfraActionDocument{
		SchemaVersion: goInfraActionSchemaVersion,
		Plan:          *cloneGoInfraPlan(plan),
		UpdatedAt:     time.Now().UTC(),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create go-infra action directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".go-infra-action-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary go-infra action: %w", err)
	}
	temporaryPath := temporary.Name()
	keepTemporary := false
	defer func() {
		if !keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("restrict temporary go-infra action: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("encode go-infra action: %w", err)
	}
	info, err := temporary.Stat()
	if err != nil {
		_ = temporary.Close()
		return fmt.Errorf("stat temporary go-infra action: %w", err)
	}
	if info.Size() > maxGoInfraActionSize {
		_ = temporary.Close()
		return fmt.Errorf("encoded go-infra action is %d bytes, maximum is %d", info.Size(), maxGoInfraActionSize)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary go-infra action: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary go-infra action: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace go-infra action: %w", err)
	}
	keepTemporary = true
	if err := os.Chmod(s.path, 0o600); err != nil {
		return fmt.Errorf("restrict go-infra action: %w", err)
	}
	return syncGoInfraActionDirectory(filepath.Dir(s.path))
}

func validateStoredGoInfraPlan(plan *goInfraPlan) error {
	if plan == nil {
		return errors.New("go-infra plan is nil")
	}
	normalized, pullRequestNumber, err := normalizeGoInfraPlanInput(plan.Input)
	if err != nil || normalized != plan.Input {
		return errors.New("go-infra plan input is invalid")
	}
	switch plan.Input.Action {
	case goInfraActionReleaseOnMerge:
		if plan.PullRequest == nil {
			return errors.New("release-on-merge plan has no pull request")
		}
		if err := validateGoInfraPullRequest(*plan.PullRequest, pullRequestNumber); err != nil {
			return err
		}
	case goInfraActionManualDispatch:
		if plan.PullRequest != nil {
			return errors.New("manual-dispatch plan contains a pull request")
		}
	}
	digest, err := goInfraPlanDigest(plan.Input, plan.PullRequest)
	if err != nil || !secureEqual(digest, plan.Digest) {
		return errors.New("go-infra plan digest does not match its content")
	}
	if plan.Complete && !plan.Started {
		return errors.New("go-infra plan completed before it started")
	}
	if !plan.Complete && plan.Result != "" {
		return errors.New("incomplete go-infra plan has a result")
	}
	if plan.Complete && plan.Result != "succeeded" && plan.Result != "failed" && plan.Result != "uncertain" {
		return fmt.Errorf("completed go-infra plan has invalid result %q", plan.Result)
	}
	return nil
}

func cloneGoInfraPlan(plan *goInfraPlan) *goInfraPlan {
	if plan == nil {
		return nil
	}
	clone := *plan
	if plan.PullRequest != nil {
		pullRequest := *plan.PullRequest
		pullRequest.Labels = append([]string(nil), plan.PullRequest.Labels...)
		clone.PullRequest = &pullRequest
	}
	return &clone
}

func syncGoInfraActionDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open go-infra action directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync go-infra action directory: %w", err)
	}
	return nil
}
