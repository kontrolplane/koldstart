// Package config loads a koldstart.pkl: it evaluates it with pkl, decodes the json pkl renders,
// fills in the runtimes and handlers its functions leave out, and checks what the schema cannot.
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
	WebSocket *WebSocket          `json:"websocket"`
}

// WebSocket is the stage's websocket api.
type WebSocket struct {
	RouteSelection string `json:"routeSelection"`
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
	EphemeralStorage       int  `json:"ephemeralStorage"`
	ReservedConcurrency    *int `json:"reservedConcurrency"`
	ProvisionedConcurrency *int `json:"provisionedConcurrency"`
	SnapStart              bool `json:"snapStart"`
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
	Tracing      bool              `json:"tracing"`
	KMSKey       Value             `json:"kmsKey"`
	Tags         map[string]string `json:"tags"`
	DeadLetter   Value             `json:"deadLetter"`
	Async        *Async            `json:"async"`
	FileSystem   *FileSystem       `json:"fileSystem"`
	// LogFormat is text or json; the levels apply to json alone.
	LogFormat           string `json:"logFormat"`
	ApplicationLogLevel string `json:"applicationLogLevel"`
	SystemLogLevel      string `json:"systemLogLevel"`
}

// The formats a function's logs can have.
const (
	LogFormatText = "text"
	LogFormatJSON = "json"
)

// Async is what lambda does with the events it is handed without waiting.
type Async struct {
	MaximumRetryAttempts *int `json:"maximumRetryAttempts"`
	// MaximumEventAge is in seconds.
	MaximumEventAge *int  `json:"maximumEventAge"`
	OnSuccess       Value `json:"onSuccess"`
	OnFailure       Value `json:"onFailure"`
}

// FileSystem is an efs access point a function mounts.
type FileSystem struct {
	AccessPoint Value  `json:"accessPoint"`
	MountPath   string `json:"mountPath"`
}

// IsSet reports whether the value was given: a string that is not empty, or one of koldstart's.
func (v Value) IsSet() bool {
	return v.Literal != "" || v.Ref != nil || v.Secret != nil || v.Output != nil || v.Join != nil || v.Pseudo != ""
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

// Alias is the alias a function with versions has, which its events invoke.
const Alias = "live"

// Versioned reports whether the function gets a version on every change and an alias pointed at
// it: when it keeps instances started, or starts from snapshots.
func (f Function) Versioned() bool {
	return f.ProvisionedConcurrency != nil || f.SnapStart
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
	AccessLogs  *AccessLogs           `json:"accessLogs"`
}

// AccessLogs are the access logs of the http api.
type AccessLogs struct {
	Format string `json:"format"`
	// Retention is in days.
	Retention int `json:"retention"`
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

// OutputRef is an output of another app's stage, from its state: in the account and region of
// the stage that reads it, or in Region and Account, read with the credentials of Role.
type OutputRef struct {
	App     string `json:"app"`
	Stage   string `json:"stage"`
	Output  string `json:"output"`
	Region  string `json:"region,omitempty"`
	Account string `json:"account,omitempty"`
	Role    string `json:"role,omitempty"`
}

// Elsewhere reports whether the output is of a stage in another region or account.
func (o OutputRef) Elsewhere(region string) bool {
	return (o.Region != "" && o.Region != region) || o.Account != "" || o.Role != ""
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
	EventRule      = "eventRule"
	EventWebSocket = "webSocketRoute"
	EventALB       = "loadBalancer"
	EventLogs      = "logSubscription"
)

// Event invokes a function. Type says which of the fields apply.
type Event struct {
	Type string `json:"type"`

	// httpRoute
	Method     string   `json:"method"`
	Path       string   `json:"path"`
	Authorizer string   `json:"authorizer"`
	Scopes     []string `json:"scopes"`
	IAM        bool     `json:"iam"`

	// schedule
	Expression string `json:"expression"`
	Timezone   string `json:"timezone"`
	Input      string `json:"input"`
	// Enabled is of a schedule, a rule, or a mapping of a queue or stream.
	Enabled *bool `json:"enabled"`

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

	// queue
	MaximumConcurrency *int `json:"maximumConcurrency"`

	// stream
	StartingPosition           string `json:"startingPosition"`
	MaximumRetryAttempts       *int   `json:"maximumRetryAttempts"`
	BisectBatchOnFunctionError bool   `json:"bisectBatchOnFunctionError"`
	ParallelizationFactor      int    `json:"parallelizationFactor"`
	OnFailure                  Value  `json:"onFailure"`
	// MaximumRecordAge and TumblingWindow are in seconds.
	MaximumRecordAge *int `json:"maximumRecordAge"`
	TumblingWindow   int  `json:"tumblingWindow"`

	// topic
	FilterPolicy string `json:"filterPolicy"`

	// bucket
	Events []string `json:"events"`
	Prefix string   `json:"prefix"`
	Suffix string   `json:"suffix"`

	// httpRoute
	Throttling *Throttling `json:"throttling"`

	// eventRule: the pattern, an object or a json string, and the bus
	Pattern          json.RawMessage   `json:"pattern"`
	Bus              Value             `json:"bus"`
	InputPath        string            `json:"inputPath"`
	InputTransformer *InputTransformer `json:"inputTransformer"`
	DeadLetter       Value             `json:"deadLetter"`
	// MaximumEventAge is in seconds. A rule's retries are MaximumRetryAttempts, as a stream's are.
	MaximumEventAge *int `json:"maximumEventAge"`

	// webSocketRoute
	Route string `json:"route"`

	// loadBalancer
	Listener          Value    `json:"listener"`
	Priority          int      `json:"priority"`
	Paths             []string `json:"paths"`
	Hosts             []string `json:"hosts"`
	Methods           []string `json:"methods"`
	MultiValueHeaders bool     `json:"multiValueHeaders"`
	// Headers, Query and SourceIPs are conditions of the rule, with the paths, hosts and methods.
	Headers   map[string][]string `json:"headers"`
	Query     map[string]string   `json:"query"`
	SourceIPs []string            `json:"sourceIps"`

	// logSubscription
	LogGroup Value  `json:"logGroup"`
	Filter   string `json:"filter"`
}

// InputTransformer is a template eventbridge fills in with parts of an event.
type InputTransformer struct {
	Paths    map[string]string `json:"paths"`
	Template string            `json:"template"`
}

// PatternJSON is an event rule's pattern as the json string eventbridge takes, from an object or
// a string of json.
func (e Event) PatternJSON() (string, error) {
	var s string
	if err := json.Unmarshal(e.Pattern, &s); err == nil {
		return s, nil
	}
	var v any
	if err := json.Unmarshal(e.Pattern, &v); err != nil {
		return "", err
	}
	data, err := json.Marshal(v)
	return string(data), err
}
