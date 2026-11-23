package dbx

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type kvEngine struct {
	c   Connection
	rc  *redis.Client
	ver string
}

func newKVEngine(ctx context.Context, c Connection, password string, dial DialFunc) (*kvEngine, error) {
	timeout := time.Duration(c.Reach.TimeoutSec) * time.Second
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	opt := &redis.Options{
		Addr:         net.JoinHostPort(c.Host, strconv.Itoa(c.Port)),
		Username:     c.User,
		Password:     password,
		DB:           c.RedisDB,
		Dialer:       func(ctx context.Context, network, addr string) (net.Conn, error) { return dial(ctx, "tcp", addr) },
		DialTimeout:  timeout,
		ReadTimeout:  time.Duration(c.Limits.TimeoutSec) * time.Second,
		WriteTimeout: 30 * time.Second,
		PoolSize:     4,
		ClientName:   "NextBoard",
		Protocol:     2,
	}
	if c.TLS != TLSOff {
		opt.TLSConfig = &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: c.TLS != TLSVerifyCA}
	}
	rc := redis.NewClient(opt)
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := rc.Ping(pctx).Err(); err != nil {
		rc.Close()
		return nil, err
	}
	e := &kvEngine{c: c, rc: rc}
	if info, err := rc.Info(pctx, "server").Result(); err == nil {
		kv := parseInfo(info)
		switch {
		case kv["valkey_version"] != "":
			e.ver = "Valkey " + kv["valkey_version"]
		case kv["redis_version"] != "":
			e.ver = "Redis " + kv["redis_version"]
		}
	}
	return e, nil
}

func (e *kvEngine) close() { e.rc.Close() }

func parseInfo(s string) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || l[0] == '#' {
			continue
		}
		if k, v, ok := strings.Cut(l, ":"); ok {
			out[k] = v
		}
	}
	return out
}

// KVInfo is what the side panel shows of a key-value server.
type KVInfo struct {
	Version string `json:"version"`
	DB      int    `json:"db"`
	// Keys per database index, for the ones that have any
	DBs     map[int]int64 `json:"dbs"`
	Memory  string        `json:"memory"`
	Clients int64         `json:"clients"`
	Mode    string        `json:"mode,omitempty"`
	SlowLog int64         `json:"slowLog"`
}

func (e *kvEngine) Info(ctx context.Context) (*KVInfo, error) {
	s, err := e.rc.Info(ctx, "all").Result()
	if err != nil {
		// a server (or an ACL) that refuses INFO all: what it gives, else the database's size
		if s, err = e.rc.Info(ctx).Result(); err != nil {
			n, err := e.rc.DBSize(ctx).Result()
			if err != nil {
				return nil, err
			}
			return &KVInfo{Version: e.ver, DB: e.c.RedisDB, DBs: map[int]int64{e.c.RedisDB: n}}, nil
		}
	}
	kv := parseInfo(s)
	out := &KVInfo{Version: e.ver, DB: e.c.RedisDB, DBs: map[int]int64{}, Memory: kv["used_memory_human"], Mode: kv["redis_mode"]}
	out.Clients, _ = strconv.ParseInt(kv["connected_clients"], 10, 64)
	for k, v := range kv {
		if !strings.HasPrefix(k, "db") {
			continue
		}
		n, err := strconv.Atoi(k[2:])
		if err != nil {
			continue
		}
		for _, part := range strings.Split(v, ",") {
			if key, val, ok := strings.Cut(part, "="); ok && key == "keys" {
				out.DBs[n], _ = strconv.ParseInt(val, 10, 64)
			}
		}
	}
	out.SlowLog, _ = e.rc.Do(ctx, "SLOWLOG", "LEN").Int64()
	return out, nil
}

// KeyItem is a key in a listing.
type KeyItem struct {
	Name string `json:"name"`
	Type string `json:"type"`
	TTL  int64  `json:"ttl"` // ms; -1 none, -2 gone
}

