package ui

import (
	"slices"
	"testing"
)

func TestFuzzy(t *testing.T) {
	for _, c := range []struct {
		query, text string
		ok          bool
		pos         []int
	}{
		{"odo", "odomto  feat/sparkline", true, []int{0, 1, 2}},
		{"produccion", "Ir al panel Producción", true, []int{12, 13, 14, 15, 16, 17, 18, 19, 20, 21}},
		{"bt2", "Migrar a Bubble Tea v2", true, []int{9, 16, 21}},
		{"odo 88", "#88 Migrar  otuletta/odomto", true, []int{1, 2, 21, 22, 23}},
		{"", "lo que sea", true, nil},
		{"zzz", "odomto", false, nil},
		{"odo kdx", "odomto", false, nil}, // every word must match
	} {
		m, ok := fuzzy(c.query, c.text)
		if ok != c.ok || ok && !slices.Equal(m.pos, c.pos) {
			t.Errorf("fuzzy(%q, %q) = %v %v, want %v %v", c.query, c.text, m.pos, ok, c.pos, c.ok)
		}
	}
}

func TestFuzzyRanking(t *testing.T) {
	score := func(q, text string) int {
		m, ok := fuzzy(q, text)
		if !ok {
			t.Fatalf("%q does not match %q", q, text)
		}
		return m.score
	}
	// Whole beats scattered; a word start beats the middle of a word.
	if score("kdx", "kdx  feat/login") <= score("kdx", "kodex de algo") {
		t.Error("a whole word ranks below scattered letters")
	}
	if score("log", "feat/login") <= score("log", "changelog") {
		t.Error("the start of a word ranks below its middle")
	}
}
