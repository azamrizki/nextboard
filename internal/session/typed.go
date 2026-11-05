package session

import (
	"bytes"
	"regexp"
	"strings"
)

// terminalReport matches what a terminal emulator sends by itself: focus
// events, mouse reports and answers to queries from the program it shows.
// Keys (arrows, function keys, bracketed paste) never match, except that a
// cursor position report looks like Ctrl/Shift+F3; losing that key beats
// typing the report into another program.
var terminalReport = regexp.MustCompile(`(?s)` + strings.Join([]string{
	`\x1b\[[IO]`,                      // focus in, focus out
	`\x1b\[<\d+;\d+;\d+[Mm]`,          // SGR mouse
	`\x1b\[M...`,                      // X10 mouse
	`\x1b\[\??\d+;\d+R`,               // cursor position
	`\x1b\[[?>=][\d;]*c`,              // device attributes
	`\x1b\[\??\d*n`,                   // device status
	`\x1b\[\??[\d;]*\$y`,              // mode report
	`\x1b\[\?\d*u`,                    // keyboard protocol flags
	`\x1b\[\d+(;\d+)*t`,               // window reports
	`\x1b\][^\x07\x1b]*(\x07|\x1b\\)`, // OSC answers (colors, clipboard)
	`\x1b[P_^][^\x1b]*\x1b\\`,         // DCS, APC, PM answers
}, "|"))

// TypedInput is client input without what the terminal emulator produced on
// its own. Those bytes answer one program on one screen; they must not reach
// the other consoles of a linked group.
func TypedInput(p []byte) []byte {
	if bytes.IndexByte(p, 0x1b) < 0 {
		return p
	}
	return terminalReport.ReplaceAll(p, nil)
}