// KeyPage is a page of SCAN.
type KeyPage struct {
	Keys   []KeyItem `json:"keys"`
	Cursor uint64    `json:"cursor"` // 0: the end
	Total  int64     `json:"total"`  // DBSIZE
}

// Scan lists keys matching a pattern, about count at a time, with their type and TTL.
func (e *kvEngine) Scan(ctx context.Context, db int, cursor uint64, match, typ string, count int64) (*KeyPage, error) {
	if count <= 0 || count > 5000 {
		count = 500
	}
	if match == "" {
		match = "*"
	}
	c := e.rc.Conn()
	defer c.Close()
	if err := c.Select(ctx, db).Err(); err != nil {
		return nil, err
	}
	var keys []string
	var next uint64
	var err error
	if typ != "" {
		keys, next, err = c.ScanType(ctx, cursor, match, count, typ).Result()
	} else {
		keys, next, err = c.Scan(ctx, cursor, match, count).Result()
	}
	if err != nil {
		return nil, err
	}
	sort.Strings(keys)
	out := &KeyPage{Cursor: next, Keys: make([]KeyItem, 0, len(keys))}
	pipe := c.Pipeline()
	types := make([]*redis.StatusCmd, len(keys))
	ttls := make([]*redis.DurationCmd, len(keys))
	for i, k := range keys {
		types[i] = pipe.Type(ctx, k)
		ttls[i] = pipe.PTTL(ctx, k)
	}
	_, _ = pipe.Exec(ctx)
	for i, k := range keys {
		it := KeyItem{Name: k, Type: types[i].Val(), TTL: -1}
		if d := ttls[i].Val(); d > 0 {
			it.TTL = d.Milliseconds()
		} else if d == -2 {
			it.TTL = -2
		}
		out.Keys = append(out.Keys, it)
	}
	out.Total, _ = c.DBSize(ctx).Result()
	return out, nil
}

// KeyValue is one key and a page of its value.
type KeyValue struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	TTL      int64  `json:"ttl"`
	Size     int64  `json:"size"`
	Encoding string `json:"encoding"`
	Length   int64  `json:"length"`
	// string
	Value any `json:"value,omitempty"`
	// hash: [field, value]; zset: [member, score]; list, set: [item]
	Items [][]any `json:"items,omitempty"`
	// stream entries
	Entries []map[string]any `json:"entries,omitempty"`
	Next    string           `json:"next,omitempty"` // cursor or offset of the next page, "" at the end
}

