package config

import "testing"

func TestEnvStripsOnePairOfQuotes(t *testing.T) {
	for in, want := range map[string]string{
		`plain`:        "plain",
		`  spaced  `:   "spaced",
		`"double"`:     "double",
		`'single'`:     "single",
		`"a@b.co"`:     "a@b.co",
		`"mismatched'`: `"mismatched'`,
		`""`:           "",
		`"`:            `"`,
		`"in"side"`:    `in"side`,
	} {
		t.Setenv("X_TEST", in)
		if got := Env("X_TEST"); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}
