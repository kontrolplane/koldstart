package migrate

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// The intrinsic functions of CloudFormation koldstart converts, and Condition, which it does not.
const (
	intrinsicRef       = "Ref"
	intrinsicCondition = "Condition"
	intrinsicIf        = "Fn::If"
)

// isTrue and isFalse report whether a yaml value is the boolean true or false.
func isTrue(n *yaml.Node) bool  { return n != nil && n.Kind == yaml.ScalarNode && n.Value == "true" }
func isFalse(n *yaml.Node) bool { return n != nil && n.Kind == yaml.ScalarNode && n.Value == "false" }

// The yaml tags of plain values.
const (
	tagString = "!!str"
	tagInt    = "!!int"
	tagFloat  = "!!float"
	tagBool   = "!!bool"
	tagNull   = "!!null"
)

// value is a pkl expression for a yaml value that has one: a scalar, a variable, or a ref. ok is
// false for objects and lists, which are written as blocks, and for what cannot be converted,
// which todo then says why.
type value struct {
	expr string
	ok   bool
	todo string
	// interpolable is a string value that can sit inside a "...\(x)..." string, and joinable one
	// that can be a part of a kdst.join(), as a ref can.
	interpolable bool
	joinable     bool
	// noValue is AWS::NoValue: the property is left out.
	noValue bool
}

func literal(expr string) value {
	return value{expr: expr, ok: true, interpolable: true, joinable: true}
}
func refValue(expr string) value    { return value{expr: expr, ok: true, joinable: true} }
func notConverted(why string) value { return value{todo: why} }

// scalar converts a scalar yaml value.
func (c *converter) scalar(n *yaml.Node) value {
	switch n.ShortTag() {
	case tagInt, tagFloat, tagBool:
		return value{expr: n.Value, ok: true}
	case tagNull:
		return value{expr: "null", ok: true}
	default:
		return c.text(n.Value)
	}
}

// text converts a string, the serverless variables in it included.
func (c *converter) text(s string) value {
	parts, err := splitVariables(s)
	if err != nil {
		return notConverted(err.Error())
	}
	if len(parts) == 1 && parts[0].variable {
		return c.variable(parts[0].text)
	}
	var b strings.Builder
	for _, p := range parts {
		if !p.variable {
			b.WriteString(strings.Trim(quote(p.text), `"`))
			continue
		}
		v := c.variable(p.text)
		if !v.ok {
			return v
		}
		if !v.interpolable {
			return notConverted("${" + p.text + "} is a secret or an output, which cannot sit inside a string")
		}
		b.WriteString(`\(` + v.expr + `)`)
	}
	return literal(`"` + b.String() + `"`)
}

// part is a piece of a string: text, or the inside of a ${...} variable.
type part struct {
	text     string
	variable bool
}

// splitVariables splits a string into text and the variables in it, which may nest.
func splitVariables(s string) ([]part, error) {
	var parts []part
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			if s != "" || parts == nil {
				parts = append(parts, part{text: s})
			}
			return parts, nil
		}
		if i > 0 {
			parts = append(parts, part{text: s[:i]})
		}
		depth, end := 0, -1
		for j := i; j < len(s) && end < 0; j++ {
			switch {
			case strings.HasPrefix(s[j:], "${"):
				depth++
				j++
			case s[j] == '}':
				depth--
				if depth == 0 {
					end = j
				}
			}
		}
		if end < 0 {
			return nil, fmt.Errorf("a ${ without its } in %q", s)
		}
		parts = append(parts, part{text: s[i+2 : end], variable: true})
		s = s[end+1:]
	}
}

