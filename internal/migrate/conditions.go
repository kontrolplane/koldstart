package migrate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// convertConditions converts resources.Conditions into pkl booleans, written as locals, by the
// name a condition has. A condition that cannot be converted is left for you, and so is what
// refers to it.
func (c *converter) convertConditions() {
	c.conditions = map[string]string{}
	conditions := get(get(c.doc, "resources"), "Conditions")
	for _, name := range keys(conditions) {
		expr, todo := c.condition(get(conditions, name), 0)
		if todo != "" {
			c.todo("resources.Conditions."+name, todo)
			continue
		}
		c.conditions[name] = expr
	}
}

// conditionName is the local a condition is converted to.
func (c *converter) conditionName(name string) (string, bool) {
	if _, ok := c.conditions[name]; !ok {
		return "", false
	}
	return localName("condition", name), true
}

// localName is a name for a local: the prefix and the name, of letters and digits.
func localName(prefix, name string) string {
	clean := regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(name, "")
	if clean == "" {
		return prefix
	}
	return prefix + strings.ToUpper(clean[:1]) + clean[1:]
}

// condition converts a condition function: Fn::Equals of two values, Fn::Not, Fn::And, Fn::Or,
// and Condition, another condition.
func (c *converter) condition(n *yaml.Node, depth int) (string, string) {
	if depth > 10 {
		return "", "a condition that refers to itself"
	}
	name, arg := intrinsicOf(n)
	switch name {
	case "Fn::Equals":
		if arg.Kind != yaml.SequenceNode || len(arg.Content) != 2 {
			return "", "Fn::Equals takes two values"
		}
		a, b := c.nodeValue(arg.Content[0]), c.nodeValue(arg.Content[1])
		for _, v := range []value{a, b} {
			if !v.ok || !v.interpolable {
				return "", "a condition on a value only known while deploying: " + flow(n)
			}
		}
		return fmt.Sprintf("%s == %s", a.expr, b.expr), ""
	case "Fn::Not":
		if arg.Kind != yaml.SequenceNode || len(arg.Content) != 1 {
			return "", "Fn::Not takes one condition"
		}
		inner, todo := c.condition(arg.Content[0], depth+1)
		return "!(" + inner + ")", todo
	case "Fn::And", "Fn::Or":
		if arg.Kind != yaml.SequenceNode || len(arg.Content) < 2 {
			return "", name + " takes two conditions or more"
		}
		op := " && "
		if name == "Fn::Or" {
			op = " || "
		}
		parts := make([]string, 0, len(arg.Content))
		for _, item := range arg.Content {
			inner, todo := c.condition(item, depth+1)
			if todo != "" {
				return "", todo
			}
			parts = append(parts, "("+inner+")")
		}
		return strings.Join(parts, op), ""
	case intrinsicCondition:
		other := get(get(get(c.doc, "resources"), "Conditions"), arg.Value)
		if other == nil {
			return "", "the condition " + arg.Value + " is not in Conditions"
		}
		return c.condition(other, depth+1)
	}
	return "", "a condition that is not Fn::Equals, Fn::Not, Fn::And, Fn::Or or Condition: " + flow(n)
}

// ifMember writes a Fn::If where a property or an element goes: when (condition) { ... } with
// else when both sides are objects or lists, if (condition) a else b for two values, and only the
// side that is not AWS::NoValue when one is.
func (c *converter) ifMember(w *writer, where, head string, arg *yaml.Node) {
	cond, a, b, todo := c.ifParts(arg)
	if todo != "" {
		c.leaveOut(w, where, todo, arg)
		return
	}
	isNoValue := func(n *yaml.Node) bool { v, ok := c.intrinsic(n); return ok && v.noValue }
	switch {
	case isNoValue(a) && isNoValue(b):
		return
	case isNoValue(b):
		w.open(fmt.Sprintf("when (%s)", cond))
		c.member(w, where, head, a)
		w.close()
		return
	case isNoValue(a):
		w.open(fmt.Sprintf("when (!%s)", cond))
		c.member(w, where, head, b)
		w.close()
		return
	}
	if v := c.ifValue(arg); v.ok {
		c.assign(w, where, head, v, arg)
		return
	}
	w.open(fmt.Sprintf("when (%s)", cond))
	c.member(w, where, head, a)
	w.depth--
	w.line("} else {")
	w.depth++
	c.member(w, where, head, b)
	w.close()
}

// paramsOf returns the params of serverless.yml by stage: params, or stages.<stage>.params.
func (c *converter) paramsOf() map[string]*yaml.Node {
	out := map[string]*yaml.Node{}
	params := get(c.doc, "params")
	for _, stage := range keys(params) {
		out[stage] = get(params, stage)
	}
	stages := get(c.doc, "stages")
	for _, stage := range keys(stages) {
		if p := get(get(stages, stage), "params"); p != nil {
			out[stage] = p
		}
	}
	return out
}

