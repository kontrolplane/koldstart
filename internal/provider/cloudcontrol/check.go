package cloudcontrol

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Problem is a property of a resource that the schema of its type does not allow, by its path
// under properties, as KeySchema[0].KeyType.
type Problem struct {
	Path    string
	Message string
}

func (p Problem) String() string {
	if p.Path == "" {
		return p.Message
	}
	return p.Path + ": " + p.Message
}

// maxProblems bounds what one resource reports, as one mistake can make many.
const maxProblems = 20

// marker is the key of koldstart's own values, a ref, a secret or an output, whose value is only
// known while applying, so the schema cannot check it.
const marker = "$koldstart"

// Check checks the properties of a resource against the schema of its type, as cloud control
// would when the resource is created: properties the type does not have, values of the wrong
// type or outside what the schema allows, required properties that are not set, and properties
// aws sets itself.
func (s *Schema) Check(properties map[string]any) []Problem {
	if s.doc == nil {
		return nil
	}
	c := &checker{defs: s.definitions, root: s.doc}
	c.object("", "", s.doc, properties)
	for _, name := range slices.Sorted(maps.Keys(properties)) {
		if slices.Contains(s.ReadOnly, name) {
			c.add(name, "aws sets %s itself: leave it out, and read it with kdst.ref()", name)
		}
	}
	slices.SortStableFunc(c.problems, func(a, b Problem) int { return strings.Compare(a.Path, b.Path) })
	if len(c.problems) > maxProblems {
		c.problems = append(c.problems[:maxProblems], Problem{Message: fmt.Sprintf("and %d more", len(c.problems)-maxProblems)})
	}
	return c.problems
}

type checker struct {
	defs     map[string]any
	root     map[string]any
	problems []Problem
}

func (c *checker) add(path, format string, args ...any) {
	c.problems = append(c.problems, Problem{Path: path, Message: fmt.Sprintf(format, args...)})
}

// value checks v against schema at path.
func (c *checker) value(path string, schema map[string]any, v any) {
	if schema = c.resolve(schema); schema == nil || isKoldstart(v) {
		return
	}
	if all, ok := schema["allOf"].([]any); ok {
		for _, s := range all {
			if m, ok := s.(map[string]any); ok {
				c.value(path, m, v)
			}
		}
	}
	for _, key := range []string{"oneOf", "anyOf"} {
		if forms, ok := schema[key].([]any); ok {
			c.forms(path, forms, v)
		}
	}
	types := schemaTypes(schema)
	if len(types) > 0 && !slices.ContainsFunc(types, func(t string) bool { return hasType(v, t) }) {
		c.add(path, "want %s, not %s", strings.Join(types, " or "), describe(v))
		return
	}
	if enum, ok := schema["enum"].([]any); ok && !slices.ContainsFunc(enum, func(e any) bool { return equal(e, v) }) {
		c.add(path, "want one of %s, not %s", listEnum(enum), describe(v))
	}
	switch x := v.(type) {
	case map[string]any:
		c.object(path, path, schema, x)
	case []any:
		c.array(path, schema, x)
	case string:
		c.text(path, schema, x)
	case float64:
		c.number(path, schema, x)
	}
}

// forms checks v against the forms of a oneOf or anyOf. When it matches none, it reports the
// problems of the form it comes closest to: one of its type, with the fewest problems.
func (c *checker) forms(path string, forms []any, v any) {
	var best []Problem
	bestFits := false
	for _, f := range forms {
		m, ok := f.(map[string]any)
		if !ok {
			continue
		}
		sub := &checker{defs: c.defs, root: c.root}
		sub.value(path, m, v)
		if len(sub.problems) == 0 {
			return
		}
		types := schemaTypes(c.resolve(m))
		fits := len(types) == 0 || slices.ContainsFunc(types, func(t string) bool { return hasType(v, t) })
		if best == nil || (fits && !bestFits) || (fits == bestFits && len(sub.problems) < len(best)) {
			best, bestFits = sub.problems, fits
		}
	}
	c.problems = append(c.problems, best...)
}

// object checks the properties of an object: each against its schema, the ones the schema
// does not have, and the ones it requires.
func (c *checker) object(path, prefix string, schema map[string]any, v map[string]any) {
	props, _ := schema["properties"].(map[string]any)
	patterns, _ := schema["patternProperties"].(map[string]any)
	additional := schema["additionalProperties"]
	for _, name := range slices.Sorted(maps.Keys(v)) {
		at := join(prefix, name)
		if s, ok := props[name].(map[string]any); ok {
			c.value(at, s, v[name])
			continue
		}
		if s := matchPattern(patterns, name); s != nil {
			c.value(at, s, v[name])
			continue
		}
		switch a := additional.(type) {
		case bool:
			if !a && (len(props) > 0 || len(patterns) > 0) {
				c.add(at, "%s", unknown(name, slices.Collect(maps.Keys(props))))
			}
		case map[string]any:
			c.value(at, a, v[name])
		}
	}
	required, _ := schema["required"].([]any)
	for _, r := range required {
		if name, ok := r.(string); ok {
			if _, set := v[name]; !set {
				c.add(join(prefix, name), "required, and not set")
			}
		}
	}
	if n, ok := schema["minProperties"].(float64); ok && float64(len(v)) < n {
		c.add(path, "want at least %s, not %d", plural(int(n), "property"), len(v))
	}
	if n, ok := schema["maxProperties"].(float64); ok && float64(len(v)) > n {
		c.add(path, "want at most %s, not %d", plural(int(n), "property"), len(v))
	}
}