// variable converts what is inside a ${...}.
func (c *converter) variable(inner string) value {
	primary, fallback := splitFallback(inner)
	if strings.Contains(primary, "${") {
		return notConverted("${" + inner + "} has a variable inside a variable")
	}
	source, path, _ := strings.Cut(primary, ":")
	switch {
	case primary == "sls:stage" || primary == "opt:stage" || primary == "self:provider.stage":
		return literal("kdst.stage")
	case primary == "self:service":
		return literal("module.app")
	case primary == "aws:accountId":
		return refValue("kdst.accountId")
	case source == "param":
		return c.param(path, inner)
	case source == "file" || strings.HasPrefix(primary, "file("):
		return c.file(primary, inner)
	case primary == "aws:region" || primary == "opt:region" || primary == "self:provider.region":
		if c.region == "" {
			return notConverted("${" + inner + "}: no provider.region to refer to")
		}
		return literal("module.region")
	case source == "self":
		return c.self(path, inner)
	case source == "env":
		if fallback != "" {
			return literal(fmt.Sprintf(`read?(%s) ?? %s`, quote("env:"+path), quote(unquoteFallback(fallback))))
		}
		return literal(fmt.Sprintf("read(%s)", quote("env:"+path)))
	case source == "ssm":
		name := strings.TrimSuffix(path, "~true")
		if !strings.HasPrefix(name, "/") && !strings.HasPrefix(name, "arn:") {
			return notConverted("${" + inner + "}: ssm with a region or another form")
		}
		return value{expr: fmt.Sprintf("kdst.ssm(%s)", quote(name)), ok: true}
	case source == "cf" || strings.HasPrefix(source, "cf(") || strings.HasPrefix(source, "cf."):
		return stackOutput(source, path, inner)
	default:
		return notConverted("${" + inner + "} has no equivalent: an import, a read() or a value of your own")
	}
}

// stackOutput converts ${cf:stack.output}, the output of a stack of <service>-<stage>, to the
// output of that stage. ${cf(region):...} and ${cf.region:...} are of a stage in that region.
func stackOutput(source, path, inner string) value {
	stack, output, ok := strings.Cut(path, ".")
	i := strings.LastIndex(stack, "-")
	if !ok || i <= 0 {
		return notConverted("${" + inner + "}: a stack name that is not <service>-<stage>")
	}
	expr := fmt.Sprintf("kdst.stageOutput(%s, %s, %s)", quote(stack[:i]), quote(stack[i+1:]), quote(output))
	region := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(source, "cf("), "cf."), ")")
	if region != "cf" {
		expr = fmt.Sprintf("(%s) { region = %s }", expr, quote(region))
	}
	return value{expr: expr, ok: true}
}

// splitFallback splits ${a, b} into a and its fallback b.
func splitFallback(inner string) (string, string) {
	primary, fallback, _ := strings.Cut(inner, ",")
	return strings.TrimSpace(primary), strings.TrimSpace(fallback)
}

func unquoteFallback(s string) string {
	return strings.Trim(s, `'"`)
}

// self converts ${self:path}: the value the path names, converted where it is.
func (c *converter) self(path, inner string) value {
	if c.selfDepth > 10 {
		return notConverted("${" + inner + "} refers to itself")
	}
	n := lookup(c.doc, strings.Split(path, "."))
	if n == nil {
		return notConverted("${" + inner + "} names nothing in serverless.yml")
	}
	if n.Kind != yaml.ScalarNode {
		return notConverted("${" + inner + "} names an object or a list: write it out")
	}
	c.selfDepth++
	defer func() { c.selfDepth-- }()
	return c.scalar(n)
}

// intrinsic converts a CloudFormation intrinsic function, or reports that n is none.
func (c *converter) intrinsic(n *yaml.Node) (value, bool) {
	name, arg := intrinsicOf(n)
	switch name {
	case "":
		return value{}, false
	case intrinsicRef:
		return c.ref(arg.Value), true
	case "Fn::GetAtt":
		var target, attribute string
		if arg.Kind == yaml.SequenceNode && len(arg.Content) == 2 {
			target, attribute = arg.Content[0].Value, arg.Content[1].Value
		} else {
			target, attribute, _ = strings.Cut(arg.Value, ".")
		}
		return c.getAtt(target, attribute), true
	case "Fn::Sub":
		return c.sub(arg), true
	case "Fn::Join":
		return c.join(arg), true
	case intrinsicIf:
		return c.ifValue(arg), true
	case "Fn::FindInMap":
		return c.findInMap(arg), true
	case "Fn::Base64":
		v := c.nodeValue(arg)
		if !v.ok || !v.interpolable {
			return notConverted("Fn::Base64 of something not known as a string while planning"), true
		}
		return literal("(" + v.expr + ").base64"), true
	case "Fn::Select":
		return c.selectValue(arg), true
	default:
		return notConverted(name + " has no equivalent: write it as a pkl expression"), true
	}
}

