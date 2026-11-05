package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
)

// ErrCrash is what Fake returns when it is told to stop as a killed deploy would: after the call
// to aws went through, before koldstart heard back.
var ErrCrash = errors.New("crashed")

// Fake is a provider that keeps resources in memory, for testing the engine.
type Fake struct {
	mu        sync.Mutex
	resources map[string]FakeResource
	calls     []string
	// ReplaceKeys are inputs that need a new resource to change.
	ReplaceKeys []string
	// CrashAfter makes the n-th create, update or delete, counting from 1, crash after it
	// went through. 0 never crashes.
	CrashAfter int
	// Fail makes the call named, such as create:function/api, fail before it goes through.
	Fail    map[string]error
	mutated int
	// LastPrior are the prior inputs of the last update, as the provider was given them.
	LastPrior map[string]any
}

// FakeResource is one resource Fake holds.
type FakeResource struct {
	// ID is the node it was made for.
	ID     string
	Inputs map[string]any
	Tags   map[string]string
}

// NewFake returns an empty Fake.
func NewFake() *Fake {
	return &Fake{resources: map[string]FakeResource{}}
}

// Resources returns what Fake holds, by physical id.
func (f *Fake) Resources() map[string]FakeResource {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.resources)
}

// Calls returns the calls made, such as create:function/api.
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// ResetCalls forgets the calls made.
func (f *Fake) ResetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

func (f *Fake) attrs(id string) map[string]string {
	return map[string]string{"Arn": "arn:fake:" + id, "Name": id}
}

// mutate records a call, fails it when told to, and crashes after it when told to.
func (f *Fake) mutate(op, id string, apply func()) error {
	f.calls = append(f.calls, op+":"+id)
	if err := f.Fail[op+":"+id]; err != nil {
		return err
	}
	apply()
	f.mutated++
	if f.CrashAfter > 0 && f.mutated == f.CrashAfter {
		return ErrCrash
	}
	return nil
}

func decodeInputs(data json.RawMessage) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	return m
}

// Create implements Provider.
func (f *Fake) Create(_ context.Context, req Request) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.Name == "" {
		return Result{}, fmt.Errorf("fake resources need a name")
	}
	if _, ok := f.resources[req.Name]; ok {
		f.calls = append(f.calls, "create:"+req.ID)
		return Result{}, ErrExists
	}
	err := f.mutate("create", req.ID, func() {
		f.resources[req.Name] = FakeResource{ID: req.ID, Inputs: decodeInputs(req.Inputs), Tags: req.Tags}
	})
	return Result{PhysicalID: req.Name, Attributes: f.attrs(req.Name)}, err
}

// Read implements Provider.
func (f *Fake) Read(_ context.Context, req Request) (Result, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "read:"+req.ID)
	r, ok := f.resources[req.PhysicalID]
	if !ok {
		return Result{}, false, nil
	}
	if r.Tags[TagNode] != req.ID {
		return Result{}, false, fmt.Errorf("%s %w", req.PhysicalID, ErrNotOurs)
	}
	return Result{PhysicalID: req.PhysicalID, Attributes: f.attrs(req.PhysicalID)}, true, nil
}

// Update implements Provider.
func (f *Fake) Update(_ context.Context, req Request) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.resources[req.PhysicalID]
	if !ok {
		return Result{}, fmt.Errorf("update of %s, which does not exist", req.PhysicalID)
	}
	f.LastPrior = decodeInputs(req.Prior)
	err := f.mutate("update", req.ID, func() {
		r.Inputs = decodeInputs(req.Inputs)
		r.Tags = req.Tags
		f.resources[req.PhysicalID] = r
	})
	return Result{PhysicalID: req.PhysicalID, Attributes: f.attrs(req.PhysicalID)}, err
}

// Delete implements Provider.
func (f *Fake) Delete(_ context.Context, req Request) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mutate("delete", req.ID, func() { delete(f.resources, req.PhysicalID) })
}

// Replaces implements Provider.
func (f *Fake) Replaces(prior, desired json.RawMessage) bool {
	before, after := decodeInputs(prior), decodeInputs(desired)
	for _, k := range f.ReplaceKeys {
		b, _ := json.Marshal(before[k])
		a, _ := json.Marshal(after[k])
		if string(a) != string(b) {
			return true
		}
	}
	return false
}
