// Package cloudcontrol is the provider of everything in resources: any AWS:: type cloud control
// supports, created, updated and deleted through its generic api, without a stack.
package cloudcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"

	"github.com/kontrolplane/koldstart/internal/awserr"
	"github.com/kontrolplane/koldstart/internal/engine"
)

// API is the part of the cloud control client the provider uses.
type API interface {
	CreateResource(context.Context, *cloudcontrol.CreateResourceInput, ...func(*cloudcontrol.Options)) (*cloudcontrol.CreateResourceOutput, error)
	GetResource(context.Context, *cloudcontrol.GetResourceInput, ...func(*cloudcontrol.Options)) (*cloudcontrol.GetResourceOutput, error)
	UpdateResource(context.Context, *cloudcontrol.UpdateResourceInput, ...func(*cloudcontrol.Options)) (*cloudcontrol.UpdateResourceOutput, error)
	DeleteResource(context.Context, *cloudcontrol.DeleteResourceInput, ...func(*cloudcontrol.Options)) (*cloudcontrol.DeleteResourceOutput, error)
	GetResourceRequestStatus(context.Context, *cloudcontrol.GetResourceRequestStatusInput, ...func(*cloudcontrol.Options)) (*cloudcontrol.GetResourceRequestStatusOutput, error)
}

// Inputs are the inputs of a resource node.
type Inputs struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
}

// Provider creates, updates and deletes resources through cloud control.
type Provider struct {
	Client API
	// Schemas say which properties need a new resource to change, and which aws sets.
	Schemas Schemas
	// Poll is the longest wait between two looks at a request; Timeout is how long one may take.
	Poll    time.Duration
	Timeout time.Duration
}

func (p Provider) poll() time.Duration {
	if p.Poll == 0 {
		return 10 * time.Second
	}
	return p.Poll
}

func (p Provider) timeout() time.Duration {
	if p.Timeout == 0 {
		return time.Hour
	}
	return p.Timeout
}

func isNotFound(err error) bool {
	return awserr.Is(err, "ResourceNotFoundException")
}

// ExistsError is a create of a resource that exists already, which koldstart did not create.
// The cli says how to take it over.
type ExistsError struct {
	Type string
	Node string
}

func (e *ExistsError) Error() string {
	return fmt.Sprintf("a %s with these properties exists already, and koldstart did not create it", e.Type)
}

// requestError is a request cloud control finished without success.
type requestError struct {
	Code    types.HandlerErrorCode
	Message string
}

