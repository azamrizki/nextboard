package orch

import (
	"strings"
	"unicode"
)

// Name matching for @mentions (docs/API-orchestration.md §3.3): exact after
// normalisation, otherwise Damerau-Levenshtein ≤ 1 (≤ 4 letters) / ≤ 2, or
// the same Double Metaphone primary key.

// Norm lowercases and drops everything that is not a letter or digit.
func Norm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Distance is the optimal-string-alignment Damerau-Levenshtein distance.
func Distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	n, m := len(ra), len(rb)
	d := make([][]int, n+1)
	for i := range d {
		d[i] = make([]int, m+1)
		d[i][0] = i
	}
	for j := 0; j <= m; j++ {
		d[0][j] = j
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[n][m]
}

// Similar reports whether typed is a likely misspelling of name (both
// normalised, not equal).
func Similar(typed, name string) bool {
	if typed == "" || name == "" || typed == name {
		return false
	}
	limit := 2
	if len([]rune(typed)) <= 4 {
		limit = 1
	}
	if Distance(typed, name) <= limit {
		return true
	}
	p, q := Metaphone(typed), Metaphone(name)
	return p != "" && p == q || indonesianKey(typed) == indonesianKey(name)
}

var (
	indonesianLetters = strings.NewReplacer("dj", "j", "tj", "c", "oe", "u", "kh", "k", "dh", "d", "sy", "s", "sh", "s", "ch", "c",
		"ph", "p", "f", "p", "v", "p", "z", "s", "q", "k", "y", "i")
	indonesianVowels = strings.NewReplacer("ai", "e", "ei", "e", "au", "o")
)

// indonesianKey folds the ways one Indonesian name gets written: old spelling
// (Djihan), loan letters (Arkhan, Syifa, Nopal for Naufal), two-way diphthongs
// (Raihan, Reyhan), doubled letters (Daffa) and a final h (Aisyah). Default
// handles are Indonesian names, which Metaphone is not made for.
func indonesianKey(name string) string {
	s := indonesianVowels.Replace(indonesianLetters.Replace(name))
	var b strings.Builder
	var last rune
	for _, r := range s {
		if r != last {
			b.WriteRune(r)
		}
		last = r
	}
	return strings.TrimSuffix(b.String(), "h")
}

// Metaphone returns the primary Double Metaphone key (Lawrence Philips'
// algorithm, primary encoding only, 4 characters) of an English name.
func Metaphone(word string) string {
	v := []rune(strings.ToUpper(strings.TrimSpace(word)))
	if len(v) == 0 {
		return ""
	}
	m := &mp{v: v, last: len(v) - 1}
	m.slavo = m.has("W") || m.has("K") || m.has("CZ") || m.has("WITZ")
	cur := 0
	if m.at(0, "GN", "KN", "PN", "WR", "PS") {
		cur = 1
	}
	if v[0] == 'X' {
		m.add("S")
		cur = 1
	}
	for len(m.out) < 4 && cur < len(v) {
		cur = m.step(cur)
	}
	if len(m.out) > 4 {
		return m.out[:4]
	}
	return m.out
}

type mp struct {
	v     []rune
	last  int
	slavo bool
	out   string
}

func (m *mp) has(s string) bool { return strings.Contains(string(m.v), s) }
func (m *mp) add(s string)      { m.out += s }

func (m *mp) ch(i int) rune {
	if i < 0 || i >= len(m.v) {
		return 0
	}
	return m.v[i]
}

func (m *mp) vowel(i int) bool {
	switch m.ch(i) {
	case 'A', 'E', 'I', 'O', 'U', 'Y':
		return true
	}
	return false
}

// at reports whether one of opts starts at i.
func (m *mp) at(i int, opts ...string) bool {
	if i < 0 {
		return false
	}
	for _, o := range opts {
		r := []rune(o)
		if i+len(r) > len(m.v) {
			continue
		}
		if string(m.v[i:i+len(r)]) == o {
			return true
		}
	}
	return false
}

func (m *mp) next(i int, c rune) int {
	if m.ch(i+1) == c {
		return i + 2
	}
	return i + 1
}

