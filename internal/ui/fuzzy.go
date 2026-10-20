package ui

import (
	"slices"
	"strings"
	"unicode"
)

// match is how well a query matched a text, and which runes of the text
// matched, to highlight them.
type match struct {
	score int
	pos   []int
}

// fold lowercases and drops Spanish accents: "produccion" finds "Producción".
func fold(r rune) rune {
	switch r {
	case 'á', 'Á':
		return 'a'
	case 'é', 'É':
		return 'e'
	case 'í', 'Í':
		return 'i'
	case 'ó', 'Ó':
		return 'o'
	case 'ú', 'Ú', 'ü', 'Ü':
		return 'u'
	case 'ñ', 'Ñ':
		return 'n'
	}
	return unicode.ToLower(r)
}

func folded(s string) []rune {
	rs := []rune(s)
	for i, r := range rs {
		rs[i] = fold(r)
	}
	return rs
}

// fuzzy matches every word of the query against text, each anywhere in it:
// "odo pr" finds a PR of odomto. A word that appears whole scores far above
// one whose letters are only scattered in order; starting a word scores
// more than landing in the middle of one. An empty query matches all.
func fuzzy(query, text string) (match, bool) {
	t := folded(text)
	var m match
	for _, word := range strings.Fields(query) {
		s, pos, ok := matchWord(folded(word), t)
		if !ok {
			return match{}, false
		}
		m.score += s
		m.pos = append(m.pos, pos...)
	}
	slices.Sort(m.pos)
	m.pos = slices.Compact(m.pos)
	return m, true
}

func wordStart(t []rune, i int) bool {
	return i == 0 || !unicode.IsLetter(t[i-1]) && !unicode.IsDigit(t[i-1])
}

func matchWord(q, t []rune) (int, []int, bool) {
	// A substring: the best one starts a word, then the earliest.
	best, at := -1, -1
	for i := 0; i+len(q) <= len(t); i++ {
		if !slices.Equal(t[i:i+len(q)], q) {
			continue
		}
		s := 100 + 3*len(q) - min(i, 20)/2
		if wordStart(t, i) {
			s += 30
		}
		if s > best {
			best, at = s, i
		}
	}
	if at >= 0 {
		pos := make([]int, len(q))
		for j := range q {
			pos[j] = at + j
		}
		return best, pos, true
	}
	// A subsequence, taken greedily from the left.
	s, last := 0, -1
	pos := make([]int, 0, len(q))
	for _, c := range q {
		i := last + 1
		for i < len(t) && t[i] != c {
			i++
		}
		if i == len(t) {
			return 0, nil, false
		}
		s++
		if last >= 0 && i == last+1 {
			s += 5
		}
		if wordStart(t, i) {
			s += 8
		}
		if last >= 0 {
			s -= min(i-last-1, 5)
		}
		pos = append(pos, i)
		last = i
	}
	return s, pos, true
}