func (e *requestError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// wait looks at a request until it is done, more slowly as it takes longer, and returns the
// identifier of its resource.
func (p Provider) wait(ctx context.Context, token string) (string, error) {
	deadline := time.Now().Add(p.timeout())
	delay := min(500*time.Millisecond, p.poll())
	for {
		out, err := p.Client.GetResourceRequestStatus(ctx, &cloudcontrol.GetResourceRequestStatusInput{RequestToken: &token})
		if err != nil {
			return "", fmt.Errorf("reading request %s: %w", token, err)
		}
		e := out.ProgressEvent
		switch e.OperationStatus {
		case types.OperationStatusSuccess:
			return aws.ToString(e.Identifier), nil
		case types.OperationStatusFailed, types.OperationStatusCancelComplete:
			return aws.ToString(e.Identifier), &requestError{Code: e.ErrorCode, Message: aws.ToString(e.StatusMessage)}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("request %s is still %s after %s: deploy again to keep waiting", token, e.OperationStatus, p.timeout())
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, p.poll())
	}
}

// status returns how a request ended, or "" while it runs.
func (p Provider) status(ctx context.Context, token string) (types.OperationStatus, error) {
	out, err := p.Client.GetResourceRequestStatus(ctx, &cloudcontrol.GetResourceRequestStatusInput{RequestToken: &token})
	if err != nil {
		return "", fmt.Errorf("reading request %s: %w", token, err)
	}
	return out.ProgressEvent.OperationStatus, nil
}

func decode(data json.RawMessage) (Inputs, error) {
	var in Inputs
	if len(data) == 0 {
		return in, nil
	}
	if err := json.Unmarshal(data, &in); err != nil {
		return in, fmt.Errorf("reading the inputs: %w", err)
	}
	if in.Properties == nil {
		in.Properties = map[string]any{}
	}
	return in, nil
}

// Create implements engine.Provider. The client token is the same for every try of one create,
// so a deploy that died after cloud control took the request makes no second resource. Only when
// the earlier try is known to have failed does a new token start over.
func (p Provider) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	in, err := decode(req.Inputs)
	if err != nil {
		return engine.Result{}, err
	}
	desired, err := json.Marshal(in.Properties)
	if err != nil {
		return engine.Result{}, err
	}
	token := req.ClientToken
	if req.Token != "" {
		status, err := p.status(ctx, req.Token)
		if err != nil {
			return engine.Result{}, err
		}
		if status == types.OperationStatusFailed || status == types.OperationStatusCancelComplete {
			token += "-" + req.Token[:min(8, len(req.Token))]
		}
	}
	out, err := p.Client.CreateResource(ctx, &cloudcontrol.CreateResourceInput{
		TypeName: &in.Type, DesiredState: aws.String(string(desired)), ClientToken: optional(token),
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating a %s: %w", in.Type, err)
	}
	request := aws.ToString(out.ProgressEvent.RequestToken)
	if req.Progress != nil {
		if err := req.Progress(request); err != nil {
			return engine.Result{}, err
		}
	}
	id, err := p.wait(ctx, request)
	if re, ok := errors.AsType[*requestError](err); ok && re.Code == types.HandlerErrorCodeAlreadyExists {
		return engine.Result{}, &ExistsError{Type: in.Type, Node: req.ID}
	}
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating a %s: %w", in.Type, err)
	}
	return p.get(ctx, in.Type, id)
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// get reads a resource, and makes its properties the attributes ref() reads.
func (p Provider) get(ctx context.Context, typ, id string) (engine.Result, error) {
	out, err := p.Client.GetResource(ctx, &cloudcontrol.GetResourceInput{TypeName: &typ, Identifier: &id})
	if err != nil {
		return engine.Result{}, fmt.Errorf("reading the %s %s: %w", typ, id, err)
	}
	var props map[string]any
	if err := json.Unmarshal([]byte(aws.ToString(out.ResourceDescription.Properties)), &props); err != nil {
		return engine.Result{}, fmt.Errorf("reading the properties of the %s %s: %w", typ, id, err)
	}
	return engine.Result{PhysicalID: id, Attributes: Attributes(id, props)}, nil
}

// Attributes are what ref() reads of a resource: each top-level property, objects and lists as
// json, and its identifier.
func Attributes(id string, props map[string]any) map[string]string {
	out := map[string]string{"Identifier": id}
	for k, v := range props {
		switch x := v.(type) {
		case string:
			out[k] = x
		case float64:
			out[k] = strconv.FormatFloat(x, 'f', -1, 64)
		case bool:
			out[k] = strconv.FormatBool(x)
		case nil:
		default:
			data, _ := json.Marshal(x)
			out[k] = string(data)
		}
	}
	return out
}

// Read implements engine.Provider: by identifier, or by the request a crashed deploy recorded,
// waited out when it still runs.
func (p Provider) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	in, err := decode(req.Inputs)
	if err != nil {
		return engine.Result{}, false, err
	}
	id := req.PhysicalID
	if id == "" && req.Token != "" {
		found, err := p.wait(ctx, req.Token)
		if _, failed := errors.AsType[*requestError](err); failed {
			return engine.Result{}, false, nil
		}
		if err != nil {
			return engine.Result{}, false, err
		}
		id = found
	}
	if id == "" {
		return engine.Result{}, false, nil
	}
	res, err := p.get(ctx, in.Type, id)
	if isNotFound(err) {
		return engine.Result{}, false, nil
	}
	return res, err == nil, err
}