func (m *mp) step(i int) int {
	switch c := m.ch(i); c {
	case 'A', 'E', 'I', 'O', 'U', 'Y':
		if i == 0 {
			m.add("A")
		}
		return i + 1
	case 'B':
		m.add("P")
		return m.next(i, 'B')
	case 'Ç':
		m.add("S")
		return i + 1
	case 'C':
		return m.stepC(i)
	case 'D':
		if m.at(i, "DG") {
			if m.at(i+2, "I", "E", "Y") {
				m.add("J")
				return i + 3
			}
			m.add("TK")
			return i + 2
		}
		m.add("T")
		if m.at(i, "DT", "DD") {
			return i + 2
		}
		return i + 1
	case 'F':
		m.add("F")
		return m.next(i, 'F')
	case 'G':
		return m.stepG(i)
	case 'H':
		if (i == 0 || m.vowel(i-1)) && m.vowel(i+1) {
			m.add("H")
			return i + 2
		}
		return i + 1
	case 'J':
		switch {
		case m.at(i, "JOSE") || m.at(0, "SAN "):
			if (i == 0 && m.ch(i+4) == ' ') || m.at(0, "SAN ") {
				m.add("H")
			} else {
				m.add("J")
			}
			return i + 1
		case i == 0:
			m.add("J")
		case m.vowel(i-1) && !m.slavo && (m.ch(i+1) == 'A' || m.ch(i+1) == 'O'):
			m.add("J")
		case i == m.last:
			m.add("J")
		case !m.at(i+1, "L", "T", "K", "S", "N", "M", "B", "Z") && !m.at(i-1, "S", "K", "L"):
			m.add("J")
		}
		return m.next(i, 'J')
	case 'K':
		m.add("K")
		return m.next(i, 'K')
	case 'L':
		m.add("L")
		return m.next(i, 'L')
	case 'M':
		m.add("M")
		if (m.at(i-1, "UMB") && (i+1 == m.last || m.at(i+2, "ER"))) || m.ch(i+1) == 'M' {
			return i + 2
		}
		return i + 1
	case 'N', 'Ñ':
		m.add("N")
		return m.next(i, 'N')
	case 'P':
		if m.ch(i+1) == 'H' {
			m.add("F")
			return i + 2
		}
		m.add("P")
		if m.at(i+1, "P", "B") {
			return i + 2
		}
		return i + 1
	case 'Q':
		m.add("K")
		return m.next(i, 'Q')
	case 'R':
		if !(i == m.last && !m.slavo && m.at(i-2, "IE") && !m.at(i-4, "ME", "MA")) {
			m.add("R")
		}
		return m.next(i, 'R')
	case 'S':
		return m.stepS(i)
	case 'T':
		switch {
		case m.at(i, "TION"), m.at(i, "TIA", "TCH"):
			m.add("X")
			return i + 3
		case m.at(i, "TH", "TTH"):
			if m.at(i+2, "OM", "AM") || m.at(0, "VAN ", "VON ", "SCH") {
				m.add("T")
			} else {
				m.add("0")
			}
			return i + 2
		}
		m.add("T")
		if m.at(i+1, "T", "D") {
			return i + 2
		}
		return i + 1
	case 'V':
		m.add("F")
		return m.next(i, 'V')
	case 'W':
		if m.at(i, "WR") {
			m.add("R")
			return i + 2
		}
		if i == 0 && (m.vowel(i+1) || m.at(i, "WH")) {
			m.add("A")
		}
		if m.at(i, "WICZ", "WITZ") {
			m.add("TS")
			return i + 4
		}
		return i + 1
	case 'X':
		if !(i == m.last && (m.at(i-3, "IAU", "EAU") || m.at(i-2, "AU", "OU"))) {
			m.add("KS")
		}
		if m.at(i+1, "C", "X") {
			return i + 2
		}
		return i + 1
	case 'Z':
		if m.ch(i+1) == 'H' {
			m.add("J")
			return i + 2
		}
		m.add("S")
		return m.next(i, 'Z')
	}
	return i + 1
}

