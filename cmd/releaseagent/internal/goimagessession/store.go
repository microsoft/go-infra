// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimagessession

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/atomicfile"
)

const maxDocumentSize = 4 << 20

var (
	// ErrNotFound indicates that no persisted session exists yet.
	ErrNotFound = errors.New("release session not found")
	// ErrLocked indicates that another process holds the session lease.
	ErrLocked = errors.New("release session is locked by another process")
)

// Store persists session documents. Implementations must not persist credentials.
type Store interface {
	Load(context.Context) (*Document, error)
	Save(context.Context, *Document) error
}

// FileStore atomically persists a session as JSON in a local file.
type FileStore struct {
	path string
	mu   sync.Mutex
}

// NewFileStore creates a file-backed session store.
func NewFileStore(path string) (*FileStore, error) {
	if path == "" {
		return nil, errors.New("session file path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("make session file path absolute: %w", err)
	}
	return &FileStore{path: filepath.Clean(absolute)}, nil
}

// Path returns the absolute session document path.
func (s *FileStore) Path() string {
	return s.path
}

// Load reads and validates a current or explicitly migratable session document.
func (s *FileStore) Load(ctx context.Context) (*Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var document Document
	if err := atomicfile.ReadJSON(s.path, maxDocumentSize, &document); errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, fmt.Errorf("read session file: %w", err)
	}
	if err := document.Validate(); err != nil {
		return nil, fmt.Errorf("validate session file: %w", err)
	}
	return &document, nil
}

// Save validates and atomically replaces the current session document.
func (s *FileStore) Save(ctx context.Context, document *Document) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := document.Validate(); err != nil {
		return fmt.Errorf("refuse to save invalid session: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := atomicfile.WriteJSON(s.path, ".release-session-*.tmp", maxDocumentSize, document); err != nil {
		return fmt.Errorf("write session file: %w", err)
	}
	return nil
}

// AcquireLease prevents two cooperative release UI processes from using the same session file.
// A process that terminates without releasing the lease leaves the small .lock file behind; the
// operator must verify no process is active before removing it manually.
func (s *FileStore) AcquireLease() (*Lease, error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return nil, fmt.Errorf("create session directory: %w", err)
	}
	lockPath := s.path + ".lock"
	file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("%w: %s", ErrLocked, lockPath)
	}
	if err != nil {
		return nil, fmt.Errorf("create session lease: %w", err)
	}
	metadata := struct {
		PID       int       `json:"pid"`
		StartedAt time.Time `json:"startedAt"`
	}{
		PID:       os.Getpid(),
		StartedAt: time.Now().UTC(),
	}
	if err := json.NewEncoder(file).Encode(metadata); err != nil {
		_ = file.Close()
		_ = os.Remove(lockPath)
		return nil, fmt.Errorf("write session lease: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(lockPath)
		return nil, fmt.Errorf("sync session lease: %w", err)
	}
	return &Lease{path: lockPath, file: file}, nil
}

// Lease represents exclusive cooperative ownership of a session file.
type Lease struct {
	path string
	file *os.File
	once sync.Once
	err  error
}

// Release relinquishes the session lease. It is safe to call more than once.
func (l *Lease) Release() error {
	l.once.Do(func() {
		closeErr := l.file.Close()
		removeErr := os.Remove(l.path)
		l.err = errors.Join(closeErr, removeErr)
	})
	return l.err
}