func (c *checker) array(path string, schema map[string]any, v []any) {
	if n, ok := schema["minItems"].(float64); ok && float64(len(v)) < n {
		c.add(path, "want at least %s, not %d", plural(int(n), "item"), len(v))
	}
	if n, ok := schema["maxItems"].(float64); ok && float64(len(v)) > n {
		c.add(path, "want at most %s, not %d", plural(int(n), "item"), len(v))
	}
	items, ok := schema["items"].(map[string]any)
	if !ok {
		return
	}
	for i, item := range v {
		c.value(path+"["+strconv.Itoa(i)+"]", items, item)
	}
}

func (c *checker) text(path string, schema map[string]any, v string) {
	n := len([]rune(v))
	if min, ok := schema["minLength"].(float64); ok && float64(n) < min {
		c.add(path, "want at least %s, not %d", plural(int(min), "character"), n)
	}
	if max, ok := schema["maxLength"].(float64); ok && float64(n) > max {
		c.add(path, "want at most %s, not %d", plural(int(max), "character"), n)
	}
	if p, ok := schema["pattern"].(string); ok {
		if re := compile(p); re != nil && !re.MatchString(v) {
			c.add(path, "%q does not match the pattern %s", v, p)
		}
	}
}

func (c *checker) number(path string, schema map[string]any, v float64) {
	if min, ok := schema["minimum"].(float64); ok && v < min {
		c.add(path, "want at least %v, not %v", min, v)
	}
	if max, ok := schema["maximum"].(float64); ok && v > max {
		c.add(path, "want at most %v, not %v", max, v)
	}
}

// resolve follows a $ref to the definition or property it names.
func (c *checker) resolve(schema map[string]any) map[string]any {
	for range 32 {
		ref, ok := schema["$ref"].(string)
		if !ok {
			return schema
		}
		var target any
		switch {
		case strings.HasPrefix(ref, "#/definitions/"):
			target = c.defs[strings.TrimPrefix(ref, "#/definitions/")]
		case strings.HasPrefix(ref, "#/properties/"):
			props, _ := c.root["properties"].(map[string]any)
			target = props[strings.TrimPrefix(ref, "#/properties/")]
		}
		next, ok := target.(map[string]any)
		if !ok {
			return nil
		}
		schema = next
	}
	return nil
}

func schemaTypes(schema map[string]any) []string {
	switch t := schema["type"].(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// hasType reports whether v is of a json schema type. An integer is a number without a fraction,
// as pkl writes a whole number.
func hasType(v any, t string) bool {
	switch x := v.(type) {
	case nil:
		return t == "null"
	case bool:
		return t == "boolean"
	case string:
		return t == "string"
	case float64:
		return t == "number" || (t == "integer" && x == math.Trunc(x))
	case []any:
		return t == "array"
	case map[string]any:
		return t == "object"
	}
	return false
}

// describe says what kind of value v is, for a problem.
func describe(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(x)
	case string:
		return strconv.Quote(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case []any:
		return "a list"
	case map[string]any:
		return "an object"
	}
	return fmt.Sprintf("%T", v)
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	if stem, ok := strings.CutSuffix(noun, "y"); ok {
		return strconv.Itoa(n) + " " + stem + "ies"
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

func listEnum(enum []any) string {
	parts := make([]string, 0, len(enum))
	for _, e := range enum {
		parts = append(parts, describe(e))
	}
	return strings.Join(parts, ", ")
}

func equal(a, b any) bool {
	if reflect.DeepEqual(a, b) {
		return true
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func isKoldstart(v any) bool {
	m, ok := v.(map[string]any)
	_, has := m[marker]
	return ok && has
}

func join(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// unknown says a property is not one of the type's, and which it may have meant.
func unknown(name string, names []string) string {
	slices.Sort(names)
	best, bestDistance := "", 3
	for _, n := range names {
		if strings.EqualFold(n, name) {
			best = n
			break
		}
		if d := distance(strings.ToLower(n), strings.ToLower(name)); d < bestDistance {
			best, bestDistance = n, d
		}
	}
	if best != "" {
		return "no such property: did you mean " + best + "?"
	}
	return "no such property"
}

// distance is the edit distance of two strings.
func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

func matchPattern(patterns map[string]any, name string) map[string]any {
	for _, p := range slices.Sorted(maps.Keys(patterns)) {
		if re := compile(p); re != nil && re.MatchString(name) {
			s, _ := patterns[p].(map[string]any)
			return s
		}
	}
	return nil
}

// patterns caches the compiled patterns of the schemas. A pattern go cannot compile, such as
// one with a lookahead, is not checked.
var patterns sync.Map

func compile(p string) *regexp.Regexp {
	if re, ok := patterns.Load(p); ok {
		return re.(*regexp.Regexp)
	}
	re, err := regexp.Compile(p)
	if err != nil {
		re = nil
	}
	patterns.Store(p, re)
	return re
}
