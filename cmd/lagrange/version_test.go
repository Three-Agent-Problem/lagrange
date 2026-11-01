package main

import "testing"

func TestVersionString(t *testing.T) {
	defer func(v, c, d string) { version, commit, date = v, c, d }(version, commit, date)
	version, commit, date = "0.1.0", "e139944d2b7c0f1a", "2026-11-01T17:00:00Z"
	if got, want := versionString(), "0.1.0 (e139944, 2026-11-01)"; got != want {
		t.Errorf("versionString() = %q, want %q", got, want)
	}
}
