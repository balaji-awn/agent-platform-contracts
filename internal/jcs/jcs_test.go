package jcs

import (
	"math"
	"strings"
	"testing"
)

// Examples from RFC 8785 sections 3.2.2 and 3.2.3.
func TestTransformRFCExamples(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{
			name: "primitives",
			in: `{
  "numbers": [333333333.33333329, 1E30, 4.50, 2e-3, 0.000000000000000000000000001],
  "string": "\u20ac$\u000F\u000aA'\u0042\u0022\u005c\\\"\/",
  "literals": [null, true, false]
}`,
			want: `{"literals":[null,true,false],"numbers":[333333333.3333333,1e+30,4.5,0.002,1e-27],"string":"€$\u000f\nA'B\"\\\\\"/"}`,
		},
		{
			name: "sorting by UTF-16 code units",
			in: `{
  "\u20ac": "Euro Sign",
  "\r": "Carriage Return",
  "\ufb33": "Hebrew Letter Dalet With Dagesh",
  "1": "One",
  "\ud83d\ude00": "Emoji: Grinning Face",
  "\u0080": "Control",
  "\u00f6": "Latin Small Letter O With Diaeresis"
}`,
			want: "{\"\\r\":\"Carriage Return\",\"1\":\"One\",\"\u0080\":\"Control\",\"ö\":\"Latin Small Letter O With Diaeresis\",\"€\":\"Euro Sign\",\"😀\":\"Emoji: Grinning Face\",\"\ufb33\":\"Hebrew Letter Dalet With Dagesh\"}",
		},
		{
			name: "nesting and whitespace",
			in:   " { \"b\" : [ 1 , { \"d\":1, \"c\":2 } ], \"a\" : {} } ",
			want: `{"a":{},"b":[1,{"c":2,"d":1}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Transform([]byte(tt.in))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

// Number vectors from RFC 8785 appendix B.
func TestFormatNumberRFCVectors(t *testing.T) {
	tests := []struct {
		bits uint64
		want string
	}{
		{0x0000000000000000, "0"},
		{0x8000000000000000, "0"},
		{0x0000000000000001, "5e-324"},
		{0x8000000000000001, "-5e-324"},
		{0x7fefffffffffffff, "1.7976931348623157e+308"},
		{0xffefffffffffffff, "-1.7976931348623157e+308"},
		{0x4340000000000000, "9007199254740992"},
		{0xc340000000000000, "-9007199254740992"},
		{0x4430000000000000, "295147905179352830000"},
		{0x44b52d02c7e14af5, "9.999999999999997e+22"},
		{0x44b52d02c7e14af6, "1e+23"},
		{0x44b52d02c7e14af7, "1.0000000000000001e+23"},
		{0x444b1ae4d6e2ef4e, "999999999999999700000"},
		{0x444b1ae4d6e2ef4f, "999999999999999900000"},
		{0x444b1ae4d6e2ef50, "1e+21"},
		{0x3eb0c6f7a0b5ed8c, "9.999999999999997e-7"},
		{0x3eb0c6f7a0b5ed8d, "0.000001"},
		{0x41b3de4355555553, "333333333.3333332"},
		{0x41b3de4355555554, "333333333.33333325"},
		{0x41b3de4355555555, "333333333.3333333"},
		{0x41b3de4355555556, "333333333.3333334"},
		{0x41b3de4355555557, "333333333.33333343"},
		{0xbecbf647612f3696, "-0.0000033333333333333333"},
		{0x43143ff3c1cb0959, "1424953923781206.2"},
	}
	for _, tt := range tests {
		got, err := formatNumber(math.Float64frombits(tt.bits))
		if err != nil {
			t.Errorf("%#x: %v", tt.bits, err)
			continue
		}
		if got != tt.want {
			t.Errorf("%#x: got %s, want %s", tt.bits, got, tt.want)
		}
	}
}

func TestTransformRejects(t *testing.T) {
	for _, in := range []string{``, `{`, `1e400`, `{} {}`, `[1,]`} {
		if _, err := Transform([]byte(in)); err == nil {
			t.Errorf("Transform(%q) succeeded, want error", in)
		}
	}
	if _, err := formatNumber(math.NaN()); err == nil || !strings.Contains(err.Error(), "not a valid") {
		t.Errorf("formatNumber(NaN) = %v, want error", err)
	}
}