// Key reads a key: its type, TTL, size and a page of its value (page: a cursor or offset).
func (e *kvEngine) Key(ctx context.Context, db int, name, page string) (*KeyValue, error) {
	c := e.rc.Conn()
	defer c.Close()
	if err := c.Select(ctx, db).Err(); err != nil {
		return nil, err
	}
	typ, err := c.Type(ctx, name).Result()
	if err != nil {
		return nil, err
	}
	if typ == "none" {
		return nil, fmt.Errorf("%w: no key %q", ErrNotFound, name)
	}
	kv := &KeyValue{Name: name, Type: typ, TTL: -1}
	if d, err := c.PTTL(ctx, name).Result(); err == nil && d > 0 {
		kv.TTL = d.Milliseconds()
	}
	kv.Size, _ = c.MemoryUsage(ctx, name).Result()
	kv.Encoding, _ = c.ObjectEncoding(ctx, name).Result()
	const n = 200
	cur, _ := strconv.ParseUint(page, 10, 64)
	next := func(c uint64) string {
		if c == 0 {
			return ""
		}
		return strconv.FormatUint(c, 10)
	}
	switch typ {
	case "string":
		kv.Length, _ = c.StrLen(ctx, name).Result()
		v, err := c.GetRange(ctx, name, 0, CellMax-1).Result()
		if err != nil {
			return nil, err
		}
		kv.Value = cell([]byte(v), "")
		if kv.Length > CellMax {
			kv.Value = map[string]any{"$cut": v, "len": kv.Length}
		}
	case "hash":
		kv.Length, _ = c.HLen(ctx, name).Result()
		vals, nc, err := c.HScan(ctx, name, cur, "*", n).Result()
		if err != nil {
			return nil, err
		}
		for i := 0; i+1 < len(vals); i += 2 {
			kv.Items = append(kv.Items, []any{cell([]byte(vals[i]), ""), cell([]byte(vals[i+1]), "")})
		}
		kv.Next = next(nc)
	case "list":
		kv.Length, _ = c.LLen(ctx, name).Result()
		start := int64(cur)
		vals, err := c.LRange(ctx, name, start, start+n-1).Result()
		if err != nil {
			return nil, err
		}
		for _, v := range vals {
			kv.Items = append(kv.Items, []any{cell([]byte(v), "")})
		}
		if start+n < kv.Length {
			kv.Next = strconv.FormatInt(start+n, 10)
		}
	case "set":
		kv.Length, _ = c.SCard(ctx, name).Result()
		vals, nc, err := c.SScan(ctx, name, cur, "*", n).Result()
		if err != nil {
			return nil, err
		}
		for _, v := range vals {
			kv.Items = append(kv.Items, []any{cell([]byte(v), "")})
		}
		kv.Next = next(nc)
	case "zset":
		kv.Length, _ = c.ZCard(ctx, name).Result()
		start := int64(cur)
		vals, err := c.ZRangeWithScores(ctx, name, start, start+n-1).Result()
		if err != nil {
			return nil, err
		}
		for _, z := range vals {
			kv.Items = append(kv.Items, []any{cell([]byte(fmt.Sprint(z.Member)), ""), z.Score})
		}
		if start+n < kv.Length {
			kv.Next = strconv.FormatInt(start+n, 10)
		}
	case "stream":
		kv.Length, _ = c.XLen(ctx, name).Result()
		from := "-"
		if page != "" {
			from = "(" + page
		}
		msgs, err := c.XRangeN(ctx, name, from, "+", n).Result()
		if err != nil {
			return nil, err
		}
		for _, m := range msgs {
			ent := map[string]any{"id": m.ID}
			f := map[string]any{}
			for k, v := range m.Values {
				f[k] = cell([]byte(fmt.Sprint(v)), "")
			}
			ent["fields"] = f
			kv.Entries = append(kv.Entries, ent)
		}
		if len(msgs) == n {
			kv.Next = msgs[len(msgs)-1].ID
		}
	default:
		kv.Value = "this type is shown by the console: " + typ
	}
	return kv, nil
}

// ---- commands --------------------------------------------------------------------------------

var redisReads = set("GET", "MGET", "STRLEN", "GETRANGE", "SUBSTR", "EXISTS", "TYPE", "TTL", "PTTL", "EXPIRETIME", "PEXPIRETIME",
	"HGET", "HMGET", "HGETALL", "HKEYS", "HVALS", "HLEN", "HEXISTS", "HSCAN", "HSTRLEN", "HRANDFIELD",
	"LRANGE", "LLEN", "LINDEX", "LPOS", "SMEMBERS", "SISMEMBER", "SMISMEMBER", "SCARD", "SSCAN", "SRANDMEMBER", "SINTER", "SUNION", "SDIFF", "SINTERCARD",
	"ZRANGE", "ZRANGEBYSCORE", "ZREVRANGE", "ZREVRANGEBYSCORE", "ZRANGEBYLEX", "ZREVRANGEBYLEX", "ZCARD", "ZSCORE", "ZMSCORE", "ZRANK", "ZREVRANK", "ZCOUNT", "ZLEXCOUNT", "ZSCAN", "ZRANDMEMBER",
	"XRANGE", "XREVRANGE", "XLEN", "XINFO", "XPENDING", "SCAN", "DBSIZE", "INFO", "PING", "ECHO", "TIME", "OBJECT", "MEMORY", "RANDOMKEY",
	"SLOWLOG", "LASTSAVE", "ROLE", "COMMAND", "BITCOUNT", "GETBIT", "BITPOS", "PFCOUNT", "GEOPOS", "GEODIST", "GEOSEARCH", "GEOHASH", "GEORADIUS_RO", "GEORADIUSBYMEMBER_RO",
	"JSON.GET", "JSON.MGET", "JSON.TYPE", "JSON.STRLEN", "JSON.OBJKEYS", "JSON.OBJLEN", "JSON.ARRLEN", "FT.SEARCH", "FT.INFO", "FT._LIST", "TS.GET", "TS.RANGE", "TS.INFO", "LOLWUT", "SELECT", "CLIENT", "CONFIG", "HELLO", "LATENCY")

