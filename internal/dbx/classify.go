package dbx

import (
	"strings"
	"unicode"
)

// Kind of a statement, for what a connection allows (docs/API-db.md, Safety).
type Kind string

const (
	KindRead    Kind = "read"    // SELECT, SHOW, EXPLAIN …: never changes anything
	KindWrite   Kind = "write"   // INSERT, UPDATE, DELETE, CALL …: changes rows
	KindDDL     Kind = "ddl"     // CREATE, ALTER, DROP, TRUNCATE, GRANT …: changes the schema
	KindTx      Kind = "tx"      // BEGIN, COMMIT, ROLLBACK, SAVEPOINT
	KindSession Kind = "session" // USE, SET of a harmless session setting
)

// Stmt is one statement of a script, classified.
type Stmt struct {
	SQL  string `json:"sql"`
	Kind Kind   `json:"kind"`
	Verb string `json:"verb"`
	// Danger says why it always asks, with the connection's name typed ("" when it does not):
	// DROP, TRUNCATE, DELETE or UPDATE without WHERE, ALTER … DROP.
	Danger string `json:"danger,omitempty"`
	// Table is the table a DELETE, UPDATE, TRUNCATE or DROP TABLE is about, for counting its rows.
	Table string `json:"table,omitempty"`
}

// Writes says the statement changes data or the schema.
func (s Stmt) Writes() bool { return s.Kind == KindWrite || s.Kind == KindDDL }

// dialect quirks of the splitter
type lexOpts struct {
	backtick  bool // MySQL `identifiers`
	hash      bool // MySQL # comments
	dollar    bool // PostgreSQL $tag$ strings
	backslash bool // MySQL \ escapes in strings
}

func lexFor(d Driver) lexOpts {
	if d == Postgres {
		return lexOpts{dollar: true}
	}
	return lexOpts{backtick: true, hash: true, backslash: true}
}

// Split cuts a script into statements on the semicolons outside strings, identifiers and
// comments. Empty statements are dropped.
func Split(script string, d Driver) []string {
	o := lexFor(d)
	var out []string
	start := 0
	i := 0
	n := len(script)
	for i < n {
		c := script[i]
		switch {
		case c == '\'' || c == '"' || (c == '`' && o.backtick):
			i = skipQuoted(script, i, c, o.backslash && c != '`')
		case c == '-' && i+1 < n && script[i+1] == '-':
			i = skipLine(script, i)
		case c == '#' && o.hash:
			i = skipLine(script, i)
		case c == '/' && i+1 < n && script[i+1] == '*':
			if j := strings.Index(script[i+2:], "*/"); j >= 0 {
				i += j + 4
			} else {
				i = n
			}
		case c == '$' && o.dollar:
			i = skipDollar(script, i)
		case c == ';':
			if s := strings.TrimSpace(script[start:i]); s != "" {
				out = append(out, s)
			}
			i++
			start = i
		default:
			i++
		}
	}
	if s := strings.TrimSpace(script[start:]); s != "" && !onlyComments(s, o) {
		out = append(out, s)
	}
	return out
}

func skipQuoted(s string, i int, q byte, backslash bool) int {
	i++
	for i < len(s) {
		switch {
		case backslash && s[i] == '\\':
			i += 2
		case s[i] == q:
			if i+1 < len(s) && s[i+1] == q { // '' or "" escapes the quote
				i += 2
				continue
			}
			return i + 1
		default:
			i++
		}
	}
	return len(s)
}

func skipLine(s string, i int) int {
	if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
		return i + j + 1
	}
	return len(s)
}

// skipDollar skips a PostgreSQL dollar-quoted string ($$…$$, $tag$…$tag$), or one '$'.
func skipDollar(s string, i int) int {
	j := i + 1
	for j < len(s) && (s[j] == '_' || unicode.IsLetter(rune(s[j])) || (j > i+1 && unicode.IsDigit(rune(s[j])))) {
		j++
	}
	if j >= len(s) || s[j] != '$' {
		return i + 1 // a $1 parameter or a lone $
	}
	tag := s[i : j+1]
	if k := strings.Index(s[j+1:], tag); k >= 0 {
		return j + 1 + k + len(tag)
	}
	return len(s)
}

func onlyComments(s string, o lexOpts) bool {
	return len(words(s, o)) == 0
}

// word is a token outside strings and comments, with the parenthesis depth it sits at.
type word struct {
	text  string // upper-cased for keywords; as written for identifiers
	raw   string
	depth int
	pos   int // byte offset in the statement
}

