package session

import "testing"

func TestTypedInput(t *testing.T) {
	keep := []string{
		"a", "hello\r", "\x03", "\x1b", "\x1b[A", "\x1b[1;5C", "\x1b[3~", "\x1b[15~", "\x1bOP", "\x1b[Z",
		"\x1b[97;5u", "\x1b[200~pasted\x1b[201~", "é日本語",
	}
	for _, s := range keep {
		if got := string(TypedInput([]byte(s))); got != s {
			t.Errorf("key %q became %q", s, got)
		}
	}
	drop := []string{
		"\x1b[I", "\x1b[O", "\x1b[<0;12;5M", "\x1b[<0;12;5m", "\x1b[M !!", "\x1b[12;40R", "\x1b[?6;1R",
		"\x1b[?1;2c", "\x1b[>0;276;0c", "\x1b[0n", "\x1b[?2004;1$y", "\x1b[?0u", "\x1b[8;24;80t",
		"\x1b]11;rgb:0b0b/1010/2020\x07", "\x1b]10;rgb:e6e6/eaea/f2f2\x1b\\", "\x1bP1$r0m\x1b\\", "\x1b_Gi=1;OK\x1b\\",
	}
	for _, s := range drop {
		if got := TypedInput([]byte(s)); len(got) != 0 {
			t.Errorf("report %q left %q", s, got)
		}
	}
	if got := string(TypedInput([]byte("a\x1b[Ib\x1b[12;40Rc"))); got != "abc" {
		t.Errorf("mixed chunk: %q", got)
	}
}
