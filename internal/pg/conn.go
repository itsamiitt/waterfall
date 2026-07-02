// Package pg is a minimal, dependency-free PostgreSQL client (wire protocol v3) — enough
// for the enrichment datastore: connect, run DDL/DML, parameterized queries, and read text
// results. It exists so the project keeps its zero-external-dependency property while still
// integrating with a real Postgres for tenant-isolation (RLS) enforcement (docs/06, docs/18).
//
// Auth: trust, cleartext, and SCRAM-SHA-256 (RFC 7677, no channel binding). Optional TLS via
// the SSLRequest negotiation (Config.TLS / DSN sslmode). Limits (honest): text result format
// only; MD5 auth and SCRAM channel binding are not implemented; one in-flight statement at a
// time (no pipelining); a Conn is not safe for concurrent use (see Pool). It is a focused
// datastore client, not a general driver.
package pg

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// Config is the connection target.
type Config struct {
	Host     string
	Port     int
	User     string
	Password string // used for cleartext or SCRAM-SHA-256 auth
	Database string
	TLS      *tls.Config // non-nil requests a TLS (SSLRequest) upgrade before startup
}

// Conn is a single Postgres connection. Not safe for concurrent use.
type Conn struct {
	c   net.Conn
	r   *bufio.Reader
	buf []byte
}

// Result is a text-format query result.
type Result struct {
	Cols []string
	Rows [][]*string // nil element == SQL NULL
}

// PGError is a structured Postgres ErrorResponse.
type PGError struct {
	Severity string
	Code     string
	Message  string
}

func (e *PGError) Error() string {
	return fmt.Sprintf("pg: %s %s: %s", e.Severity, e.Code, e.Message)
}

// Connect dials Postgres and completes the v3 startup handshake.
func Connect(cfg Config) (*Conn, error) {
	d := net.Dialer{Timeout: 5 * time.Second}
	nc, err := d.Dial("tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	if err != nil {
		return nil, err
	}
	c := &Conn{c: nc, r: bufio.NewReader(nc)}
	if cfg.TLS != nil {
		if err := c.startTLS(cfg.TLS); err != nil {
			nc.Close()
			return nil, err
		}
	}
	if err := c.startup(cfg); err != nil {
		c.c.Close()
		return nil, err
	}
	return c, nil
}

// startTLS performs the PostgreSQL SSLRequest negotiation and, if the server agrees,
// upgrades the connection to TLS before the startup handshake.
func (c *Conn) startTLS(tlsCfg *tls.Config) error {
	// SSLRequest: length(8) + magic code 80877103, no type byte.
	req := be32(nil, 8)
	req = be32(req, 80877103)
	if _, err := c.c.Write(req); err != nil {
		return err
	}
	var resp [1]byte
	if _, err := io.ReadFull(c.c, resp[:]); err != nil {
		return err
	}
	if resp[0] != 'S' {
		return fmt.Errorf("pg: server declined TLS (replied %q)", resp[0])
	}
	tc := tls.Client(c.c, tlsCfg)
	if err := tc.Handshake(); err != nil {
		return err
	}
	c.c = tc
	c.r = bufio.NewReader(tc)
	return nil
}

// Close closes the connection (best-effort Terminate).
func (c *Conn) Close() error {
	if c.c == nil {
		return nil
	}
	_ = c.writeMsg('X', nil)
	return c.c.Close()
}

func (c *Conn) startup(cfg Config) error {
	var b []byte
	b = be32(b, 196608) // protocol 3.0
	b = cstr(b, "user")
	b = cstr(b, cfg.User)
	if cfg.Database != "" {
		b = cstr(b, "database")
		b = cstr(b, cfg.Database)
	}
	b = cstr(b, "client_encoding")
	b = cstr(b, "UTF8")
	b = append(b, 0) // terminator
	// startup message has no type byte: length-prefixed body
	if err := c.writeRaw(prefixLen(b)); err != nil {
		return err
	}
	for {
		typ, body, err := c.readMsg()
		if err != nil {
			return err
		}
		switch typ {
		case 'R': // Authentication
			code := binary.BigEndian.Uint32(body)
			switch code {
			case 0: // AuthenticationOk
			case 3: // cleartext password
				if err := c.writeMsg('p', cstr(nil, cfg.Password)); err != nil {
					return err
				}
			case 10: // SASL (SCRAM-SHA-256)
				if err := c.authSCRAM(cfg, body[4:]); err != nil {
					return err
				}
			default:
				return fmt.Errorf("pg: unsupported auth method %d (need trust, cleartext, or SCRAM-SHA-256)", code)
			}
		case 'E':
			return parseError(body)
		case 'Z': // ReadyForQuery
			return nil
		case 'S', 'K', 'N': // ParameterStatus / BackendKeyData / Notice — ignore
		default:
			// ignore other startup messages
		}
	}
}

