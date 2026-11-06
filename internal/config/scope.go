package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// The scopes of a secret's name: as it is, or under the app and stage.
const (
	scopeAbsolute = "absolute"
	scopeStage    = "stage"
)

// scopeSecrets puts the name of each secret kdst.param() and kdst.secret() give under the app and
// stage, /<app>/<stage>/<name> in ssm and <app>/<stage>/<name> in secrets manager, and drops the
// scope of every secret, so what reads the project sees only full names. It returns data with
// those changes.
func scopeSecrets(data []byte, app, stage string) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	var errs []error
	walkSecrets(doc, "", func(where string, s map[string]any) {
		scope, _ := s["scope"].(string)
		delete(s, "scope")
		switch scope {
		case "", scopeAbsolute:
			return
		case scopeStage:
		default:
			errs = append(errs, fmt.Errorf("%s: a secret's scope is absolute or stage, not %q", where, scope))
			return
		}
		source, _ := s["source"].(string)
		name, _ := s["name"].(string)
		full, err := stageName(source, name, app, stage)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", where, err))
			return
		}
		s["name"] = full
	})
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return json.Marshal(doc)
}

// stageName is the full name of a secret of source named relative to the app and stage, or what
// is wrong with name.
func stageName(source, name, app, stage string) (string, error) {
	helper, absolute, prefix := "kdst.param", "kdst.ssm", "/"+app+"/"+stage+"/"
	if source == SourceSecretsManager {
		helper, absolute, prefix = "kdst.secret", "kdst.secretsmanager", app+"/"+stage+"/"
	}
	switch {
	case app == "" || stage == "":
		return "", fmt.Errorf("%s(%q) needs the project's app and stage: set app", helper, name)
	case strings.HasPrefix(name, "/"):
		return "", fmt.Errorf("%s(%q): the name goes after %s: drop the leading /, or use %s(%q) for the full name", helper, name, prefix, absolute, name)
	case name == "" || strings.HasSuffix(name, "/") || strings.Contains(name, "//") || strings.Contains(name, ".."):
		return "", fmt.Errorf("%s(%q): the name goes after %s and has no empty segments or ..: remove them, or use %s() for the full name", helper, name, prefix, absolute)
	}
	return prefix + name, nil
}

// walkSecrets calls fn on each secret inside v, a json document, with where it is, such as
// functions.api.environment.API_KEY.
func walkSecrets(v any, where string, fn func(where string, secret map[string]any)) {
	switch x := v.(type) {
	case map[string]any:
		if x[marker] == kindSecret {
			fn(where, x)
			return
		}
		for _, k := range slices.Sorted(maps.Keys(x)) {
			next := k
			if where != "" {
				next = where + "." + k
			}
			walkSecrets(x[k], next, fn)
		}
	case []any:
		for i, item := range x {
			walkSecrets(item, fmt.Sprintf("%s[%d]", where, i), fn)
		}
	}
}