// param converts ${param:name}: the param of the stage, or of default.
func (c *converter) param(name, inner string) value {
	found := false
	for _, p := range c.paramsOf() {
		if get(p, name) != nil {
			found = true
		}
	}
	if !found {
		return notConverted("${" + inner + "}: no stage has the param " + name)
	}
	c.usesParams = true
	return literal(fmt.Sprintf("param(%s)", quote(name)))
}

var fileVariable = regexp.MustCompile(`^file\(([^)]+)\)(?::(.+))?$`)

// fileNode reads what a ${file(path):key} names: the yaml or json file, or the value at key in
// it, relative to serverless.yml.
//
//nolint:gosec // reads a file serverless.yml names, next to it
func (c *converter) fileNode(primary string) (*yaml.Node, string) {
	m := fileVariable.FindStringSubmatch(primary)
	if m == nil {
		return nil, "${" + primary + "} is not a ${file(path)} or ${file(path):key}"
	}
	path := strings.Trim(strings.TrimSpace(m[1]), `'"`)
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yml", ".yaml", ".json":
	default:
		return nil, "${" + primary + "} runs code: write what it returns"
	}
	data, err := os.ReadFile(filepath.Join(c.dir, path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "${" + primary + "}: there is no " + path + " next to serverless.yml"
	}
	if err != nil {
		return nil, "${" + primary + "}: " + err.Error()
	}
	var file yaml.Node
	if err := yaml.Unmarshal(data, &file); err != nil || len(file.Content) == 0 {
		return nil, "${" + primary + "}: not yaml or json"
	}
	n := file.Content[0]
	if m[2] != "" {
		if n = lookup(n, strings.Split(m[2], ".")); n == nil {
			return nil, "${" + primary + "} names nothing in " + path
		}
	}
	return n, ""
}

// file converts ${file(path):key} where a value goes: the value in the file.
func (c *converter) file(primary, inner string) value {
	n, todo := c.fileNode(primary)
	if todo != "" {
		return notConverted(todo)
	}
	if n.Kind != yaml.ScalarNode {
		return notConverted("${" + inner + "} names an object or a list inside a string")
	}
	return c.scalar(n)
}

// wholeFile returns the yaml a scalar that is only a ${file()} names, to write in its place.
func (c *converter) wholeFile(n *yaml.Node) (*yaml.Node, bool) {
	if n.Kind != yaml.ScalarNode {
		return nil, false
	}
	parts, err := splitVariables(n.Value)
	if err != nil || len(parts) != 1 || !parts[0].variable {
		return nil, false
	}
	primary, _ := splitFallback(parts[0].text)
	if !strings.HasPrefix(primary, "file(") {
		return nil, false
	}
	file, todo := c.fileNode(primary)
	if todo != "" || file.Kind == yaml.ScalarNode {
		return nil, false
	}
	return file, true
}

// mapping writes a yaml mapping as nested pkl Mappings, every key an entry, so a lookup by a key
// only known at evaluation, such as the region, finds it.
func (c *converter) mapping(w *writer, where, head string, n *yaml.Node) {
	w.open(head)
	for _, key := range keys(n) {
		v := get(n, key)
		if v.Kind == yaml.MappingNode {
			c.mapping(w, where+"."+key, "["+quote(key)+"] = new Mapping", v)
			continue
		}
		c.assign(w, where+"."+key, "["+quote(key)+"]", c.nodeValue(v), v)
	}
	w.close()
}

// locals writes what the rest of the file refers to: the conditions, the Mappings and the params.
func (c *converter) locals(w *writer) {
	if len(c.conditions) == 0 && !c.usesMappings && !c.usesParams {
		return
	}
	w.blank()
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for k := range c.conditions {
			if !yield(k) {
				return
			}
		}
	}) {
		w.comment("The condition " + name + " of serverless.yml.")
		w.line("local %s = %s", localName("condition", name), c.conditions[name])
	}
	if c.usesMappings {
		w.comment("The Mappings of serverless.yml.")
		c.mapping(w, "resources.Mappings", "local cfnMappings = new Mapping", get(get(c.doc, "resources"), "Mappings"))
	}
	if c.usesParams {
		params := c.paramsOf()
		w.comment("The params of serverless.yml, by stage; param() takes the stage's, or default's.")
		w.open("local params = new Mapping")
		for _, stage := range slices.Sorted(func(yield func(string) bool) {
			for k := range params {
				if !yield(k) {
					return
				}
			}
		}) {
			w.open(fmt.Sprintf("[%s] = new Mapping", quote(stage)))
			for _, key := range keys(params[stage]) {
				c.assign(w, "params."+stage+"."+key, "["+quote(key)+"]", c.nodeValue(get(params[stage], key)), get(params[stage], key))
			}
			w.close()
		}
		w.close()
		w.line(`local function param(name: String) = params.getOrNull(kdst.stage)?.getOrNull(name) ?? params.getOrNull("default")?.getOrNull(name) ?? throw("no param \(name) for the stage \(kdst.stage)")`)
	}
}