// authSCRAM runs the SASL SCRAM-SHA-256 exchange. mechBody is the AuthenticationSASL body
// (the offered mechanism list). It consumes the SASLContinue and SASLFinal messages; the
// caller's loop then reads AuthenticationOk + ReadyForQuery.
func (c *Conn) authSCRAM(cfg Config, mechBody []byte) error {
	const mech = "SCRAM-SHA-256"
	if !offersMechanism(mechBody, mech) {
		return fmt.Errorf("pg: server did not offer %s", mech)
	}
	sc, err := newSCRAM(cfg.User, cfg.Password)
	if err != nil {
		return err
	}

	// SASLInitialResponse: mechanism name + int32(len) + client-first-message.
	first := sc.clientFirst()
	var init []byte
	init = cstr(init, mech)
	init = be32(init, uint32(len(first)))
	init = append(init, first...)
	if err := c.writeMsg('p', init); err != nil {
		return err
	}

	// Expect AuthenticationSASLContinue (code 11).
	typ, body, err := c.readMsg()
	if err != nil {
		return err
	}
	if typ == 'E' {
		return parseError(body)
	}
	if typ != 'R' || binary.BigEndian.Uint32(body) != 11 {
		return fmt.Errorf("pg: expected SASLContinue, got %q", typ)
	}
	clientFinal, err := sc.clientFinal(body[4:])
	if err != nil {
		return err
	}
	if err := c.writeMsg('p', clientFinal); err != nil {
		return err
	}

	// Expect AuthenticationSASLFinal (code 12) with the server verifier.
	typ, body, err = c.readMsg()
	if err != nil {
		return err
	}
	if typ == 'E' {
		return parseError(body)
	}
	if typ != 'R' || binary.BigEndian.Uint32(body) != 12 {
		return fmt.Errorf("pg: expected SASLFinal, got %q", typ)
	}
	return sc.verify(body[4:])
}

// offersMechanism reports whether the null-terminated mechanism list contains want.
func offersMechanism(body []byte, want string) bool {
	for _, m := range strings.Split(string(body), "\x00") {
		if m == want {
			return true
		}
	}
	return false
}

// Exec runs a statement via the simple query protocol, discarding any rows.
func (c *Conn) Exec(sql string) error {
	_, err := c.simpleQuery(sql)
	return err
}

// Query runs a statement via the simple query protocol and returns its rows.
func (c *Conn) Query(sql string) (*Result, error) {
	return c.simpleQuery(sql)
}

// RolePrivileges reports whether the currently-connected role can bypass row-level security —
// i.e. it is a superuser or has the BYPASSRLS attribute. Such a role would silently defeat
// tenant isolation (G1), so the app checks this at startup and refuses to run as one.
func (c *Conn) RolePrivileges() (super, bypassRLS bool, err error) {
	res, err := c.Query("select rolsuper, rolbypassrls from pg_roles where rolname = current_user")
	if err != nil {
		return false, false, err
	}
	if len(res.Rows) == 0 || res.Rows[0][0] == nil {
		return false, false, fmt.Errorf("pg: current role not found in pg_roles")
	}
	super = *res.Rows[0][0] == "t"
	if len(res.Rows[0]) > 1 && res.Rows[0][1] != nil {
		bypassRLS = *res.Rows[0][1] == "t"
	}
	return super, bypassRLS, nil
}

