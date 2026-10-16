package kernel

import (
	"slices"
	"testing"
)

func TestParseRelease(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"v6.12", "v6.12"},
		{"v6.12-rc3", "v6.12"},
		{"6.12.107", "v6.12"},
		{"v2.6.32", "v2.6.32"},
		{"v2.6.32-rc8", "v2.6.32"},
		{"2.6.32.71", "v2.6.32"},
		{"v3.0", "v3.0"},
		{"7.0.0-39-generic", "v7.0"},
	} {
		r := ParseRelease(tc.in)
		if r.String() != tc.want {
			t.Errorf("ParseRelease(%q) = %v, want %s", tc.in, r, tc.want)
		}
	}
	for _, in := range []string{"", "v2.6", "foo", "vX.Y", "v2.4.0", "2.4.37", "next-20261001"} {
		if r := ParseRelease(in); r != 0 {
			t.Errorf("ParseRelease(%q) = %v, want 0", in, r)
		}
	}
	if !(ParseRelease("v2.6.39") < ParseRelease("v3.0") && ParseRelease("v6.19") < ParseRelease("v7.0") &&
		ParseRelease("v7.2") < Unreleased) {
		t.Error("releases are not ordered")
	}
}

func TestParseReleases(t *testing.T) {
	body := `{"releases": [
		{"moniker": "mainline", "version": "6.19-rc3", "iseol": false},
		{"moniker": "stable", "version": "6.18.5", "iseol": false},
		{"moniker": "longterm", "version": "6.12.43", "iseol": false},
		{"moniker": "longterm", "version": "5.10.240", "iseol": true},
		{"moniker": "linux-next", "version": "next-20260101", "iseol": false}]}`
	got, err := ParseReleases([]byte(body))
	want := []Series{{"6.18", false}, {"6.12", true}}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("ParseReleases = %v, %v; want %v", got, err, want)
	}
	// Right after its release a series has no patch level yet.
	got, err = ParseReleases([]byte(`{"releases": [{"moniker": "stable", "version": "7.3", "iseol": false}]}`))
	if want := []Series{{"7.3", false}}; err != nil || !slices.Equal(got, want) {
		t.Errorf("ParseReleases of a new series = %v, %v; want %v", got, err, want)
	}
	if _, err := ParseReleases([]byte(`{"releases": []}`)); err == nil {
		t.Error("ParseReleases of an empty list succeeded")
	}
	if _, err := ParseReleases([]byte(`nonsense`)); err == nil {
		t.Error("ParseReleases of nonsense succeeded")
	}
}

func TestTagOrder(t *testing.T) {
	sorted := []string{"v2.6.11", "v2.6.32-rc1", "v2.6.32", "v2.6.32.1", "v2.6.32.10", "v2.6.33-rc1",
		"v2.6.39.4", "v3.0-rc1", "v3.0", "v3.0.1", "v6.6", "v6.6.8", "v6.7-rc1", "v6.7"}
	stable := map[string]bool{"v2.6.32.1": true, "v2.6.32.10": true, "v2.6.39.4": true, "v3.0.1": true, "v6.6.8": true}
	var tags []Tag
	for _, name := range sorted {
		tag, ok := ParseTag(name)
		if !ok || tag.Stable != stable[name] || tag.Release != ParseRelease(name) {
			t.Fatalf("ParseTag(%s) = %+v, %v", name, tag, ok)
		}
		tags = append(tags, tag)
	}
	shuffled := slices.Clone(tags)
	slices.Reverse(shuffled)
	slices.SortFunc(shuffled, Tag.Compare)
	if !slices.Equal(shuffled, tags) {
		t.Errorf("sorted %v, want %v", shuffled, tags)
	}
	for _, name := range []string{"v2.6.16.28-rc1", "v5.12-rc1-dontuse", "v6.6.8-rc1", "v6", "6.6", "v6.6.8.1", "v2.4.0"} {
		if _, ok := ParseTag(name); ok {
			t.Errorf("ParseTag(%s) accepted", name)
		}
	}
}