// Update implements engine.Provider with a json patch from the properties it was last applied
// with. Taking over a resource, there are none: then the live ones are the start, and only what
// the configuration sets is patched.
func (p Provider) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	in, err := decode(req.Inputs)
	if err != nil {
		return engine.Result{}, err
	}
	var ops []Op
	if len(req.Prior) > 0 {
		prior, err := decode(req.Prior)
		if err != nil {
			return engine.Result{}, err
		}
		ops = Patch(prior.Properties, in.Properties, p.readOnly(ctx, in.Type))
	} else {
		live, err := p.live(ctx, in.Type, req.PhysicalID)
		if err != nil {
			return engine.Result{}, err
		}
		ops = Patch(live, in.Properties, nil)
		ops = slices.DeleteFunc(ops, func(o Op) bool { return o.Op == "remove" })
	}
	if len(ops) == 0 {
		return p.get(ctx, in.Type, req.PhysicalID)
	}
	patch, err := json.Marshal(ops)
	if err != nil {
		return engine.Result{}, err
	}
	out, err := p.Client.UpdateResource(ctx, &cloudcontrol.UpdateResourceInput{
		TypeName: &in.Type, Identifier: &req.PhysicalID, PatchDocument: aws.String(string(patch)), ClientToken: optional(req.ClientToken),
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("updating the %s %s: %w", in.Type, req.PhysicalID, err)
	}
	request := aws.ToString(out.ProgressEvent.RequestToken)
	if req.Progress != nil {
		if err := req.Progress(request); err != nil {
			return engine.Result{}, err
		}
	}
	if _, err := p.wait(ctx, request); err != nil {
		return engine.Result{}, fmt.Errorf("updating the %s %s: %w", in.Type, req.PhysicalID, err)
	}
	return p.get(ctx, in.Type, req.PhysicalID)
}

func (p Provider) live(ctx context.Context, typ, id string) (map[string]any, error) {
	out, err := p.Client.GetResource(ctx, &cloudcontrol.GetResourceInput{TypeName: &typ, Identifier: &id})
	if err != nil {
		return nil, fmt.Errorf("reading the %s %s: %w", typ, id, err)
	}
	var props map[string]any
	if err := json.Unmarshal([]byte(aws.ToString(out.ResourceDescription.Properties)), &props); err != nil {
		return nil, err
	}
	return props, nil
}

// Delete implements engine.Provider; one already gone is not an error.
func (p Provider) Delete(ctx context.Context, req engine.Request) error {
	in, err := decode(req.Prior)
	if err != nil {
		return err
	}
	out, err := p.Client.DeleteResource(ctx, &cloudcontrol.DeleteResourceInput{TypeName: &in.Type, Identifier: &req.PhysicalID})
	if isNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("deleting the %s %s: %w", in.Type, req.PhysicalID, err)
	}
	_, err = p.wait(ctx, aws.ToString(out.ProgressEvent.RequestToken))
	if re, ok := errors.AsType[*requestError](err); ok && re.Code == types.HandlerErrorCodeNotFound {
		return nil
	}
	if err != nil {
		return fmt.Errorf("deleting the %s %s: %w", in.Type, req.PhysicalID, err)
	}
	return nil
}

func (p Provider) readOnly(ctx context.Context, typ string) []string {
	if p.Schemas == nil {
		return nil
	}
	s, err := p.Schemas.Schema(ctx, typ)
	if err != nil {
		return nil
	}
	return s.ReadOnly
}

// Replaces implements engine.Provider: a change of the type, or of a property the type's schema
// says is create only. Without the schema every change is an update, which cloud control
// refuses with its own error when it is not one.
func (p Provider) Replaces(prior, desired json.RawMessage) bool {
	a, _ := decode(prior)
	b, _ := decode(desired)
	if a.Type != b.Type {
		return true
	}
	if p.Schemas == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := p.Schemas.Schema(ctx, b.Type)
	if err != nil {
		return false
	}
	for _, key := range s.CreateOnly {
		if !reflect.DeepEqual(a.Properties[key], b.Properties[key]) {
			return true
		}
	}
	return false
}

// Op is one operation of a json patch.
type Op struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

// Patch returns the json patch that turns before into after at the top level, leaving the
// properties aws sets alone.
func Patch(before, after map[string]any, readOnly []string) []Op {
	var ops []Op
	for _, k := range slices.Sorted(maps.Keys(after)) {
		if slices.Contains(readOnly, k) {
			continue
		}
		old, had := before[k]
		switch {
		case !had:
			ops = append(ops, Op{Op: "add", Path: "/" + escape(k), Value: after[k]})
		case !reflect.DeepEqual(old, after[k]):
			ops = append(ops, Op{Op: "replace", Path: "/" + escape(k), Value: after[k]})
		}
	}
	for _, k := range slices.Sorted(maps.Keys(before)) {
		if _, keep := after[k]; !keep && !slices.Contains(readOnly, k) {
			ops = append(ops, Op{Op: "remove", Path: "/" + escape(k)})
		}
	}
	return ops
}

// escape escapes a key for a json pointer.
func escape(k string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(k)
}
