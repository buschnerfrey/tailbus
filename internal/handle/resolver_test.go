package handle

import "testing"

func TestFindMatchesRanksDeterministically(t *testing.T) {
	entries := map[string]ServiceManifest{
		"alpha": {
			Capabilities: []string{"research.company", "document.summarize"},
			Domains:      []string{"finance"},
			Tags:         []string{"deep", "trusted"},
			Version:      "2.0.0",
			Commands:     []CommandSpec{{Name: "solve"}},
		},
		"beta": {
			Capabilities: []string{"research.company"},
			Domains:      []string{"finance"},
			Tags:         []string{"deep"},
			Version:      "2.0.0",
			Commands:     []CommandSpec{{Name: "solve"}},
		},
		"gamma": {
			Capabilities: []string{"research.company"},
			Domains:      []string{"legal"},
			Tags:         []string{"deep"},
			Version:      "2.0.0",
			Commands:     []CommandSpec{{Name: "solve"}},
		},
	}

	matches := FindMatches(entries, FindQuery{
		Capabilities: []string{"research.company"},
		Domains:      []string{"finance"},
		Tags:         []string{"deep"},
		CommandName:  "solve",
		Version:      "2.0.0",
	})
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matches))
	}
	if matches[0].Handle != "alpha" {
		t.Fatalf("expected alpha first, got %s", matches[0].Handle)
	}
	if matches[1].Handle != "beta" {
		t.Fatalf("expected beta second, got %s", matches[1].Handle)
	}
	if matches[0].Score != matches[1].Score {
		t.Fatalf("expected ties to sort by handle, scores differ: %d vs %d", matches[0].Score, matches[1].Score)
	}
}

func TestFindMatchesRejectsNonMatchesAndAppliesLimit(t *testing.T) {
	entries := map[string]ServiceManifest{
		"a": {Capabilities: []string{"pricing.quote"}, Commands: []CommandSpec{{Name: "quote"}}},
		"b": {Capabilities: []string{"pricing.quote"}, Commands: []CommandSpec{{Name: "quote"}}},
		"c": {Capabilities: []string{"pricing.quote"}},
	}

	matches := FindMatches(entries, FindQuery{
		Capabilities: []string{"pricing.quote"},
		CommandName:  "quote",
		Limit:        1,
	})
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].Handle != "a" {
		t.Fatalf("expected lexical tie-break winner a, got %s", matches[0].Handle)
	}
}

func TestFindMatchesRecordsMatchReasons(t *testing.T) {
	matches := FindMatches(map[string]ServiceManifest{
		"solver": {
			Capabilities: []string{"code.solve"},
			Domains:      []string{"engineering"},
			Tags:         []string{"fast"},
			Version:      "1.2.3",
			Commands:     []CommandSpec{{Name: "solve"}},
		},
	}, FindQuery{
		Capabilities: []string{"code.solve"},
		Domains:      []string{"engineering"},
		Tags:         []string{"fast"},
		CommandName:  "solve",
		Version:      "1.2.3",
	})
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	got := matches[0].MatchReasons
	want := []string{
		"capability:code.solve",
		"domain:engineering",
		"tag:fast",
		"command:solve",
		"version:1.2.3",
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d reasons, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reason %d: want %q got %q", i, want[i], got[i])
		}
	}
}
