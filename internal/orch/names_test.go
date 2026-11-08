package orch

import "testing"

func TestSimilarKnowsIndonesianSpellings(t *testing.T) {
	for typed, name := range map[string]string{
		"nopal":   "naufal",  // f → p, au → o; too far apart by edit distance
		"raihan":  "reyhan",  // the same name, spelled two ways
		"rehan":   "reyhan",  //
		"hekal":   "haikal",  //
		"sipa":    "syifa",   // sy → s, f → p
		"djihan":  "jihan",   // old spelling
		"chantik": "cantik",  //
		"arkhan":  "arkan",   //
		"dafa":    "daffa",   //
		"aisya":   "aisyah",  //
		"elfan":   "elvan",   //
		"fadhil":  "fadil",   //
		"sahra":   "zahra",   //
		"moelyan": "mulyan",  //
		"maulana": "molana",  //
		"tjahaya": "cahaya",  //
		"yasmien": "yasmien", // equal: nothing to suggest
	} {
		want := typed != name
		if got := Similar(typed, name); got != want {
			t.Errorf("Similar(%q, %q) = %v, want %v", typed, name, got, want)
		}
	}
}

// Folding must not make other names look the same: each hit asks the user who they meant.
func TestSimilarKeepsDifferentNamesApart(t *testing.T) {
	for _, pair := range [][2]string{
		{"nopal", "pandu"}, {"tiara", "vania"}, {"luna", "maura"}, {"arkan", "rafa"},
		{"gibran", "ilham"}, {"salsa", "wildan"}, {"kayla", "olivia"}, {"bintang", "cantika"},
	} {
		if Similar(pair[0], pair[1]) {
			t.Errorf("Similar(%q, %q)", pair[0], pair[1])
		}
	}
}

// The default handles (session.handleNames) need telling apart: a typed name
// that folds onto two of them could not say who was meant.
func TestDefaultHandlesFoldApart(t *testing.T) {
	seen := map[string]string{}
	for _, h := range []string{"Arkan", "Bintang", "Cantika", "Daffa", "Elvan", "Farel", "Gibran", "Haikal", "Ilham", "Jihan", "Kayla", "Luna",
		"Maura", "Naufal", "Olivia", "Pandu", "Rafa", "Reyhan", "Salsa", "Tiara", "Vania", "Wildan", "Yasmin", "Zahra", "Aruna"} {
		k := indonesianKey(Norm(h))
		if other, ok := seen[k]; ok {
			t.Errorf("%s and %s both fold to %q", h, other, k)
		}
		seen[k] = h
	}
}