func (m *mp) stepC(i int) int {
	switch {
	case i > 1 && !m.vowel(i-2) && m.at(i-1, "ACH") && m.ch(i+2) != 'I' && (m.ch(i+2) != 'E' || m.at(i-2, "BACHER", "MACHER")):
		m.add("K")
		return i + 2
	case i == 0 && m.at(i, "CAESAR"):
		m.add("S")
		return i + 2
	case m.at(i, "CHIA"):
		m.add("K")
		return i + 2
	case m.at(i, "CH"):
		switch {
		case i > 0 && m.at(i, "CHAE"):
			m.add("K")
		case i == 0 && (m.at(i+1, "HARAC", "HARIS") || m.at(i+1, "HOR", "HYM", "HIA", "HEM")) && !m.at(0, "CHORE"):
			m.add("K")
		case m.at(0, "VAN ", "VON ", "SCH") || m.at(i-2, "ORCHES", "ARCHIT", "ORCHID") || m.at(i+2, "T", "S") ||
			((m.at(i-1, "A", "O", "U", "E") || i == 0) && m.at(i+2, "L", "R", "N", "M", "B", "H", "F", "V", "W", " ")):
			m.add("K")
		case i > 0 && m.at(0, "MC"):
			m.add("K")
		default:
			m.add("X")
		}
		return i + 2
	case m.at(i, "CZ") && !m.at(i-2, "WICZ"):
		m.add("S")
		return i + 2
	case m.at(i+1, "CIA"):
		m.add("X")
		return i + 3
	case m.at(i, "CC") && !(i == 1 && m.ch(0) == 'M'):
		if m.at(i+2, "I", "E", "H") && !m.at(i+2, "HU") {
			if (i == 1 && m.ch(i-1) == 'A') || m.at(i-1, "UCCEE", "UCCES") {
				m.add("KS")
			} else {
				m.add("X")
			}
			return i + 3
		}
		m.add("K")
		return i + 2
	case m.at(i, "CK", "CG", "CQ"):
		m.add("K")
		return i + 2
	case m.at(i, "CI", "CE", "CY"):
		m.add("S")
		return i + 2
	}
	m.add("K")
	switch {
	case m.at(i+1, " C", " Q", " G"):
		return i + 3
	case m.at(i+1, "C", "K", "Q") && !m.at(i+1, "CE", "CI"):
		return i + 2
	}
	return i + 1
}

func (m *mp) stepG(i int) int {
	if m.ch(i+1) == 'H' {
		switch {
		case i > 0 && !m.vowel(i-1):
			m.add("K")
		case i == 0:
			if m.ch(i+2) == 'I' {
				m.add("J")
			} else {
				m.add("K")
			}
		case (i > 1 && m.at(i-2, "B", "H", "D")) || (i > 2 && m.at(i-3, "B", "H", "D")) || (i > 3 && m.at(i-4, "B", "H")):
		default:
			if i > 2 && m.ch(i-1) == 'U' && m.at(i-3, "C", "G", "L", "R", "T") {
				m.add("F")
			} else if i > 0 && m.ch(i-1) != 'I' {
				m.add("K")
			}
		}
		return i + 2
	}
	if m.ch(i+1) == 'N' {
		if i == 1 && m.vowel(0) && !m.slavo {
			m.add("KN")
		} else if !m.at(i+2, "EY") && m.ch(i+1) != 'Y' && !m.slavo {
			m.add("N")
		} else {
			m.add("KN")
		}
		return i + 2
	}
	switch {
	case m.at(i+1, "LI") && !m.slavo:
		m.add("KL")
		return i + 2
	case i == 0 && (m.ch(i+1) == 'Y' || m.at(i+1, "ES", "EP", "EB", "EL", "EY", "IB", "IL", "IN", "IE", "EI", "ER")):
		m.add("K")
		return i + 2
	case (m.at(i+1, "ER") || m.ch(i+1) == 'Y') && !m.at(0, "DANGER", "RANGER", "MANGER") && !m.at(i-1, "E", "I") && !m.at(i-1, "RGY", "OGY"):
		m.add("K")
		return i + 2
	case m.at(i+1, "E", "I", "Y") || m.at(i-1, "AGGI", "OGGI"):
		if m.at(0, "VAN ", "VON ", "SCH") || m.at(i+1, "ET") {
			m.add("K")
		} else {
			m.add("J")
		}
		return i + 2
	}
	m.add("K")
	return m.next(i, 'G')
}

func (m *mp) stepS(i int) int {
	switch {
	case m.at(i-1, "ISL", "YSL"):
		return i + 1
	case i == 0 && m.at(i, "SUGAR"):
		m.add("X")
		return i + 1
	case m.at(i, "SH"):
		if m.at(i+1, "HEIM", "HOEK", "HOLM", "HOLZ") {
			m.add("S")
		} else {
			m.add("X")
		}
		return i + 2
	case m.at(i, "SIO", "SIA") || m.at(i, "SIAN"):
		m.add("S")
		return i + 3
	case (i == 0 && m.at(i+1, "M", "N", "L", "W")) || m.at(i+1, "Z"):
		m.add("S")
		return m.next(i, 'Z')
	case m.at(i, "SC"):
		if m.ch(i+2) == 'H' {
			if m.at(i+3, "OO", "ER", "EN", "UY", "ED", "EM") {
				if m.at(i+3, "ER", "EN") {
					m.add("X")
				} else {
					m.add("SK")
				}
			} else {
				m.add("X")
			}
			return i + 3
		}
		if m.at(i+2, "I", "E", "Y") {
			m.add("S")
		} else {
			m.add("SK")
		}
		return i + 3
	}
	if !(i == m.last && m.at(i-2, "AI", "OI")) {
		m.add("S")
	}
	if m.at(i+1, "S", "Z") {
		return i + 2
	}
	return i + 1
}
