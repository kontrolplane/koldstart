// Package engine plans and applies the difference between the nodes a configuration asks for
// and what the state says exists, through one provider per type of resource.
package engine

import (
	"context"
	"encoding/json"
	"errors"
)

// Request is what a provider is asked to do for one node.
type Request struct {
	// ID is the node's id, such as function/api.
	ID string
	// Name is the physical name the node gets, for providers whose resources are named.
	Name string
	// PhysicalID is the resource's id in aws, empty on a create.
	PhysicalID string
	// Inputs are the desired properties, refs and secrets resolved.
	Inputs json.RawMessage
	// Prior are the properties it was last applied with, empty on a create.
	Prior json.RawMessage
	// Attributes are what the state holds of it, empty on a create.
	Attributes map[string]string
	// Tags go on everything koldstart creates.
	Tags map[string]string
	// Token is the request token of an operation a crashed deploy started.
	Token string
	// ClientToken is the same for every try of one operation, so a provider whose api takes an
	// idempotency token makes the resource once however often a crashed deploy retries.
	ClientToken string
	// Progress records a request token before waiting on it, so a deploy that dies while
	// waiting can find the operation again.
	Progress func(token string) error
}

// Result is what exists after a provider call.
type Result struct {
	PhysicalID string
	Attributes map[string]string
}

// Provider creates, reads, updates and deletes one type of resource.
type Provider interface {
	Create(ctx context.Context, req Request) (Result, error)
	// Read returns the resource as it is, and false when it does not exist.
	Read(ctx context.Context, req Request) (Result, bool, error)
	Update(ctx context.Context, req Request) (Result, error)
	// Delete removes the resource; one that does not exist is not an error.
	Delete(ctx context.Context, req Request) error
	// Replaces reports whether going from prior to desired needs a new resource.
	Replaces(prior, desired json.RawMessage) bool
}

// ErrExists is a create of a resource that already exists. The engine then reads it, and takes
// it over when its tags say a crashed deploy of this node created it.
var ErrExists = errors.New("already exists")

// ErrNotOurs is a resource that exists with the node's name but that koldstart did not create.
var ErrNotOurs = errors.New("exists, but koldstart did not create it")

// Secrets resolves secret references.
type Secrets interface {
	Resolve(ctx context.Context, source, name, key string, atRuntime bool) (string, error)
}

// Outputs reads the outputs of other apps' stages.
type Outputs interface {
	Output(ctx context.Context, app, stage, name string) (string, error)
}
