package state

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
)

// Memory is a Backend in memory, for tests.
type Memory struct {
	mu       sync.Mutex
	states   map[string][][]byte // every version of each state key, oldest first
	locks    map[string]LockInfo
	zips     map[string][]byte
	Bucket   string
	puts     int
	FailNext error // returned by the next Write, then cleared
}

// NewMemory returns an empty Memory.
func NewMemory() *Memory {
	return &Memory{
		states: map[string][][]byte{},
		locks:  map[string]LockInfo{},
		zips:   map[string][]byte{},
		Bucket: "koldstart-state-000000000000-eu-west-1",
	}
}

func etagOf(versions [][]byte) string {
	if len(versions) == 0 {
		return ""
	}
	return fmt.Sprintf(`"%d"`, len(versions))
}

// Read implements Backend.
func (m *Memory) Read(_ context.Context, app, stage string) (*State, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	versions := m.states[stateKey(app, stage)]
	if len(versions) == 0 || versions[len(versions)-1] == nil {
		return nil, etagOf(versions), nil
	}
	s, err := Decode(versions[len(versions)-1])
	return s, etagOf(versions), err
}

// Write implements Backend.
func (m *Memory) Write(_ context.Context, s *State, etag string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.FailNext; err != nil {
		m.FailNext = nil
		return "", err
	}
	key := stateKey(s.App, s.Stage)
	versions := m.states[key]
	current := etagOf(versions)
	if len(versions) > 0 && versions[len(versions)-1] == nil {
		current = "" // deleted: only a write that expects nothing succeeds
	}
	if etag != current {
		return "", ErrConflict
	}
	data, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	m.states[key] = append(versions, data)
	m.puts++
	return etagOf(m.states[key]), nil
}

// Writes counts the successful writes, for tests that check state is written after each step.
func (m *Memory) Writes() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.puts
}

// Delete implements Backend.
func (m *Memory) Delete(_ context.Context, app, stage string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := stateKey(app, stage)
	m.states[key] = append(m.states[key], nil)
	return nil
}

// Lock implements Backend.
func (m *Memory) Lock(_ context.Context, app, stage string, info LockInfo) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if held, ok := m.locks[lockKey(app, stage)]; ok {
		return &LockedError{Stage: stage, Info: held}
	}
	m.locks[lockKey(app, stage)] = info
	return nil
}

// Unlock implements Backend.
func (m *Memory) Unlock(_ context.Context, app, stage string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.locks[lockKey(app, stage)]; !ok {
		return ErrNotLocked
	}
	delete(m.locks, lockKey(app, stage))
	return nil
}

// LockInfo implements Backend.
func (m *Memory) LockInfo(_ context.Context, app, stage string) (*LockInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if info, ok := m.locks[lockKey(app, stage)]; ok {
		return &info, nil
	}
	return nil, nil
}

// PutZip implements Backend.
func (m *Memory) PutZip(_ context.Context, app, stage, sha string, data []byte) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := zipKey(app, stage, sha)
	if _, ok := m.zips[key]; !ok {
		m.zips[key] = data
	}
	return m.Bucket, key, nil
}

// Zip returns a stored zip, for tests.
func (m *Memory) Zip(key string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.zips[key]
	return data, ok
}

// History implements Backend.
func (m *Memory) History(_ context.Context, app, stage string) ([]ObjectVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	versions := m.states[stateKey(app, stage)]
	out := make([]ObjectVersion, 0, len(versions))
	for i, v := range slices.Backward(versions) {
		if v == nil {
			continue
		}
		out = append(out, ObjectVersion{ID: fmt.Sprint(i + 1), LastModified: time.Unix(int64(i), 0), Latest: i == len(versions)-1})
	}
	return out, nil
}

// ReadVersion implements Backend.
func (m *Memory) ReadVersion(_ context.Context, app, stage, version string) (*State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	versions := m.states[stateKey(app, stage)]
	var i int
	if _, err := fmt.Sscan(version, &i); err != nil || i < 1 || i > len(versions) || versions[i-1] == nil {
		return nil, fmt.Errorf("no version %s of the state of %s/%s", version, app, stage)
	}
	return Decode(versions[i-1])
}

// Stages implements Backend.
func (m *Memory) Stages(_ context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, key := range slices.Sorted(maps.Keys(m.states)) {
		versions := m.states[key]
		if len(versions) == 0 || versions[len(versions)-1] == nil {
			continue
		}
		out = append(out, strings.TrimSuffix(key, "/state.json"))
	}
	return out, nil
}