// words gives the bare words and quoted identifiers of a statement, outside strings and
// comments; quoted identifiers keep their quotes off.
func words(s string, o lexOpts) []word {
	var out []word
	depth := 0
	i := 0
	n := len(s)
	for i < n {
		c := s[i]
		switch {
		case c == '\'':
			i = skipQuoted(s, i, c, o.backslash)
		case c == '"' || (c == '`' && o.backtick):
			j := skipQuoted(s, i, c, false)
			id := strings.Trim(s[i:j], string(c))
			out = append(out, word{text: id, raw: id, depth: depth, pos: i})
			i = j
		case c == '-' && i+1 < n && s[i+1] == '-':
			i = skipLine(s, i)
		case c == '#' && o.hash:
			i = skipLine(s, i)
		case c == '/' && i+1 < n && s[i+1] == '*':
			if j := strings.Index(s[i+2:], "*/"); j >= 0 {
				i += j + 4
			} else {
				i = n
			}
		case c == '$' && o.dollar:
			i = skipDollar(s, i)
		case c == '(':
			depth++
			i++
		case c == ')':
			if depth > 0 {
				depth--
			}
			i++
		case c == '_' || c == '@' || unicode.IsLetter(rune(c)) || c >= 0x80:
			j := i
			for j < n && (s[j] == '_' || s[j] == '.' || s[j] == '$' || s[j] == '@' || unicode.IsLetter(rune(s[j])) || unicode.IsDigit(rune(s[j])) || s[j] >= 0x80) {
				j++
			}
			out = append(out, word{text: strings.ToUpper(s[i:j]), raw: s[i:j], depth: depth, pos: i})
			i = j
		default:
			i++
		}
	}
	return out
}

var (
	readVerbs  = set("SELECT", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "TABLE", "VALUES", "HELP", "CHECKSUM")
	writeVerbs = set("INSERT", "UPDATE", "DELETE", "REPLACE", "MERGE", "UPSERT", "LOAD", "CALL", "DO", "COPY", "HANDLER", "LOCK", "UNLOCK", "VACUUM", "ANALYZE", "OPTIMIZE", "REINDEX", "CLUSTER", "REFRESH", "NOTIFY", "LISTEN", "PREPARE", "EXECUTE", "DEALLOCATE", "IMPORT", "INSTALL", "UNINSTALL", "KILL", "FLUSH", "PURGE", "RESET", "DISCARD", "SECURITY", "REASSIGN", "CHECKPOINT")
	ddlVerbs   = set("CREATE", "ALTER", "DROP", "TRUNCATE", "RENAME", "GRANT", "REVOKE", "COMMENT")
	txVerbs    = set("BEGIN", "START", "COMMIT", "ROLLBACK", "SAVEPOINT", "RELEASE", "END", "ABORT")
	// SET of these only changes how this session reads; any other SET could undo read-only
	safeSets = set("NAMES", "CHARACTER", "CHARSET", "SEARCH_PATH", "TIME", "TIMEZONE", "TIME_ZONE", "DATESTYLE", "STATEMENT_TIMEOUT", "LOCK_TIMEOUT", "CLIENT_ENCODING", "APPLICATION_NAME", "MAX_EXECUTION_TIME", "SQL_SELECT_LIMIT")
)

func set(v ...string) map[string]bool {
	m := make(map[string]bool, len(v))
	for _, s := range v {
		m[s] = true
	}
	return m
}