func (c *Conn) simpleQuery(sql string) (*Result, error) {
	if err := c.writeMsg('Q', cstr(nil, sql)); err != nil {
		return nil, err
	}
	res := &Result{}
	var firstErr error
	for {
		typ, body, err := c.readMsg()
		if err != nil {
			return nil, err
		}
		switch typ {
		case 'T':
			res.Cols = parseRowDescription(body)
		case 'D':
			res.Rows = append(res.Rows, parseDataRow(body))
		case 'C', 'I', 'S', 'N':
			// CommandComplete / EmptyQuery / ParameterStatus / Notice
		case 'E':
			if firstErr == nil {
				firstErr = parseError(body)
			}
		case 'Z':
			return res, firstErr
		}
	}
}

// ExecParams runs a parameterized statement (extended protocol), discarding rows.
func (c *Conn) ExecParams(sql string, args ...any) error {
	_, err := c.QueryParams(sql, args...)
	return err
}

// QueryParams runs a parameterized statement via the extended query protocol (Parse/Bind/
// Execute/Sync) with all parameters and results in text format. Using bound parameters —
// not string interpolation — is what keeps the datastore free of SQL injection.
func (c *Conn) QueryParams(sql string, args ...any) (*Result, error) {
	// Parse (unnamed statement, let the server infer parameter types)
	var p []byte
	p = cstr(p, "") // statement name
	p = cstr(p, sql)
	p = be16(p, 0) // 0 parameter type oids
	if err := c.writeMsg('P', p); err != nil {
		return nil, err
	}
	// Bind (unnamed portal <- unnamed statement)
	var b []byte
	b = cstr(b, "") // portal
	b = cstr(b, "") // statement
	b = be16(b, 0)  // 0 parameter format codes => all text
	b = be16(b, uint16(len(args)))
	for _, a := range args {
		val, isNull := encodeParam(a)
		if isNull {
			b = be32(b, 0xFFFFFFFF) // -1 length == NULL
			continue
		}
		b = be32(b, uint32(len(val)))
		b = append(b, val...)
	}
	b = be16(b, 0) // 0 result format codes => all text
	if err := c.writeMsg('B', b); err != nil {
		return nil, err
	}
	return c.finishExtended()
}

// finishExtended issues Describe(portal)/Execute/Sync and reads the reply stream. Split out
// so the Bind builder above stays readable.
func (c *Conn) finishExtended() (*Result, error) {
	// Describe portal ""
	var d []byte
	d = append(d, 'P')
	d = cstr(d, "")
	if err := c.writeMsg('D', d); err != nil {
		return nil, err
	}
	// Execute portal "", unlimited rows
	var e []byte
	e = cstr(e, "")
	e = be32(e, 0)
	if err := c.writeMsg('E', e); err != nil {
		return nil, err
	}
	if err := c.writeMsg('S', nil); err != nil { // Sync
		return nil, err
	}

	res := &Result{}
	var firstErr error
	for {
		typ, body, err := c.readMsg()
		if err != nil {
			return nil, err
		}
		switch typ {
		case '1', '2', 'n': // ParseComplete / BindComplete / NoData
		case 'T':
			res.Cols = parseRowDescription(body)
		case 'D':
			res.Rows = append(res.Rows, parseDataRow(body))
		case 'C', 'S', 'N', 'I':
		case 'E':
			if firstErr == nil {
				firstErr = parseError(body)
			}
		case 'Z':
			return res, firstErr
		}
	}
}

// --- message IO ---

func (c *Conn) writeRaw(b []byte) error {
	_, err := c.c.Write(b)
	return err
}

func (c *Conn) writeMsg(typ byte, body []byte) error {
	c.buf = c.buf[:0]
	c.buf = append(c.buf, typ)
	c.buf = be32(c.buf, uint32(len(body)+4))
	c.buf = append(c.buf, body...)
	_, err := c.c.Write(c.buf)
	return err
}

func (c *Conn) readMsg() (byte, []byte, error) {
	var hdr [5]byte
	if _, err := readFull(c.r, hdr[:]); err != nil {
		return 0, nil, err
	}
	typ := hdr[0]
	n := binary.BigEndian.Uint32(hdr[1:5])
	if n < 4 {
		return 0, nil, fmt.Errorf("pg: bad message length %d", n)
	}
	body := make([]byte, n-4)
	if _, err := readFull(c.r, body); err != nil {
		return 0, nil, err
	}
	return typ, body, nil
}

