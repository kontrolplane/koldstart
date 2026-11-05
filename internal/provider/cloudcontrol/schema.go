package cloudcontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
)

// Schema is what koldstart reads of a resource type's schema.
type Schema struct {
	// CreateOnly are the top-level properties a change of needs a new resource.
	CreateOnly []string
	// ReadOnly are the top-level properties aws sets, never patched.
	ReadOnly []string
	// WriteOnly are the top-level properties aws takes and never returns.
	WriteOnly []string
	// Provisionable is false for a type cloud control cannot create.
	Provisionable bool

	doc         map[string]any
	definitions map[string]any
}

// Schemas looks up resource type schemas.
type Schemas interface {
	Schema(ctx context.Context, typ string) (*Schema, error)
}

// DescribeTypeAPI is the part of the cloudformation client the schemas use.
type DescribeTypeAPI interface {
	DescribeType(context.Context, *cloudformation.DescribeTypeInput, ...func(*cloudformation.Options)) (*cloudformation.DescribeTypeOutput, error)
}

// Registry reads schemas from the cloudformation registry, once each.
type Registry struct {
	Client DescribeTypeAPI
	mu     sync.Mutex
	cache  map[string]*Schema
}

// Schema implements Schemas.
func (r *Registry) Schema(ctx context.Context, typ string) (*Schema, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.cache[typ]; ok {
		return s, nil
	}
	out, err := r.Client.DescribeType(ctx, &cloudformation.DescribeTypeInput{Type: cftypes.RegistryTypeResource, TypeName: &typ})
	if err != nil {
		return nil, fmt.Errorf("reading the schema of %s: %w", typ, err)
	}
	s, err := ParseSchema(aws.ToString(out.Schema))
	if err != nil {
		return nil, fmt.Errorf("reading the schema of %s: %w", typ, err)
	}
	s.Provisionable = out.ProvisioningType != cftypes.ProvisioningTypeNonProvisionable
	if r.cache == nil {
		r.cache = map[string]*Schema{}
	}
	r.cache[typ] = s
	return s, nil
}

// ParseSchema reads a schema: its create only and read only properties, by their top-level
// property, so a change anywhere under /properties/KeySchema counts as one of KeySchema, and the
// rest, for Check.
func ParseSchema(doc string) (*Schema, error) {
	var raw struct {
		CreateOnly  []string       `json:"createOnlyProperties"`
		ReadOnly    []string       `json:"readOnlyProperties"`
		WriteOnly   []string       `json:"writeOnlyProperties"`
		Definitions map[string]any `json:"definitions"`
	}
	if err := json.Unmarshal([]byte(doc), &raw); err != nil {
		return nil, err
	}
	var whole map[string]any
	if err := json.Unmarshal([]byte(doc), &whole); err != nil {
		return nil, err
	}
	return &Schema{
		CreateOnly: topLevel(raw.CreateOnly), ReadOnly: topLevel(raw.ReadOnly), WriteOnly: topLevel(raw.WriteOnly), Provisionable: true,
		doc: whole, definitions: raw.Definitions,
	}, nil
}

func topLevel(pointers []string) []string {
	var out []string
	for _, p := range pointers {
		key, _, _ := strings.Cut(strings.TrimPrefix(p, "/properties/"), "/")
		if key != "" && !slices.Contains(out, key) {
			out = append(out, key)
		}
	}
	return out
}
