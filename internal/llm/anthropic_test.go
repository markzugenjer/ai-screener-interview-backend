package llm

import "testing"

func TestExtractJSON(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`: `{"a":1}`,
		"Sure!\n```json\n{\"a\":{\"b\":\"}\"}}\n```": `{"a":{"b":"}"}}`,
		`prefix {"s":"quote \" and { brace"} tail`:   `{"s":"quote \" and { brace"}`,
	}
	for in, want := range cases {
		got, err := ExtractJSON(in)
		if err != nil || got != want {
			t.Errorf("%q -> %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"no json here", `{"open": true`} {
		if _, err := ExtractJSON(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}