// nodeValue converts a node that is one value: a scalar, or an intrinsic function.
func (c *converter) nodeValue(n *yaml.Node) value {
	if n == nil {
		return notConverted("nothing to convert")
	}
	if v, ok := c.intrinsic(n); ok {
		return v
	}
	if n.Kind == yaml.AliasNode {
		return c.nodeValue(n.Alias)
	}
	if n.Kind != yaml.ScalarNode {
		return notConverted("an object or a list where a single value goes")
	}
	return c.scalar(n)
}

// joinValues joins values into one string: a pkl string when every part is one, as
// "arn:aws:s3:::\(kdst.stage)-uploads", else a kdst.join() of them, for refs and the account's
// values.
func joinValues(parts []value) value {
	interpolable := true
	for _, p := range parts {
		switch {
		case !p.ok:
			return p
		case p.noValue:
			return notConverted("AWS::NoValue inside a string")
		case !p.joinable:
			return notConverted("a secret or an output inside a string, which cannot be joined")
		case !p.interpolable:
			interpolable = false
		}
	}
	if interpolable {
		return interpolate(parts)
	}
	var exprs []string
	var run []value
	flush := func() {
		if len(run) == 0 {
			return
		}
		if s := interpolate(run); s.expr != `""` {
			exprs = append(exprs, s.expr)
		}
		run = nil
	}
	for _, p := range parts {
		if p.interpolable {
			run = append(run, p)
			continue
		}
		flush()
		exprs = append(exprs, p.expr)
	}
	flush()
	return refValue("kdst.join(List(" + strings.Join(exprs, ", ") + "))")
}

// interpolate joins string values into one pkl string, each that is not a literal interpolated.
func interpolate(parts []value) value {
	var b strings.Builder
	for _, p := range parts {
		if inner, ok := stringLiteral(p.expr); ok {
			b.WriteString(inner)
			continue
		}
		expr := p.expr
		if strings.HasPrefix(expr, "(if (") && strings.HasSuffix(expr, ")") {
			expr = expr[1 : len(expr)-1]
		}
		b.WriteString(`\(` + expr + `)`)
	}
	return literal(`"` + b.String() + `"`)
}

// stringLiteral returns the inside of a pkl string literal, with its escapes and interpolations,
// or false when expr is not one.
func stringLiteral(expr string) (string, bool) {
	if len(expr) < 2 || expr[0] != '"' || expr[len(expr)-1] != '"' || strings.HasPrefix(expr, `"""`) {
		return "", false
	}
	return expr[1 : len(expr)-1], true
}

// text is a literal string as a value.
func textValue(s string) value {
	return literal(quote(s))
}

// sub converts a Fn::Sub: its ${Name}, ${Name.Attribute}, ${AWS::...} and serverless variables,
// with the variables of its second argument when it has one.
func (c *converter) sub(arg *yaml.Node) value {
	template, vars := arg, (*yaml.Node)(nil)
	if arg.Kind == yaml.SequenceNode && len(arg.Content) == 2 {
		template, vars = arg.Content[0], arg.Content[1]
	}
	if template.Kind != yaml.ScalarNode {
		return notConverted("Fn::Sub of something that is not a string")
	}
	s := template.Value
	var parts []value
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			if s != "" {
				parts = append(parts, textValue(s))
			}
			break
		}
		if i > 0 {
			parts = append(parts, textValue(s[:i]))
		}
		if strings.HasPrefix(s[i:], "${!") {
			end := strings.Index(s[i:], "}")
			if end < 0 {
				return notConverted("a ${ without its } in Fn::Sub")
			}
			parts = append(parts, textValue("${"+s[i+3:i+end]+"}"))
			s = s[i+end+1:]
			continue
		}
		inner, rest, ok := innerVariable(s[i:])
		if !ok {
			return notConverted("a ${ without its } in Fn::Sub")
		}
		parts = append(parts, c.subVariable(inner, vars))
		s = rest
	}
	if len(parts) == 0 {
		return textValue("")
	}
	return joinValues(parts)
}

