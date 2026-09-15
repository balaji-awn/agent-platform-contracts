package mock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// InputValidator checks an execution input against a version's input schema.
type InputValidator interface {
	// ValidateInput returns one FieldError per violation, with Path a JSON Pointer into input, or
	// nil if input is valid. An error means the schema could not be used; the mock answers 500.
	ValidateInput(schema, input json.RawMessage) ([]FieldError, error)
}

// FieldError is one problem with a request or input. Path is a JSON Pointer. The mock folds these
// into the error message, since the API's error object has no structured details.
type FieldError struct {
	Path    string
	Message string
}

// NoValidation accepts every input.
var NoValidation InputValidator = noValidation{}

type noValidation struct{}

func (noValidation) ValidateInput(json.RawMessage, json.RawMessage) ([]FieldError, error) {
	return nil, nil
}

// BasicValidator is a dependency-free validator for the subset of JSON Schema 2020-12 that agent
// input schemas commonly use: type, enum, const, required, properties, additionalProperties,
// items, minItems, maxItems, minLength, maxLength, pattern, minimum, maximum, exclusiveMinimum, and
// exclusiveMaximum. Other keywords, including $ref and the allOf/anyOf/oneOf combinators, are
// ignored. Plug in a full validator through Options.Validator when a test needs one.
type BasicValidator struct{}

// ValidateInput implements InputValidator.
func (BasicValidator) ValidateInput(schema, input json.RawMessage) ([]FieldError, error) {
	var sch any
	if err := json.Unmarshal(schema, &sch); err != nil {
		return nil, fmt.Errorf("input schema: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("input: %w", err)
	}
	var errs []FieldError
	check(sch, v, "", &errs)
	return errs, nil
}

func check(sch, v any, path string, errs *[]FieldError) {
	add := func(p, msg string) { *errs = append(*errs, FieldError{Path: p, Message: msg}) }
	s, ok := sch.(map[string]any)
	if !ok {
		if b, isBool := sch.(bool); isBool && !b {
			add(path, "no value is allowed here")
		}
		return
	}
	if t, ok := s["type"]; ok && !typeMatches(t, v) {
		add(path, "must be of type "+typeNames(t))
		return
	}
	if e, ok := s["enum"].([]any); ok && !slices.ContainsFunc(e, func(x any) bool { return equalJSON(x, v) }) {
		add(path, "must be one of the enumerated values")
	}
	if c, ok := s["const"]; ok && !equalJSON(c, v) {
		add(path, "must equal the schema's const value")
	}
	switch v := v.(type) {
	case map[string]any:
		if req, ok := s["required"].([]any); ok {
			for _, r := range req {
				if name, ok := r.(string); ok {
					if _, present := v[name]; !present {
						add(path+"/"+escapePointer(name), "is required")
					}
				}
			}
		}
		props, _ := s["properties"].(map[string]any)
		names := make([]string, 0, len(v))
		for name := range v {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			p := path + "/" + escapePointer(name)
			if ps, ok := props[name]; ok {
				check(ps, v[name], p, errs)
				continue
			}
			switch ap := s["additionalProperties"].(type) {
			case bool:
				if !ap {
					add(p, "is not an allowed property")
				}
			case map[string]any:
				check(ap, v[name], p, errs)
			}
		}
	case []any:
		if n, ok := number(s, "minItems"); ok && float64(len(v)) < n {
			add(path, fmt.Sprintf("must have at least %v items", n))
		}
		if n, ok := number(s, "maxItems"); ok && float64(len(v)) > n {
			add(path, fmt.Sprintf("must have at most %v items", n))
		}
		if items, ok := s["items"]; ok {
			for i, e := range v {
				check(items, e, path+"/"+strconv.Itoa(i), errs)
			}
		}
	case string:
		n := float64(utf8.RuneCountInString(v))
		if m, ok := number(s, "minLength"); ok && n < m {
			add(path, fmt.Sprintf("must be at least %v characters", m))
		}
		if m, ok := number(s, "maxLength"); ok && n > m {
			add(path, fmt.Sprintf("must be at most %v characters", m))
		}
		if p, ok := s["pattern"].(string); ok {
			if re, err := regexp.Compile(p); err == nil && !re.MatchString(v) {
				add(path, "must match pattern "+p)
			}
		}
	case json.Number:
		f, _ := v.Float64()
		if m, ok := number(s, "minimum"); ok && f < m {
			add(path, fmt.Sprintf("must be >= %v", m))
		}
		if m, ok := number(s, "maximum"); ok && f > m {
			add(path, fmt.Sprintf("must be <= %v", m))
		}
		if m, ok := number(s, "exclusiveMinimum"); ok && f <= m {
			add(path, fmt.Sprintf("must be > %v", m))
		}
		if m, ok := number(s, "exclusiveMaximum"); ok && f >= m {
			add(path, fmt.Sprintf("must be < %v", m))
		}
	}
}

func typeMatches(t, v any) bool {
	switch t := t.(type) {
	case string:
		return isType(t, v)
	case []any:
		for _, name := range t {
			if n, ok := name.(string); ok && isType(n, v) {
				return true
			}
		}
		return false
	}
	return true
}

func isType(name string, v any) bool {
	switch name {
	case "null":
		return v == nil
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(json.Number)
		return ok
	case "integer":
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		f, err := n.Float64()
		return err == nil && f == math.Trunc(f)
	}
	return true
}

func typeNames(t any) string {
	if list, ok := t.([]any); ok {
		names := make([]string, 0, len(list))
		for _, n := range list {
			names = append(names, fmt.Sprint(n))
		}
		return strings.Join(names, " or ")
	}
	return fmt.Sprint(t)
}

func number(s map[string]any, key string) (float64, bool) {
	f, ok := s[key].(float64)
	return f, ok
}

// equalJSON compares a schema value (numbers as float64) with an input value (numbers as
// json.Number).
func equalJSON(a, b any) bool {
	return reflect.DeepEqual(normalize(a), normalize(b))
}

func normalize(v any) any {
	switch v := v.(type) {
	case json.Number:
		f, _ := v.Float64()
		return f
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = normalize(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = normalize(e)
		}
		return out
	}
	return v
}

func escapePointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}
