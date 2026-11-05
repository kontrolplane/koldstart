// Package config loads a koldstart.pkl: it evaluates it with pkl, decodes the json pkl renders,
// and checks what the schema cannot.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Project is a koldstart.pkl, evaluated for one stage.
type Project struct {
	App       string              `json:"app"`
	Region    string              `json:"region"`
	Stage     string              `json:"stage"`
	Functions map[string]Function `json:"functions"`
	Resources map[string]Resource `json:"resources"`
	Outputs   map[string]Value    `json:"outputs"`
	Tags      map[string]string   `json:"tags"`
	HTTP      *HTTP               `json:"http"`
}

// The runtimes a function can have.
const (
	RuntimeNode22 = "nodejs22.x"
	RuntimeNode24 = "nodejs24.x"
	RuntimeGo     = "provided.al2023"
	RuntimePy312  = "python3.12"
	RuntimePy313  = "python3.13"
)

// The architectures a function can have.
const (
	ArchARM64 = "arm64"
	ArchX8664 = "x86_64"
)

// Function is one lambda function.
type Function struct {
	Runtime string `json:"runtime"`
	Handler string `json:"handler"`
	Memory  int    `json:"memory"`
	// Timeout is in seconds.
	Timeout      int    `json:"timeout"`
	Architecture string `json:"architecture"`
	// EphemeralStorage is in megabytes.
	EphemeralStorage    int  `json:"ephemeralStorage"`
	ReservedConcurrency *int `json:"reservedConcurrency"`
	// LogRetention is in days.
	LogRetention int               `json:"logRetention"`
	Environment  map[string]Value  `json:"environment"`
	Events       []Event           `json:"events"`
	URL          *FunctionURL      `json:"url"`
	Permissions  []Statement       `json:"permissions"`
	Link         map[string]string `json:"link"`
	VPC          *VPC              `json:"vpc"`
	Bundle       Bundle            `json:"bundle"`
	Typecheck    bool              `json:"typecheck"`
	Image        *Image            `json:"image"`
	Layers       []Value           `json:"layers"`
}

// Image is a container image a function runs instead of a runtime.
type Image struct {
	Dockerfile       string            `json:"dockerfile"`
	Context          string            `json:"context"`
	BuildArgs        map[string]string `json:"buildArgs"`
	Command          []string          `json:"command"`
	EntryPoint       []string          `json:"entryPoint"`
	WorkingDirectory string            `json:"workingDirectory"`
}

// IsPython reports whether the function runs on a python runtime.
func (f Function) IsPython() bool {
	return f.Runtime == RuntimePy312 || f.Runtime == RuntimePy313
}

// IsImage reports whether the function is a container image.
func (f Function) IsImage() bool {
	return f.Image != nil
}

// IsNode reports whether the function runs on a node runtime.
func (f Function) IsNode() bool {
	return f.Runtime == RuntimeNode22 || f.Runtime == RuntimeNode24
}

// IsGo reports whether the function is a go binary on the os-only runtime.
func (f Function) IsGo() bool {
	return f.Runtime == RuntimeGo
}

// FunctionURL is the function's own url.
type FunctionURL struct {
	// Auth is iam or none.
	Auth string `json:"auth"`
	CORS *CORS  `json:"cors"`
}

// CORS is the cross-origin configuration of a function url or the http api.
type CORS struct {
	Origins     []string `json:"origins"`
	Methods     []string `json:"methods"`
	Headers     []string `json:"headers"`
	Expose      []string `json:"expose"`
	Credentials bool     `json:"credentials"`
	// MaxAge is in seconds.
	MaxAge int `json:"maxAge"`
}

// Statement is an iam policy statement added to the function's role.
type Statement struct {
	Effect    string   `json:"effect"`
	Actions   []string `json:"actions"`
	Resources []Value  `json:"resources"`
}

// VPC puts the function in subnets.
type VPC struct {
	SubnetIDs        []Value `json:"subnetIds"`
	SecurityGroupIDs []Value `json:"securityGroupIds"`
}