// innerVariable splits "${a}rest" into a and rest, the ${} nesting counted.
func innerVariable(s string) (string, string, bool) {
	depth := 0
	for j := 0; j < len(s); j++ {
		switch {
		case strings.HasPrefix(s[j:], "${"):
			depth++
			j++
		case s[j] == '}':
			depth--
			if depth == 0 {
				return s[2:j], s[j+1:], true
			}
		}
	}
	return "", "", false
}

// subVariable converts a variable of a Fn::Sub: one of its own, a pseudo parameter, a
// serverless variable, an attribute or a ref.
func (c *converter) subVariable(inner string, vars *yaml.Node) value {
	if v := get(vars, inner); v != nil {
		return c.nodeValue(v)
	}
	switch {
	case strings.HasPrefix(inner, "AWS::"):
		return c.ref(inner)
	case strings.Contains(inner, ":"):
		return c.variable(inner)
	case strings.Contains(inner, "."):
		target, attribute, _ := strings.Cut(inner, ".")
		return c.getAtt(target, attribute)
	}
	return c.ref(inner)
}

// join converts a Fn::Join of a list of values.
func (c *converter) join(arg *yaml.Node) value {
	if arg.Kind != yaml.SequenceNode || len(arg.Content) != 2 || arg.Content[1].Kind != yaml.SequenceNode {
		return notConverted("Fn::Join of something other than a delimiter and a list")
	}
	delimiter := arg.Content[0].Value
	var parts []value
	for i, item := range arg.Content[1].Content {
		if i > 0 && delimiter != "" {
			parts = append(parts, textValue(delimiter))
		}
		parts = append(parts, c.nodeValue(item))
	}
	if len(parts) == 0 {
		return textValue("")
	}
	return joinValues(parts)
}

// selectValue converts a Fn::Select of a list written out.
func (c *converter) selectValue(arg *yaml.Node) value {
	if arg.Kind != yaml.SequenceNode || len(arg.Content) != 2 || arg.Content[1].Kind != yaml.SequenceNode {
		return notConverted("Fn::Select of a list that is only known while deploying, such as Fn::GetAZs")
	}
	i, ok := integer(arg.Content[0])
	if !ok || i < 0 || i >= len(arg.Content[1].Content) {
		return notConverted("Fn::Select with an index outside its list")
	}
	return c.nodeValue(arg.Content[1].Content[i])
}

// ifValue converts a Fn::If where one value goes: if (condition) a else b.
func (c *converter) ifValue(arg *yaml.Node) value {
	cond, a, b, todo := c.ifParts(arg)
	if todo != "" {
		return notConverted(todo)
	}
	va, vb := c.nodeValue(a), c.nodeValue(b)
	switch {
	case !va.ok:
		return va
	case !vb.ok:
		return vb
	case va.noValue || vb.noValue:
		return notConverted("Fn::If with AWS::NoValue where a value goes")
	}
	return value{expr: fmt.Sprintf("(if (%s) %s else %s)", cond, va.expr, vb.expr), ok: true,
		interpolable: va.interpolable && vb.interpolable, joinable: va.joinable && vb.joinable}
}

// ifParts reads a Fn::If: its condition as a pkl expression, and its two values.
func (c *converter) ifParts(arg *yaml.Node) (string, *yaml.Node, *yaml.Node, string) {
	if arg.Kind != yaml.SequenceNode || len(arg.Content) != 3 {
		return "", nil, nil, "Fn::If takes a condition and two values"
	}
	cond, ok := c.conditionName(arg.Content[0].Value)
	if !ok {
		return "", nil, nil, "Fn::If of the condition " + arg.Content[0].Value + ", which is not converted"
	}
	return cond, arg.Content[1], arg.Content[2], ""
}