// never run from NextBoard: they block, stop or replace the server
var redisRefused = set("SHUTDOWN", "DEBUG", "MONITOR", "SUBSCRIBE", "PSUBSCRIBE", "SSUBSCRIBE", "SYNC", "PSYNC", "REPLICAOF", "SLAVEOF", "FAILOVER", "MIGRATE", "BLPOP", "BRPOP", "BLMOVE", "BRPOPLPUSH", "BZPOPMIN", "BZPOPMAX", "BLMPOP", "BZMPOP", "WAIT", "WAITAOF", "KEYS")

// RedisStmt classifies a command line.
func RedisStmt(args []string) (Stmt, string) {
	if len(args) == 0 {
		return Stmt{Kind: KindRead}, ""
	}
	cmd := strings.ToUpper(args[0])
	st := Stmt{SQL: strings.Join(args, " "), Verb: cmd, Kind: KindWrite}
	sub := ""
	if len(args) > 1 {
		sub = strings.ToUpper(args[1])
	}
	switch {
	case cmd == "KEYS":
		return st, "KEYS blocks the server on a big keyspace: the key list uses SCAN instead"
	case redisRefused[cmd]:
		return st, cmd + " is not run from NextBoard: it blocks or stops the server"
	case cmd == "CLUSTER" && (sub == "RESET" || sub == "FAILOVER" || sub == "FORGET"):
		return st, "CLUSTER " + sub + " is not run from NextBoard"
	case cmd == "XREAD" || cmd == "XREADGROUP":
		for _, a := range args {
			if strings.EqualFold(a, "BLOCK") {
				return st, cmd + " BLOCK waits forever: leave BLOCK out"
			}
		}
		if cmd == "XREAD" {
			st.Kind = KindRead
		}
	case cmd == "CONFIG":
		if sub == "GET" {
			st.Kind = KindRead
		}
	case cmd == "CLIENT":
		if sub == "LIST" || sub == "INFO" || sub == "GETNAME" || sub == "ID" {
			st.Kind = KindRead
		}
	case cmd == "MEMORY" || cmd == "OBJECT" || cmd == "SLOWLOG" || cmd == "LATENCY":
		st.Kind = KindRead
		if sub == "RESET" || sub == "PURGE" {
			st.Kind = KindWrite
		}
	case cmd == "SELECT" || cmd == "HELLO":
		st.Kind = KindSession
	case redisReads[cmd]:
		st.Kind = KindRead
	}
	switch cmd {
	case "FLUSHDB", "FLUSHALL":
		st.Danger = cmd + " removes every key" + map[bool]string{true: " of every database", false: " of this database"}[cmd == "FLUSHALL"]
	case "SWAPDB":
		st.Danger = "SWAPDB swaps two whole databases"
	case "SCRIPT", "FUNCTION":
		if sub == "FLUSH" {
			st.Danger = cmd + " FLUSH removes every loaded " + strings.ToLower(cmd)
		}
	case "UNLINK", "DEL":
		if len(args) > 11 {
			st.Danger = fmt.Sprintf("%s of %d keys at once", cmd, len(args)-1)
		}
	}
	return st, ""
}

