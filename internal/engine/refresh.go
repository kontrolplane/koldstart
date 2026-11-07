package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/kontrolplane/koldstart/internal/state"
)

// Differ is a provider that reads a resource as it is in aws, and says which of the inputs it
// was applied with it no longer has. A provider without it only says whether the resource is
// still there.
type Differ interface {
	Drift(ctx context.Context, req Request) ([]string, error)
}

// refreshed is what reading a resource as it is found.
type refreshed struct {
	gone  bool
	drift []string
}

// refresh reads the resource of a node as it is now: gone, when something outside koldstart
// deleted it, or with the inputs that were changed outside koldstart since it was applied. A
// resource with tags that are not koldstart's, as an imported one has, is still there.
func (e *Engine) refresh(ctx context.Context, p Provider, n Node, st *state.State, prior *state.Resource) (refreshed, error) {
	applied, err := e.priorWithExternal(ctx, prior, prior.SecretsHash)
	if err != nil {
		return refreshed{}, err
	}
	req := Request{
		ID: n.ID, Name: n.Name, PhysicalID: prior.PhysicalID, Inputs: applied, Prior: applied,
		Attributes: prior.Attributes, Tags: e.tags(n.ID, st.App, st.Stage),
	}
	_, found, err := p.Read(ctx, req)
	switch {
	case errors.Is(err, ErrNotOurs):
	case err != nil:
		return refreshed{}, fmt.Errorf("reading it as it is: %w", err)
	case !found:
		return refreshed{gone: true}, nil
	}
	d, ok := p.(Differ)
	if !ok {
		return refreshed{}, nil
	}
	drift, err := d.Drift(ctx, req)
	if err != nil {
		return refreshed{}, fmt.Errorf("reading it as it is: %w", err)
	}
	return refreshed{drift: drift}, nil
}

// fromLive changes the request of a node that was deleted or changed outside koldstart: one that
// is gone is created anew, and one that changed is updated from how it is, not from the inputs it
// was last applied with, so what changed is set back.
func fromLive(req Request, c Change) Request {
	if c.Gone {
		req.PhysicalID, req.Prior, req.Attributes = "", nil, nil
	}
	if len(c.Drift) > 0 {
		req.Prior = nil
	}
	return req
}