// findInMap converts a Fn::FindInMap into a lookup in the Mappings, written as a local.
func (c *converter) findInMap(arg *yaml.Node) value {
	if arg.Kind != yaml.SequenceNode || len(arg.Content) < 3 {
		return notConverted("Fn::FindInMap takes a map, a key and a second key")
	}
	mapName := arg.Content[0].Value
	if get(get(get(c.doc, "resources"), "Mappings"), mapName) == nil {
		return notConverted("Fn::FindInMap of " + mapName + ", which Mappings does not have")
	}
	k1, k2 := c.nodeValue(arg.Content[1]), c.nodeValue(arg.Content[2])
	for _, k := range []value{k1, k2} {
		if !k.ok || !k.interpolable {
			return notConverted("Fn::FindInMap with a key only known while deploying")
		}
	}
	c.usesMappings = true
	return literal(fmt.Sprintf("cfnMappings[%s][%s][%s]", quote(mapName), k1.expr, k2.expr))
}

// intrinsicOf returns the function a node calls and its argument: { Ref: x }, or !Ref x.
func intrinsicOf(n *yaml.Node) (string, *yaml.Node) {
	if strings.HasPrefix(n.Tag, "!") && !strings.HasPrefix(n.Tag, "!!") {
		name := strings.TrimPrefix(n.Tag, "!")
		if name != intrinsicRef && name != intrinsicCondition {
			name = "Fn::" + name
		}
		arg := *n
		arg.Tag = ""
		return name, &arg
	}
	if n.Kind == yaml.MappingNode && len(n.Content) == 2 {
		key := n.Content[0].Value
		if key == intrinsicRef || strings.HasPrefix(key, "Fn::") || key == intrinsicCondition {
			return key, n.Content[1]
		}
	}
	return "", nil
}

// ref converts a Ref: to a resource, its identifier, which is what Ref returns for most types;
// to a function, its name.
func (c *converter) ref(target string) value {
	switch target {
	case "AWS::Region":
		if c.region == "" {
			return notConverted("Ref AWS::Region: no provider.region to refer to")
		}
		return literal("module.region")
	case "AWS::AccountId":
		return refValue("kdst.accountId")
	case "AWS::Partition":
		return refValue("kdst.partition")
	case "AWS::URLSuffix":
		return refValue("kdst.urlSuffix")
	case "AWS::StackName":
		return literal(`"\(module.app)-\(kdst.stage)"`)
	case "AWS::NoValue":
		return value{ok: true, noValue: true}
	case "AWS::StackId", "AWS::NotificationARNs":
		return notConverted("Ref " + target + " has no equivalent")
	}
	if fn, ok := c.functionOf(target); ok {
		return refValue(fmt.Sprintf(`kdst.ref(%s, "FunctionName")`, quote(fn)))
	}
	if name, ok := c.resourceNames[target]; ok {
		return refValue(fmt.Sprintf(`kdst.ref(%s, "Identifier")`, quote(name)))
	}
	return notConverted("Ref " + target + " names nothing koldstart deploys")
}

// getAtt converts a Fn::GetAtt to a ref of the same attribute.
func (c *converter) getAtt(target, attribute string) value {
	if fn, ok := c.functionOf(target); ok {
		return refValue(fmt.Sprintf("kdst.ref(%s, %s)", quote(fn), quote(attribute)))
	}
	if name, ok := c.resourceNames[target]; ok {
		return refValue(fmt.Sprintf("kdst.ref(%s, %s)", quote(name), quote(attribute)))
	}
	return notConverted("Fn::GetAtt " + target + "." + attribute + " names nothing koldstart deploys")
}

// functionOf finds the function a logical id of the serverless framework stands for, such as
// ApiLambdaFunction for the function api.
func (c *converter) functionOf(logicalID string) (string, bool) {
	name, ok := c.functionsByLogicalID[logicalID]
	return name, ok
}

// logicalID is the logical id the serverless framework gives a function's lambda function.
func logicalID(function string) string {
	r := strings.NewReplacer("-", "Dash", "_", "Underscore")
	name := r.Replace(function)
	return strings.ToUpper(name[:1]) + name[1:] + "LambdaFunction"
}

// property writes key = value, or key { ... } for an object or a list, as dynamic properties.
func (c *converter) property(w *writer, where, key string, n *yaml.Node) {
	c.member(w, where, member(key), n)
}

