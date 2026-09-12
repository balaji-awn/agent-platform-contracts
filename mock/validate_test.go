package mock

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestBasicValidator(t *testing.T) {
	tests := []struct {
		name, schema, input string
		want                []string // error paths
	}{
		{"valid", `{"type":"object","required":["a"],"properties":{"a":{"type":"string"}}}`, `{"a":"x"}`, nil},
		{"wrong root type", `{"type":"object"}`, `[]`, []string{""}},
		{"missing required", `{"type":"object","required":["a","b"]}`, `{"a":1}`, []string{"/b"}},
		{"nested type", `{"properties":{"a":{"properties":{"b":{"type":"integer"}}}}}`, `{"a":{"b":1.5}}`, []string{"/a/b"}},
		{"integer accepts 2.0", `{"type":"integer"}`, `2.0`, nil},
		{"type list", `{"type":["string","null"]}`, `null`, nil},
		{"enum", `{"enum":["x",1]}`, `2`, []string{""}},
		{"enum number match", `{"enum":["x",1]}`, `1.0`, nil},
		{"const", `{"const":{"a":[1]}}`, `{"a":[1]}`, nil},
		{"additionalProperties false", `{"properties":{"a":{}},"additionalProperties":false}`, `{"a":1,"b":2,"c/d":3}`, []string{"/b", "/c~1d"}},
		{"additionalProperties schema", `{"additionalProperties":{"type":"string"}}`, `{"a":"x","b":1}`, []string{"/b"}},
		{"items", `{"items":{"type":"string"},"minItems":1}`, `["a",2]`, []string{"/1"}},
		{"array bounds", `{"minItems":2,"maxItems":3}`, `[1]`, []string{""}},
		{"string bounds", `{"minLength":2,"maxLength":3}`, `"héllo"`, []string{""}},
		{"pattern", `{"pattern":"^a+$"}`, `"ab"`, []string{""}},
		{"numeric bounds", `{"minimum":1,"exclusiveMaximum":10}`, `10`, []string{""}},
		{"false schema", `{"properties":{"a":false}}`, `{"a":1}`, []string{"/a"}},
		{"unknown keywords ignored", `{"$ref":"#/x","allOf":[{"type":"string"}]}`, `1`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs, err := BasicValidator{}.ValidateInput(json.RawMessage(tt.schema), json.RawMessage(tt.input))
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, e := range errs {
				got = append(got, e.Path)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("error paths %q, want %q (%+v)", got, tt.want, errs)
			}
		})
	}
	if _, err := (BasicValidator{}).ValidateInput(json.RawMessage(`{`), json.RawMessage(`1`)); err == nil {
		t.Error("invalid schema accepted")
	}
}