// Classify says what a statement does.
func Classify(sql string, d Driver) Stmt {
	o := lexFor(d)
	ws := words(sql, o)
	st := Stmt{SQL: sql, Kind: KindWrite}
	if len(ws) == 0 {
		st.Kind = KindRead
		return st
	}
	verb := ws[0].text
	st.Verb = verb
	has := func(from int, kw ...string) int {
		for j := from; j < len(ws); j++ {
			for _, k := range kw {
				if ws[j].depth == ws[0].depth && ws[j].text == k {
					return j
				}
			}
		}
		return -1
	}
	anyWord := func(kw ...string) bool {
		for _, w := range ws[1:] {
			for _, k := range kw {
				if w.text == k {
					return true
				}
			}
		}
		return false
	}
	switch {
	case verb == "EXPLAIN":
		st.Kind = KindRead
		// EXPLAIN ANALYZE (or EXPLAIN (ANALYZE, …)) runs the statement: it is what that one is
		analyze := false
		for k := 1; k < len(ws) && k < 10; k++ {
			if ws[k].text == "ANALYZE" || ws[k].text == "ANALYSE" {
				analyze = true
			}
		}
		if analyze {
			for k := 1; k < len(ws); k++ {
				w := ws[k].text
				if ws[k].depth == 0 && w != "ANALYZE" && w != "ANALYSE" && w != "VERBOSE" && w != "FORMAT" && (readVerbs[w] || writeVerbs[w] || w == "WITH") {
					inner := Classify(sql[ws[k].pos:], d)
					if inner.Kind != KindRead {
						inner.SQL, inner.Verb = sql, "EXPLAIN "+inner.Verb
						return inner
					}
					break
				}
			}
		}
	case verb == "SELECT":
		st.Kind = KindRead
		// SELECT … INTO a table (PostgreSQL) or a file (MySQL) writes; INTO @var does not
		if j := has(1, "INTO"); j >= 0 && j+1 < len(ws) && !userVar(ws[j+1].raw) {
			st.Kind = KindWrite
		}
		if anyWord("FOR") && has(1, "UPDATE", "SHARE") >= 0 {
			st.Kind = KindWrite // takes row locks
		}
	case verb == "WITH":
		st.Kind = KindRead
		// a data-modifying CTE or a WITH … DELETE/UPDATE/INSERT main statement
		for _, w := range ws[1:] {
			if w.text == "INSERT" || w.text == "UPDATE" || w.text == "DELETE" || w.text == "MERGE" {
				st.Kind = KindWrite
				st.Verb = w.text
				if (w.text == "DELETE" || w.text == "UPDATE") && w.depth == ws[0].depth && has(1, "WHERE") < 0 {
					st.Danger = w.text + " without WHERE changes every row"
				}
				break
			}
		}
	case readVerbs[verb]:
		st.Kind = KindRead
	case txVerbs[verb]:
		st.Kind = KindTx
		if verb == "START" && (len(ws) < 2 || ws[1].text != "TRANSACTION") {
			st.Kind = KindWrite // START SLAVE, START REPLICA …
		}
	case verb == "USE":
		st.Kind = KindSession
	case verb == "SET":
		st.Kind = KindSession
		if len(ws) > 1 {
			name := ws[1].text
			if name == "SESSION" || name == "LOCAL" || name == "@@SESSION" {
				if len(ws) > 2 {
					name = ws[2].text
				}
			}
			name = strings.TrimPrefix(strings.TrimPrefix(name, "@@SESSION."), "@@")
			if !safeSets[name] && !userVar(ws[1].raw) {
				st.Kind = KindWrite
			}
		}
	case ddlVerbs[verb]:
		st.Kind = KindDDL
		switch verb {
		case "DROP":
			st.Danger = "DROP removes " + strings.ToLower(object(ws)) + " and everything in it"
			if len(ws) > 2 && ws[1].text == "TABLE" {
				st.Table = tableAt(ws, 2)
			}
		case "TRUNCATE":
			st.Danger = "TRUNCATE empties the table"
			j := 1
			if len(ws) > 1 && ws[1].text == "TABLE" {
				j = 2
			}
			st.Table = tableAt(ws, j)
		case "ALTER":
			if has(1, "DROP") >= 0 {
				st.Danger = "ALTER … DROP removes a column, an index or a constraint"
			}
		}
	case verb == "DELETE":
		st.Kind = KindWrite
		j := has(1, "FROM")
		if j >= 0 {
			st.Table = tableAt(ws, j+1)
		} else {
			st.Table = tableAt(ws, 1)
		}
		if has(1, "WHERE") < 0 {
			st.Danger = "DELETE without WHERE removes every row"
		}
	case verb == "UPDATE":
		st.Kind = KindWrite
		st.Table = tableAt(ws, 1)
		if has(1, "WHERE") < 0 {
			st.Danger = "UPDATE without WHERE changes every row"
		}
	case writeVerbs[verb]:
		st.Kind = KindWrite
		if verb == "COPY" && has(1, "TO") >= 0 && has(1, "FROM") < 0 {
			st.Kind = KindRead // COPY … TO STDOUT
		}
		if verb == "ANALYZE" && d == MySQL {
			st.Kind = KindRead // ANALYZE TABLE only refreshes statistics
		}
	}
	return st
}

// userVar says a word is a MySQL user variable (@x), not a system one (@@x).
func userVar(w string) bool { return strings.HasPrefix(w, "@") && !strings.HasPrefix(w, "@@") }

func object(ws []word) string {
	if len(ws) > 1 {
		if ws[1].text == "TEMPORARY" && len(ws) > 2 {
			return "the temporary " + ws[2].raw
		}
		return "the " + ws[1].raw
	}
	return "it"
}

// tableAt gives the table name at ws[j], skipping IF EXISTS, ONLY, LOW_PRIORITY and the like.
func tableAt(ws []word, j int) string {
	for j < len(ws) {
		switch ws[j].text {
		case "IF", "EXISTS", "ONLY", "LOW_PRIORITY", "QUICK", "IGNORE", "FROM", "TABLE":
			j++
			continue
		}
		return ws[j].raw
	}
	return ""
}