// member writes a property or an element of a dynamic object: head = value, or head { ... }.
func (c *converter) member(w *writer, where, head string, n *yaml.Node) {
	if name, arg := intrinsicOf(n); name == intrinsicIf {
		c.ifMember(w, where, head, arg)
		return
	}
	if file, ok := c.wholeFile(n); ok {
		w.comment("From " + n.Value + ".")
		c.member(w, where, head, file)
		return
	}
	if v, ok := c.intrinsic(n); ok {
		if v.noValue {
			return
		}
		c.assign(w, where, head, v, n)
		return
	}
	switch n.Kind {
	case yaml.ScalarNode:
		c.assign(w, where, head, c.scalar(n), n)
	case yaml.AliasNode:
		c.member(w, where, head, n.Alias)
	case yaml.MappingNode:
		w.open(head)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			c.property(w, where+"."+k, k, n.Content[i+1])
		}
		w.close()
	case yaml.SequenceNode:
		if len(n.Content) == 0 {
			c.assign(w, where, head, literal("new Listing {}"), n)
			return
		}
		w.open(head)
		for i, item := range n.Content {
			c.element(w, fmt.Sprintf("%s[%d]", where, i), item)
		}
		w.close()
	}
}

// element writes an element of a list.
func (c *converter) element(w *writer, where string, n *yaml.Node) {
	if name, arg := intrinsicOf(n); name == intrinsicIf {
		c.ifMember(w, where, "new", arg)
		return
	}
	if v, ok := c.intrinsic(n); ok {
		if v.noValue {
			return
		}
		c.elementValue(w, where, v, n)
		return
	}
	switch n.Kind {
	case yaml.MappingNode:
		c.member(w, where, "new", n)
	case yaml.SequenceNode:
		c.member(w, where, "new Listing", n)
	case yaml.AliasNode:
		c.element(w, where, n.Alias)
	default:
		c.elementValue(w, where, c.scalar(n), n)
	}
}

func (c *converter) elementValue(w *writer, where string, v value, n *yaml.Node) {
	if !v.ok {
		c.leaveOut(w, where, v.todo, n)
		return
	}
	w.line("%s", v.expr)
}

// assign writes head = value, or leaves it out with a todo.
func (c *converter) assign(w *writer, where, head string, v value, n *yaml.Node) {
	if !v.ok {
		c.leaveOut(w, where, v.todo, n)
		return
	}
	if head == "new" || head == "new Listing" {
		w.line("%s", v.expr)
		return
	}
	w.line("%s = %s", head, v.expr)
}

// leaveOut writes what could not be converted as a todo, with the yaml it came from.
func (c *converter) leaveOut(w *writer, where, why string, n *yaml.Node) {
	c.leave(w, Add, where, why, n)
}

// flow is a node as one line of yaml.
func flow(n *yaml.Node) string {
	copied := deepCopy(n)
	setFlow(copied)
	data, err := yaml.Marshal(copied)
	if err != nil {
		return n.Value
	}
	return strings.TrimSpace(string(data))
}

func deepCopy(n *yaml.Node) *yaml.Node {
	c := *n
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, child := range n.Content {
		c.Content[i] = deepCopy(child)
	}
	return &c
}

func setFlow(n *yaml.Node) {
	if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
		n.Style = yaml.FlowStyle
	}
	n.HeadComment, n.LineComment, n.FootComment = "", "", ""
	for _, child := range n.Content {
		setFlow(child)
	}
}

// jsonString is a yaml value as a pkl string of its json, as filter policies and inputs take it.
func jsonString(n *yaml.Node) (string, error) {
	var v any
	if err := n.Decode(&v); err != nil {
		return "", err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return quote(string(data)), nil
}

// lookup finds the node at a path of keys in a mapping.
func lookup(n *yaml.Node, path []string) *yaml.Node {
	for _, key := range path {
		n = get(n, key)
		if n == nil {
			return nil
		}
	}
	return n
}

// get returns the value of a key of a mapping, or nil.
func get(n *yaml.Node, key string) *yaml.Node {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// str returns the string of a scalar, or "".
func str(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

// integer returns the number of a scalar, and whether it is one.
func integer(n *yaml.Node) (int, bool) {
	if n == nil {
		return 0, false
	}
	i, err := strconv.Atoi(n.Value)
	return i, err == nil
}

// keys returns the keys of a mapping, in their order.
func keys(n *yaml.Node) []string {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	out := make([]string, 0, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, n.Content[i].Value)
	}
	return out
}
