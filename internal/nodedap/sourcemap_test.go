package nodedap

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

// testdata/ts was compiled by tsc 6 (tsconfig.json there): src/greet.ts → dist/greet.js + .map.
func tsFixture(t *testing.T) (script, source string) {
	t.Helper()
	dir, err := filepath.Abs("testdata/ts")
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "dist", "greet.js"), filepath.Join(dir, "src", "greet.ts")
}

func TestSourceMapBothWays(t *testing.T) {
	script, source := tsFixture(t)
	sm, err := loadSourceMap(script, "greet.js.map")
	if err != nil {
		t.Fatal(err)
	}
	if !sm.has(source) {
		t.Fatalf("sources %v", sm.sources)
	}
	// 1-based: greet.ts 7 `const line = …` is greet.js 5, at column 5
	cases := []struct{ srcLine, genLine, genCol int }{
		{7, 5, 5},
		{8, 6, 5},
		{11, 8, 1},
		{13, 10, 1},
	}
	for _, c := range cases {
		gl, gc, landed, ok := sm.generated(source, c.srcLine-1)
		if !ok || gl+1 != c.genLine || gc+1 != c.genCol || landed+1 != c.srcLine {
			t.Errorf("ts %d → js %d:%d (%v, landed %d), want %d:%d", c.srcLine, gl+1, gc+1, ok, landed+1, c.genLine, c.genCol)
		}
		src, l, _, ok := sm.original(c.genLine-1, c.genCol-1)
		if !ok || src != source || l+1 != c.srcLine {
			t.Errorf("js %d:%d → %s %d, want ts %d", c.genLine, c.genCol, src, l+1, c.srcLine)
		}
	}
	// a line without code of its own (the interface) takes the next that has some
	if gl, _, landed, ok := sm.generated(source, 1); !ok || landed+1 != 6 || gl+1 != 3 {
		t.Errorf("interface line: js %d, landed ts %d (%v)", gl+1, landed+1, ok)
	}
	// a place in the script before any mapping on its line takes the line's first one
	if src, l, _, ok := sm.original(4, 0); !ok || src != source || l+1 != 7 {
		t.Errorf("js 5:1 → %s %d", src, l+1)
	}
	if _, _, _, ok := sm.original(0, 0); ok {
		t.Error("the use strict line maps to nothing")
	}
}

func TestSourceMapInline(t *testing.T) {
	script, source := tsFixture(t)
	raw, err := os.ReadFile(script + ".map")
	if err != nil {
		t.Fatal(err)
	}
	sm, err := loadSourceMap(script, "data:application/json;charset=utf-8;base64,"+base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	if gl, _, _, ok := sm.generated(source, 6); !ok || gl+1 != 5 {
		t.Fatalf("inline map: %d %v", gl+1, ok)
	}
}

func TestBadSourceMaps(t *testing.T) {
	for _, raw := range []string{
		`{"version":2,"sources":["a.ts"],"mappings":"AAAA"}`,
		`{"version":3,"sources":["a.ts"],"mappings":"AAAA!"}`,
		`{"version":3,"sources":["a.ts"],"mappings":"ACAA"}`,  // a source that is not there
		`{"version":3,"sources":["a.ts"],"mappings":"AAAAg"}`, // ends inside a number
		`not json`,
	} {
		if _, err := parseSourceMap([]byte(raw), "/x"); err == nil {
			t.Errorf("%s: no error", raw)
		}
	}
	sm, err := parseSourceMap([]byte(`{"version":3,"sourceRoot":"/src/","sources":["a.ts","webpack://app/b.ts"],"mappings":"AAAA,CCAA"}`), "/out")
	if err != nil {
		t.Fatal(err)
	}
	if sm.sources[0] != "/src/a.ts" || sm.sources[1] != "webpack://app/b.ts" {
		t.Fatalf("%v", sm.sources)
	}
}