// SplitArgs cuts a command line like redis-cli: spaces separate, quotes keep, \ escapes in "".
func SplitArgs(line string) ([]string, error) {
	var out []string
	var b strings.Builder
	in := byte(0)
	have := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case in == 0 && (c == ' ' || c == '\t'):
			if have {
				out = append(out, b.String())
				b.Reset()
				have = false
			}
		case in == 0 && (c == '"' || c == '\''):
			in, have = c, true
		case in != 0 && c == in:
			in = 0
		case in == '"' && c == '\\' && i+1 < len(line):
			i++
			switch line[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(line[i])
			}
		default:
			b.WriteByte(c)
			have = true
		}
	}
	if in != 0 {
		return nil, errors.New("a quote is not closed")
	}
	if have {
		out = append(out, b.String())
	}
	return out, nil
}

// CommandOpts is a console command.
type CommandOpts struct {
	DB          int    `json:"db"`
	Line        string `json:"line"`
	Confirmed   bool   `json:"confirmed"`
	ConfirmName string `json:"confirmName"`
	ReadOnly    bool   `json:"-"`
	NoConfirm   bool   `json:"-"`
}

// Command runs one console line and gives its reply as redis-cli would print it.
func (e *kvEngine) Command(ctx context.Context, o CommandOpts) (string, error) {
	args, err := SplitArgs(o.Line)
	if err != nil {
		return "", invalid("%s", err.Error())
	}
	if len(args) == 0 {
		return "", invalid("nothing to run")
	}
	st, refused := RedisStmt(args)
	if refused != "" {
		return "", invalid("%s", refused)
	}
	if (e.c.Security.ReadOnly || o.ReadOnly) && st.Kind == KindWrite {
		return "", &ReadOnlyError{Stmt: st}
	}
	if st.Danger != "" && o.ConfirmName != e.c.Name {
		return "", &ConfirmError{Code: "confirm_name", Stmts: []Stmt{st}, Rows: []int64{-1}}
	}
	if st.Kind == KindWrite && st.Danger == "" && e.c.Security.ConfirmWrites && !o.Confirmed && !o.NoConfirm {
		return "", &ConfirmError{Code: "confirm", Stmts: []Stmt{st}}
	}
	c := e.rc.Conn()
	defer c.Close()
	if err := c.Select(ctx, o.DB).Err(); err != nil {
		return "", err
	}
	if st.Verb == "SELECT" {
		return "OK (the database is chosen in the side panel)", nil
	}
	ia := make([]any, len(args))
	for i, a := range args {
		ia[i] = a
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(e.c.Limits.TimeoutSec)*time.Second)
	defer cancel()
	v, err := c.Do(ctx, ia...).Result()
	if errors.Is(err, redis.Nil) {
		return "(nil)", nil
	}
	if err != nil {
		return "", err
	}
	var b strings.Builder
	printReply(&b, v, "")
	out := b.String()
	if len(out) > 1<<20 {
		out = out[:1<<20] + "\n… (cut at 1 MB)"
	}
	return strings.TrimRight(out, "\n"), nil
}

func printReply(b *strings.Builder, v any, indent string) {
	switch x := v.(type) {
	case nil:
		b.WriteString("(nil)\n")
	case string:
		b.WriteString(strconv.Quote(x) + "\n")
	case int64:
		fmt.Fprintf(b, "(integer) %d\n", x)
	case float64:
		fmt.Fprintf(b, "(double) %v\n", x)
	case bool:
		fmt.Fprintf(b, "(boolean) %v\n", x)
	case []any:
		if len(x) == 0 {
			b.WriteString("(empty array)\n")
			return
		}
		w := len(strconv.Itoa(len(x)))
		for i, it := range x {
			if i > 0 {
				b.WriteString(indent)
			}
			fmt.Fprintf(b, "%*d) ", w, i+1)
			printReply(b, it, indent+strings.Repeat(" ", w+2))
		}
	case map[any]any:
		i := 0
		for k, val := range x {
			if i > 0 {
				b.WriteString(indent)
			}
			fmt.Fprintf(b, "%v => ", k)
			printReply(b, val, indent+"  ")
			i++
		}
	default:
		fmt.Fprintf(b, "%v\n", x)
	}
}