// Bundle is how a node function is bundled.
type Bundle struct {
	External  []string `json:"externals"`
	Sourcemap bool     `json:"sourcemap"`
	Include   []string `json:"include"`
}

// HTTP is the stage's http api.
type HTTP struct {
	Domain      string                `json:"domain"`
	Certificate Value                 `json:"certificate"`
	CORS        *CORS                 `json:"cors"`
	Authorizers map[string]Authorizer `json:"authorizers"`
	Throttling  *Throttling           `json:"throttling"`
}

// Authorizer guards http routes: a jwt issuer, or a lambda function of the project.
type Authorizer struct {
	// Type is jwt or lambda.
	Type     string   `json:"type"`
	Issuer   string   `json:"issuer"`
	Audience []string `json:"audience"`
	Function string   `json:"functionName"`
	Identity []string `json:"identity"`
	// TTL is in seconds.
	TTL int `json:"ttl"`
}

// Throttling is the stage-wide request limit of the http api.
type Throttling struct {
	Rate  float64 `json:"rate"`
	Burst int     `json:"burst"`
}

// Resource is anything else in aws, created through cloud control.
type Resource struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
	// DeletionPolicy is retain, delete, or empty for the type's default.
	DeletionPolicy string `json:"deletionPolicy"`
}

// marker is the key pkl renders on koldstart's own values, such as a ref, to tell them from a
// string or an object of resource properties.
const marker = "$koldstart"

// The kinds of value under marker.
const (
	kindRef    = "ref"
	kindSecret = "secret"
	kindOutput = "output"
	kindJoin   = "join"
	kindPseudo = "pseudo"
)

// Value is a string in the configuration: given as is, or known only while deploying.
type Value struct {
	Literal string
	Ref     *Ref
	Secret  *Secret
	Output  *OutputRef
	// Join are parts joined into one string once the refs among them are known.
	Join []Value
	// Pseudo is a value of the account and region the stage deploys to: accountId, partition or
	// urlSuffix.
	Pseudo string
}

// The values of the account and region a Pseudo stands for.
const (
	PseudoAccountID = "accountId"
	PseudoPartition = "partition"
	PseudoURLSuffix = "urlSuffix"
)

// Ref is an attribute of a resource or function of the project, once it exists.
type Ref struct {
	Resource  string `json:"resource"`
	Attribute string `json:"attribute"`
}

// Secret is read from ssm parameter store or secrets manager.
type Secret struct {
	// Source is ssm or secretsmanager.
	Source    string `json:"source"`
	Name      string `json:"name"`
	Key       string `json:"key"`
	AtRuntime bool   `json:"atRuntime"`
}

// The sources of a secret.
const (
	SourceSSM            = "ssm"
	SourceSecretsManager = "secretsmanager"
)

// OutputRef is an output of another app's stage, from its state.
type OutputRef struct {
	App    string `json:"app"`
	Stage  string `json:"stage"`
	Output string `json:"output"`
}

// UnmarshalJSON reads a plain string, or one of koldstart's values.
func (v *Value) UnmarshalJSON(data []byte) error {
	*v = Value{}
	if bytes.Equal(data, []byte("null")) {
		return nil
	}
	if len(data) > 0 && data[0] == '"' {
		return json.Unmarshal(data, &v.Literal)
	}
	var kind struct {
		Kind string `json:"$koldstart"`
	}
	if err := json.Unmarshal(data, &kind); err != nil {
		return fmt.Errorf("want a string, ref(), a secret or output(): %w", err)
	}
	switch kind.Kind {
	case kindRef:
		v.Ref = &Ref{}
		return json.Unmarshal(data, v.Ref)
	case kindSecret:
		v.Secret = &Secret{}
		return json.Unmarshal(data, v.Secret)
	case kindOutput:
		v.Output = &OutputRef{}
		return json.Unmarshal(data, v.Output)
	case kindJoin:
		var j struct {
			Parts []Value `json:"parts"`
		}
		if err := json.Unmarshal(data, &j); err != nil {
			return err
		}
		v.Join = j.Parts
		return nil
	case kindPseudo:
		var ps struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(data, &ps); err != nil {
			return err
		}
		v.Pseudo = ps.Name
		return nil
	default:
		return fmt.Errorf("want a string, ref(), a secret or output(), not %s", data)
	}
}