// --- parsers ---

func parseRowDescription(body []byte) []string {
	if len(body) < 2 {
		return nil
	}
	count := int(binary.BigEndian.Uint16(body[:2]))
	off := 2
	cols := make([]string, 0, count)
	for i := 0; i < count; i++ {
		end := off
		for end < len(body) && body[end] != 0 {
			end++
		}
		cols = append(cols, string(body[off:end]))
		off = end + 1
		off += 18 // tableOID(4)+colAttr(2)+typeOID(4)+typeLen(2)+typeMod(4)+format(2)
	}
	return cols
}

func parseDataRow(body []byte) []*string {
	if len(body) < 2 {
		return nil
	}
	count := int(binary.BigEndian.Uint16(body[:2]))
	off := 2
	row := make([]*string, 0, count)
	for i := 0; i < count; i++ {
		if off+4 > len(body) {
			break
		}
		l := int32(binary.BigEndian.Uint32(body[off : off+4]))
		off += 4
		if l < 0 {
			row = append(row, nil) // NULL
			continue
		}
		s := string(body[off : off+int(l)])
		off += int(l)
		row = append(row, &s)
	}
	return row
}

func parseError(body []byte) *PGError {
	e := &PGError{}
	off := 0
	for off < len(body) {
		field := body[off]
		if field == 0 {
			break
		}
		off++
		end := off
		for end < len(body) && body[end] != 0 {
			end++
		}
		val := string(body[off:end])
		off = end + 1
		switch field {
		case 'S':
			e.Severity = val
		case 'C':
			e.Code = val
		case 'M':
			e.Message = val
		}
	}
	return e
}

// --- encoding helpers ---

func be16(b []byte, v uint16) []byte {
	return append(b, byte(v>>8), byte(v))
}

func be32(b []byte, v uint32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func cstr(b []byte, s string) []byte {
	b = append(b, s...)
	return append(b, 0)
}

func prefixLen(body []byte) []byte {
	out := be32(nil, uint32(len(body)+4))
	return append(out, body...)
}

func encodeParam(v any) ([]byte, bool) {
	switch x := v.(type) {
	case nil:
		return nil, true
	case string:
		return []byte(x), false
	case int:
		return []byte(strconv.Itoa(x)), false
	case int64:
		return []byte(strconv.FormatInt(x, 10)), false
	case float64:
		return []byte(strconv.FormatFloat(x, 'g', -1, 64)), false
	case bool:
		if x {
			return []byte("t"), false
		}
		return []byte("f"), false
	case time.Time:
		return []byte(x.UTC().Format(time.RFC3339Nano)), false
	default:
		return []byte(fmt.Sprint(x)), false
	}
}

// readFull reads exactly len(p) bytes, coping with short reads.
func readFull(r *bufio.Reader, p []byte) (int, error) {
	total := 0
	for total < len(p) {
		n, err := r.Read(p[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// ParseDSN parses a minimal "host=... port=... user=... dbname=... password=... sslmode=..."
// DSN. sslmode follows libpq semantics: require => encrypt without verifying the certificate;
// verify-ca/verify-full => verify against the system roots with the host as ServerName;
// disable/empty => plaintext.
func ParseDSN(dsn string) Config {
	cfg := Config{Host: "127.0.0.1", Port: 5432}
	sslmode := ""
	for _, kv := range strings.Fields(dsn) {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			continue
		}
		k, v := kv[:i], kv[i+1:]
		switch k {
		case "host":
			cfg.Host = v
		case "port":
			if p, err := strconv.Atoi(v); err == nil {
				cfg.Port = p
			}
		case "user":
			cfg.User = v
		case "password":
			cfg.Password = v
		case "dbname", "database":
			cfg.Database = v
		case "sslmode":
			sslmode = v
		}
	}
	switch sslmode {
	case "require":
		cfg.TLS = &tls.Config{ServerName: cfg.Host, InsecureSkipVerify: true} //nolint:gosec // libpq 'require' does not verify the cert
	case "verify-ca", "verify-full":
		cfg.TLS = &tls.Config{ServerName: cfg.Host}
	}
	return cfg
}
