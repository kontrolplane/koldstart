// Package state is what koldstart deployed: one json object per app and stage, a lock beside it,
// and the zips of the functions, all in a bucket of the account.
package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"
)

// Version is the version of the state's json. A state with a newer one is refused.
const Version = 1

// State is what one stage of an app deployed.
type State struct {
	Version int    `json:"version"`
	App     string `json:"app"`
	Stage   string `json:"stage"`
	Region  string `json:"region"`
	Account string `json:"account"`
	// Serial counts the writes, so two copies can be told apart.
	Serial     int64     `json:"serial"`
	DeployedAt time.Time `json:"deployedAt"`
	DeployedBy string    `json:"deployedBy"`
	// Resources are what exists, by the id of their node, such as function/api.
	Resources map[string]*Resource `json:"resources"`
	// Retained are resources remove left in place, adopted again by the next deploy.
	Retained map[string]*Resource `json:"retained,omitempty"`
	Outputs  map[string]string    `json:"outputs,omitempty"`
}

// Resource is one thing in aws koldstart manages.
type Resource struct {
	// Type is the kind of resource, such as AWS::Lambda::Function.
	Type string `json:"type"`
	// PhysicalID is its id in aws: a name or an arn.
	PhysicalID string `json:"physicalId"`
	// Inputs are the properties it was last applied with, refs resolved and secrets left out.
	Inputs json.RawMessage `json:"inputs,omitempty"`
	// Hash is the hash of the inputs it was last applied with, secrets included.
	Hash string `json:"hash"`
	// SecretsHash is the hash of the values of its secrets alone, which tells a provider whether
	// they changed, as the inputs hold only references to them.
	SecretsHash string `json:"secretsHash,omitempty"`
	// Attributes are what ref() reads, such as Arn.
	Attributes map[string]string `json:"attributes,omitempty"`
	// Dependencies are the ids it was applied after, so it is deleted before them.
	Dependencies []string `json:"dependencies,omitempty"`
	// DeletionPolicy is retain when remove and a replacement must leave it in place.
	DeletionPolicy string `json:"deletionPolicy,omitempty"`
	// Pending is an operation that was started and may not have finished.
	Pending *Pending `json:"pending,omitempty"`
	// Imported is a resource kdst import took over. It is retained unless the configuration
	// says delete.
	Imported bool `json:"imported,omitempty"`
}

// Pending records an operation before it is made, so a deploy that dies halfway knows on the
// next run what may exist.
type Pending struct {
	// Operation is create, update, replace or delete.
	Operation string    `json:"operation"`
	StartedAt time.Time `json:"startedAt"`
	// Token is the request token of an asynchronous operation, such as one of cloud control.
	Token string `json:"token,omitempty"`
}

// New returns the empty state of a stage that was never deployed.
func New(app, stage, region, account string) *State {
	return &State{
		Version:   Version,
		App:       app,
		Stage:     stage,
		Region:    region,
		Account:   account,
		Resources: map[string]*Resource{},
	}
}

// IDs returns the ids of the resources, sorted.
func (s *State) IDs() []string {
	return slices.Sorted(maps.Keys(s.Resources))
}

// Clone returns a deep copy, safe to change while the original is being written.
func (s *State) Clone() *State {
	data, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("state does not marshal: %v", err))
	}
	var out State
	if err := json.Unmarshal(data, &out); err != nil {
		panic(fmt.Sprintf("state does not unmarshal: %v", err))
	}
	if out.Resources == nil {
		out.Resources = map[string]*Resource{}
	}
	return &out
}

// Decode reads a state, refusing one written by a newer koldstart.
func Decode(data []byte) (*State, error) {
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("reading the state: %w", err)
	}
	if s.Version > Version {
		return nil, fmt.Errorf("the state has version %d, this koldstart reads up to %d: upgrade koldstart", s.Version, Version)
	}
	if s.Resources == nil {
		s.Resources = map[string]*Resource{}
	}
	return &s, nil
}

// LockInfo says who holds a stage's lock.
type LockInfo struct {
	Who       string    `json:"who"`
	Where     string    `json:"where"`
	Operation string    `json:"operation"`
	Since     time.Time `json:"since"`
}

func (l LockInfo) String() string {
	return fmt.Sprintf("%s on %s, %s since %s", l.Who, l.Where, l.Operation, l.Since.Local().Format(time.DateTime))
}

// LockedError is a lock someone else holds.
type LockedError struct {
	Stage string
	Info  LockInfo
}

func (e *LockedError) Error() string {
	return fmt.Sprintf("stage %s is locked by %s: wait for it, or run unlock when that deploy is gone", e.Stage, e.Info)
}

// ErrConflict is a write of a state that changed since it was read: another deploy wrote it.
var ErrConflict = errors.New("the state changed since it was read: another deploy of this stage ran")

// ErrNotLocked is an unlock of a stage nobody holds.
var ErrNotLocked = errors.New("the stage is not locked")

// ObjectVersion is one version of a state, as a listing of its history returns it.
type ObjectVersion struct {
	ID           string
	LastModified time.Time
	Latest       bool
}

// Backend stores states, locks and zips.
type Backend interface {
	// Read returns a stage's state and its etag, or a nil state when it was never deployed.
	Read(ctx context.Context, app, stage string) (*State, string, error)
	// Write stores the state when its etag is still etag, the empty etag meaning it must not
	// exist yet, and returns the new etag. Otherwise it returns ErrConflict.
	Write(ctx context.Context, s *State, etag string) (string, error)
	// Delete removes a stage's state.
	Delete(ctx context.Context, app, stage string) error

	// Lock takes a stage's lock, or returns a *LockedError with who holds it.
	Lock(ctx context.Context, app, stage string, info LockInfo) error
	// Unlock releases a stage's lock, or returns ErrNotLocked.
	Unlock(ctx context.Context, app, stage string) error
	// LockInfo returns who holds a stage's lock, or nil.
	LockInfo(ctx context.Context, app, stage string) (*LockInfo, error)

	// PutZip stores a function's zip under ZipKey, unless it is there already.
	PutZip(ctx context.Context, app, stage, sha256 string, data []byte) error

	// History lists the versions of a stage's state, newest first.
	History(ctx context.Context, app, stage string) ([]ObjectVersion, error)
	// ReadVersion returns one version of a stage's state.
	ReadVersion(ctx context.Context, app, stage, version string) (*State, error)
	// Stages lists the apps and stages with a state, as app/stage.
	Stages(ctx context.Context) ([]string, error)
}

// The keys of the bucket: a stage's state and lock, and its functions' zips by their sha256.
func stateKey(app, stage string) string { return app + "/" + stage + "/state.json" }
func lockKey(app, stage string) string  { return app + "/" + stage + "/lock" }

// ZipKey is where a function's zip goes in the state bucket, which lambda reads it from.
func ZipKey(app, stage, sha string) string {
	return "zips/" + app + "/" + stage + "/" + sha + ".zip"
}