// MarshalJSON writes the value back the way pkl renders it.
func (v Value) MarshalJSON() ([]byte, error) {
	switch {
	case v.Ref != nil:
		return json.Marshal(struct {
			Kind string `json:"$koldstart"`
			Ref
		}{kindRef, *v.Ref})
	case v.Secret != nil:
		return json.Marshal(struct {
			Kind string `json:"$koldstart"`
			Secret
		}{kindSecret, *v.Secret})
	case v.Output != nil:
		return json.Marshal(struct {
			Kind string `json:"$koldstart"`
			OutputRef
		}{kindOutput, *v.Output})
	case v.Join != nil:
		return json.Marshal(struct {
			Kind  string  `json:"$koldstart"`
			Parts []Value `json:"parts"`
		}{kindJoin, v.Join})
	case v.Pseudo != "":
		return json.Marshal(struct {
			Kind string `json:"$koldstart"`
			Name string `json:"name"`
		}{kindPseudo, v.Pseudo})
	default:
		return json.Marshal(v.Literal)
	}
}

// AsValue returns the value a json value inside resource properties stands for when it is one of
// koldstart's own: a ref, a join or a pseudo value. It is false for anything else.
func AsValue(value any) (Value, bool) {
	m, ok := value.(map[string]any)
	if !ok {
		return Value{}, false
	}
	switch m[marker] {
	case kindRef, kindJoin, kindPseudo:
	default:
		return Value{}, false
	}
	data, err := json.Marshal(m)
	if err != nil {
		return Value{}, false
	}
	var v Value
	if err := json.Unmarshal(data, &v); err != nil {
		return Value{}, false
	}
	return v, true
}

// AsRef returns the ref an arbitrary json value stands for, such as one inside resource
// properties, or false when it is not one.
func AsRef(value any) (Ref, bool) {
	m, ok := value.(map[string]any)
	if !ok || m[marker] != kindRef {
		return Ref{}, false
	}
	resource, _ := m["resource"].(string)
	attribute, _ := m["attribute"].(string)
	return Ref{Resource: resource, Attribute: attribute}, true
}

// The types of event.
const (
	EventHTTPRoute = "httpRoute"
	EventSchedule  = "schedule"
	EventQueue     = "queue"
	EventTopic     = "topic"
	EventStream    = "stream"
	EventBucket    = "bucket"
)

// Event invokes a function. Type says which of the fields apply.
type Event struct {
	Type string `json:"type"`

	// httpRoute
	Method     string   `json:"method"`
	Path       string   `json:"path"`
	Authorizer string   `json:"authorizer"`
	Scopes     []string `json:"scopes"`

	// schedule
	Expression string `json:"expression"`
	Timezone   string `json:"timezone"`
	Input      string `json:"input"`
	Enabled    *bool  `json:"enabled"`

	// queue, topic, stream, bucket: the source
	Queue  Value `json:"queue"`
	Topic  Value `json:"topic"`
	Stream Value `json:"stream"`
	Bucket Value `json:"bucket"`

	// queue and stream
	BatchSize int `json:"batchSize"`
	// MaximumBatchingWindow is in seconds.
	MaximumBatchingWindow   int      `json:"maximumBatchingWindow"`
	ReportBatchItemFailures bool     `json:"reportBatchItemFailures"`
	Filters                 []string `json:"filters"`

	// stream
	StartingPosition string `json:"startingPosition"`

	// topic
	FilterPolicy string `json:"filterPolicy"`

	// bucket
	Events []string `json:"events"`
	Prefix string   `json:"prefix"`
	Suffix string   `json:"suffix"`
}