// KeyWrite is an edit of a key from the value view.
type KeyWrite struct {
	DB        int     `json:"db"`
	Key       string  `json:"key"`
	Op        string  `json:"op"` // set, hset, hdel, lset, rpush, lrem, sadd, srem, zadd, zrem, xadd, xdel, rename, expire, persist, del
	Field     string  `json:"field,omitempty"`
	Value     string  `json:"value,omitempty"`
	Score     float64 `json:"score,omitempty"`
	Index     int64   `json:"index,omitempty"`
	TTL       int64   `json:"ttl,omitempty"` // seconds
	To        string  `json:"to,omitempty"`
	Confirmed bool    `json:"confirmed"`
}

// Write edits a key.
func (e *kvEngine) Write(ctx context.Context, w KeyWrite) (string, error) {
	if e.c.Security.ReadOnly {
		return "", &ReadOnlyError{Stmt: Stmt{Verb: strings.ToUpper(w.Op), Kind: KindWrite}}
	}
	var args []any
	switch w.Op {
	case "set":
		args = []any{"SET", w.Key, w.Value, "KEEPTTL"}
	case "hset":
		args = []any{"HSET", w.Key, w.Field, w.Value}
	case "hdel":
		args = []any{"HDEL", w.Key, w.Field}
	case "lset":
		args = []any{"LSET", w.Key, w.Index, w.Value}
	case "rpush":
		args = []any{"RPUSH", w.Key, w.Value}
	case "lrem":
		args = []any{"LREM", w.Key, 1, w.Value}
	case "sadd":
		args = []any{"SADD", w.Key, w.Value}
	case "srem":
		args = []any{"SREM", w.Key, w.Value}
	case "zadd":
		args = []any{"ZADD", w.Key, w.Score, w.Value}
	case "zrem":
		args = []any{"ZREM", w.Key, w.Value}
	case "xadd":
		args = []any{"XADD", w.Key, "*", w.Field, w.Value}
	case "xdel":
		args = []any{"XDEL", w.Key, w.Field}
	case "rename":
		if w.To == "" {
			return "", invalid("rename: the new name")
		}
		args = []any{"RENAMENX", w.Key, w.To}
	case "expire":
		if w.TTL <= 0 {
			return "", invalid("expire: seconds above 0")
		}
		args = []any{"EXPIRE", w.Key, w.TTL}
	case "persist":
		args = []any{"PERSIST", w.Key}
	case "del":
		args = []any{"DEL", w.Key}
	default:
		return "", invalid("op %q", w.Op)
	}
	if e.c.Security.ConfirmWrites && !w.Confirmed {
		parts := make([]string, len(args))
		for i, a := range args {
			parts[i] = fmt.Sprint(a)
		}
		return "", &ConfirmError{Code: "confirm", Stmts: []Stmt{{SQL: strings.Join(parts, " "), Kind: KindWrite, Verb: strings.ToUpper(w.Op)}}}
	}
	c := e.rc.Conn()
	defer c.Close()
	if err := c.Select(ctx, w.DB).Err(); err != nil {
		return "", err
	}
	v, err := c.Do(ctx, args...).Result()
	if err != nil {
		return "", err
	}
	if w.Op == "rename" && v == int64(0) {
		return "", invalid("a key named %q already exists", w.To)
	}
	return fmt.Sprint(v), nil
}

// Version is the server's name and version ("Redis 7.2.5").
func (e *kvEngine) Version() string { return e.ver }
