// Package nntp implements a client for the news protocol NNTP,
// as defined in RFC 3977.
package nntp

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"sort"
	"strconv"
	"strings"
	"time"

	log "github.com/Sirupsen/logrus"
)

// timeFormatNew is the NNTP time format string for NEWNEWS / NEWGROUPS
const timeFormatNew = "20060102 150405"

// timeFormatDate is the NNTP time format string for responses to the DATE command
const timeFormatDate = "20060102150405"

// An Error represents an error response from an NNTP server.
type Error struct {
	Code uint
	Msg  string
}

func (e Error) Error() string {
	return fmt.Sprintf("%03d %s", e.Code, e.Msg)
}

// A ProtocolError represents responses from an NNTP server
// that seem incorrect for NNTP.
type ProtocolError string

func (p ProtocolError) Error() string {
	return string(p)
}

// A Conn represents a connection to an NNTP server. The connection with
// an NNTP server is stateful; it keeps track of what group you have
// selected, if any, and (if you have a group selected) which article is
// current, next, or previous.
//
// Some methods that return information about a specific message take
// either a message-id, which is global across all NNTP servers, groups,
// and messages, or a message-number, which is an integer number that is
// local to the NNTP session and currently selected group.
//
// For all methods that return an io.Reader (or an *Article, which contains
// an io.Reader), that io.Reader is only valid until the next call to a
// method of Conn.
type Conn struct {
	conn     *textproto.Conn
	Banner   string
	compress bool
}

// New connects to an NNTP server.
// The network and addr are passed to net.Dial to
// make the connection.
//
// Example:
//   conn, err := nntp.Dial("tcp", "my.news:nntp")
//
func New(network, addr string) (*Conn, error) {
	c, err := textproto.Dial(network, addr)
	if err != nil {
		return nil, err
	}

	_, msg, err := c.ReadCodeLine(200)
	if err != nil {
		return nil, err
	}

	return &Conn{
		conn:   c,
		Banner: msg,
	}, nil
}

// NewTLS connects with TLS
func NewTLS(net, addr string, cfg *tls.Config) (*Conn, error) {
	c, err := tls.Dial(net, addr, cfg)
	if err != nil {
		return nil, err
	}
	conn := textproto.NewConn(c)
	_, msg, err := conn.ReadCodeLine(200)
	if err != nil {
		return nil, err
	}

	return &Conn{
		conn:   conn,
		Banner: msg,
	}, nil
}

// Command sends a low-level command and get a response.
//
// This will return an error if the code doesn't match the expectCode
// prefix.  For example, if you specify "200", the response code MUST
// be 200 or you'll get an error.  If you specify "2", any code from
// 200 (inclusive) to 300 (exclusive) will be success.  An expectCode
// of -1 disables this behavior.
func (c *Conn) Command(cmd string, expectCode int) (int, string, error) {
	log.Infof("client: %s", cmd)
	err := c.conn.PrintfLine(cmd)
	if err != nil {
		return 0, "", err
	}
	code, msg, err := c.conn.ReadCodeLine(expectCode)
	log.Infof("server code: %d, msg: %s, err: %v", code, msg, err)
	return code, msg, err
}

// MultilineCommand wraps the functionality to
func (c *Conn) MultilineCommand(cmd string, expectCode int) (int, []string, error) {
	log.Infof("client: %s", cmd)
	err := c.conn.PrintfLine(cmd)
	if err != nil {
		return 0, nil, err
	}
	rc, l, err := c.conn.ReadCodeLine(expectCode)
	log.Infof("server code: %d, msg: %s, err: %v", rc, l, err)
	if err != nil {
		return rc, nil, err
	}
	lines := []string{l}
	ls, err := c.conn.ReadDotLines()
	if err != nil {
		return rc, nil, err
	}
	for _, line := range ls {
		log.Debugf("server: %v", line)
	}
	lines = append(lines, ls...)
	return rc, lines, err
}

// A Group gives information about a single news group on the server.
type Group struct {
	Name string
	// High and low message-numbers
	High  int64
	Low   int64
	Count int64
	// Status indicates if general posting is allowed --
	// typical values are "y", "n", or "m".
	Status string
}

// An Article represents an NNTP article.
type Article struct {
	Header map[string][]string
	Body   []string
}

func (a *Article) String() string {
	res := []string{}
	for k, v := range a.Header {
		res = append(res, fmt.Sprintf("%s: %s", k, strings.Join(v, ",")))
	}
	res = append(res, "")
	res = append(res, a.Body...)
	return strings.Join(res, "\n")
}

func maybeID(cmd, id string) string {
	if len(id) > 0 {
		return cmd + " " + id
	}
	return cmd
}

// Authenticate logs in to the NNTP server.
// It only sends the password if the server requires one.
func (c *Conn) Authenticate(username, password string) error {
	// Spec says you might not need a password and a username is it.  This needs
	// to change to support that.  Status code 381 means to send a password
	code, _, err := c.Command(fmt.Sprintf("AUTHINFO USER %s", username), 381)
	if code/100 == 3 {
		_, _, err = c.Command(fmt.Sprintf("AUTHINFO PASS %s", password), 281)
	}
	return err
}

// SetCompression turns on compression for this connection
func (c *Conn) SetCompression() error {
	_, _, err := c.Command("XFEATURE COMPRESS GZIP", 290)
	if err == nil {
		c.compress = true
	}
	return err
}

// ModeReader switches the NNTP server to "reader" mode, if it
// is a mode-switching server.
func (c *Conn) ModeReader() error {
	_, _, err := c.Command("MODE READER", 20)
	return err
}

// NewGroups returns a list of groups added since the given time.
func (c *Conn) NewGroups(since time.Time) ([]*Group, error) {
	_, _, err := c.Command(fmt.Sprintf("NEWGROUPS %s GMT", since.Format(timeFormatNew)), 231)
	if err != nil {
		return nil, err
	}
	lines, err := c.conn.ReadDotLines()
	if err != nil {
		return nil, err
	}
	return parseNewGroups(lines)
}

// NewNews returns a list of the IDs of articles posted
// to the given group since the given time.
func (c *Conn) NewNews(group string, since time.Time) ([]string, error) {
	_, lines, err := c.MultilineCommand(fmt.Sprintf("NEWNEWS %s %s GMT", group, since.Format(timeFormatNew)), 230)
	if err != nil {
		return nil, err
	}

	sort.Strings(lines)
	w := 0
	for r, s := range lines {
		if r == 0 || lines[r-1] != s {
			lines[w] = s
			w++
		}
	}
	lines = lines[0:w]

	return lines, nil
}

// MessageOverview of a message returned by OVER/XOVER command.
type MessageOverview struct {
	MessageNumber int       // Message number in the group
	Subject       string    // Subject header value. Empty if the header is missing.
	From          string    // From header value. Empty is the header is missing.
	Date          time.Time // Parsed Date header value. Zero if the header is missing or unparseable.
	MessageID     string    // Message-Id header value. Empty is the header is missing.
	References    []string  // Message-Id's of referenced messages (References header value, split on spaces). Empty if the header is missing.
	Bytes         int       // Message size in bytes, called :bytes metadata item in RFC3977.
	Lines         int       // Message size in lines, called :lines metadata item in RFC3977.
	Extra         []string  // Any additional fields returned by the server.
}

// Overview returns overviews of all messages in the current group with message number between
// begin and end, inclusive.
func (c *Conn) Overview(begin, end int64) ([]MessageOverview, error) {
	_, _, err := c.Command(fmt.Sprintf("XOVER %d-%d", begin, end), 224)
	if err != nil {
		return nil, err
	}

	result := []MessageOverview{}
	var lines []string
	if c.compress {
		log.Debugf("Reading compressed data")
		zr, err := zlib.NewReader(c.conn.R)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		scanner := bufio.NewScanner(zr)
		for scanner.Scan() {
			l := scanner.Text()
			if "." == l {
				break
			}
			lines = append(lines, l)
		}
	} else {
		lines, err = c.conn.ReadDotLines()
		log.Debugf("Read %d lines from connection", len(lines))
		if err != nil {
			return nil, err
		}
	}
	for _, line := range lines {
		if "" == line {
			return result, nil
		}
		overview := MessageOverview{}
		ss := strings.SplitN(strings.TrimSpace(line), "\t", 9)
		if len(ss) < 8 {
			return nil, ProtocolError("short header listing line: " + line + strconv.Itoa(len(ss)))
		}
		overview.MessageNumber, err = strconv.Atoi(ss[0])
		if err != nil {
			return nil, ProtocolError("bad message number '" + ss[0] + "' in line: " + line)
		}
		overview.Subject = ss[1]
		overview.From = ss[2]
		overview.Date, err = parseDate(ss[3])
		if err != nil {
			// Inability to parse date is not fatal: the field in the message may be broken or missing.
			overview.Date = time.Time{}
		}
		overview.MessageID = ss[4]
		overview.References = strings.Split(ss[5], " ") // Message-Id's contain no spaces, so this is safe.
		overview.Bytes, err = strconv.Atoi(ss[6])
		if err != nil {
			return nil, ProtocolError("bad byte count '" + ss[6] + "'in line:" + line)
		}
		overview.Lines, err = strconv.Atoi(ss[7])
		if err != nil {
			return nil, ProtocolError("bad line count '" + ss[7] + "'in line:" + line)
		}
		overview.Extra = append([]string{}, ss[8:]...)
		result = append(result, overview)
	}
	return result, nil
}

func parseGroup(line string) (*Group, error) {
	ss := strings.SplitN(strings.TrimSpace(line), " ", 4)
	if len(ss) < 4 {
		return nil, ProtocolError("short group info line: " + line)
	}
	low, err := strconv.ParseInt(ss[1], 10, 64)
	if err != nil {
		return nil, ProtocolError("bad high article number in line: " + line)
	}
	high, err := strconv.ParseInt(ss[2], 10, 64)
	if err != nil {
		return nil, ProtocolError("bad low article number in line: " + line)
	}
	count, err := strconv.ParseInt(ss[0], 10, 64)
	if err != nil {
		return nil, ProtocolError("bad count in line: " + line)
	}

	return &Group{
		Name:  ss[3],
		High:  high,
		Low:   low,
		Count: count,
	}, nil
}

// parseNewGroups is used to parse a list of group states.
func parseNewGroups(lines []string) ([]*Group, error) {
	res := make([]*Group, len(lines))
	for i, line := range lines {
		ss := strings.SplitN(strings.TrimSpace(line), " ", 4)
		if len(ss) < 4 {
			return nil, ProtocolError("short group info line: " + line)
		}
		high, err := strconv.ParseInt(ss[1], 10, 64)
		if err != nil {
			return nil, ProtocolError("bad number in line: " + line)
		}
		low, err := strconv.ParseInt(ss[2], 10, 64)
		if err != nil {
			return nil, ProtocolError("bad number in line: " + line)
		}
		res[i] = &Group{
			Name: ss[3],
			High: high,
			Low:  low,
		}
	}
	return res, nil
}

// Capabilities returns a list of features this server performs.
// Not all servers support capabilities.
func (c *Conn) Capabilities() ([]string, error) {
	_, lines, err := c.MultilineCommand("CAPABILITIES", 101)
	if err != nil {
		return nil, err
	}
	return lines, nil
}

// Date returns the current time on the server.
// Typically the time is later passed to NewGroups or NewNews.
func (c *Conn) Date() (time.Time, error) {
	_, line, err := c.Command("DATE", 111)
	if err != nil {
		return time.Time{}, err
	}
	t, err := time.Parse(timeFormatDate, line)
	if err != nil {
		return time.Time{}, ProtocolError("invalid time: " + line)
	}
	return t, nil
}

// List returns a list of groups present on the server.
// Valid forms are:
//
//   List() - return active groups
//   List(keyword) - return different kinds of information about groups
//   List(keyword, pattern) - filter groups against a glob-like pattern called a wildmat
//
func (c *Conn) List(a ...string) ([]string, error) {
	if len(a) > 2 {
		return nil, ProtocolError("List only takes up to 2 arguments")
	}
	cmd := "LIST"
	if len(a) > 0 {
		cmd += " " + a[0]
		if len(a) > 1 {
			cmd += " " + a[1]
		}
	}
	_, lines, err := c.MultilineCommand(cmd, 215)
	if err != nil {
		return nil, err
	}
	return lines, nil
}

// Group changes the current group.
func (c *Conn) Group(group string) (*Group, error) {
	_, line, err := c.Command(fmt.Sprintf("GROUP %s", group), 211)
	if err != nil {
		return nil, err
	}
	return parseGroup(line)
}

// Help returns the server's help text.
func (c *Conn) Help() ([]string, error) {
	_, lines, err := c.MultilineCommand("HELP", 100)
	if err != nil {
		return nil, err
	}
	return lines, nil
}

// nextLastStat performs the work for NEXT, LAST, and STAT.
func (c *Conn) nextLastStat(cmd, id string) (string, string, error) {
	_, line, err := c.Command(maybeID(cmd, id), 223)
	if err != nil {
		return "", "", err
	}
	ss := strings.SplitN(line, " ", 3) // optional comment ignored
	if len(ss) < 2 {
		return "", "", ProtocolError("Bad response to " + cmd + ": " + line)
	}
	return ss[0], ss[1], nil
}

// Stat looks up the message with the given id and returns its
// message number in the current group, and vice versa.
// The returned message number can be "0" if the current group
// isn't one of the groups the message was posted to.
func (c *Conn) Stat(id string) (number, msgid string, err error) {
	return c.nextLastStat("STAT", id)
}

// Last selects the previous article, returning its message number and id.
func (c *Conn) Last() (number, msgid string, err error) {
	return c.nextLastStat("LAST", "")
}

// Next selects the next article, returning its message number and id.
func (c *Conn) Next() (number, msgid string, err error) {
	return c.nextLastStat("NEXT", "")
}

// ArticleText returns the article named by id as an io.Reader.
// The article is in plain text format, not NNTP wire format.
func (c *Conn) ArticleText(id string) ([]string, error) {
	_, lines, err := c.MultilineCommand(maybeID("ARTICLE", id), 220)
	if err != nil {
		return nil, err
	}
	return lines, nil
}

// Article returns the article named by id as an *Article.
func (c *Conn) Article(id string) (*Article, error) {
	_, _, err := c.Command(maybeID("ARTICLE", id), 220)
	if err != nil {
		return nil, err
	}
	h, err := c.conn.ReadMIMEHeader()
	if err != nil {
		return nil, err
	}
	a := &Article{}
	a.Header = h
	a.Body, err = c.conn.ReadDotLines()
	if err != nil {
		return nil, err
	}
	return a, nil
}

// HeadText returns the header for the article named by id as an io.Reader.
// The article is in plain text format, not NNTP wire format.
func (c *Conn) HeadText(id string) ([]string, error) {
	_, lines, err := c.MultilineCommand(maybeID("HEAD", id), 221)
	if err != nil {
		return nil, err
	}
	return lines, nil
}

// Head returns the header for the article named by id as an *Article.
// The Body field in the Article is nil.
func (c *Conn) Head(id string) (*Article, error) {
	_, _, err := c.Command(maybeID("HEAD", id), 221)
	if err != nil {
		return nil, err
	}
	r := c.conn.DotReader()
	a, err := readHeader(bufio.NewReader(r))
	if err != nil {
		return nil, err
	}
	return a, nil
}

// Body returns the body for the article named by id as an io.Reader.
func (c *Conn) Body(id string) ([]string, error) {
	_, _, err := c.Command(maybeID("BODY", id), 222)
	if err != nil {
		return nil, err
	}
	lines, err := c.conn.ReadDotLines()
	if err != nil {
		return nil, err
	}
	return lines, nil
}

// RawPost reads a text-formatted article from r and posts it to the server.
func (c *Conn) RawPost(r io.Reader) error {
	_, _, err := c.Command("POST", 3)
	if err != nil {
		return err
	}
	br := bufio.NewReader(r)
	eof := false
	for {
		line, err := br.ReadString('\n')
		if err == io.EOF {
			eof = true
		} else if err != nil {
			return err
		}
		if eof && len(line) == 0 {
			break
		}
		if strings.HasSuffix(line, "\n") {
			line = line[0 : len(line)-1]
		}
		var prefix string
		if strings.HasPrefix(line, ".") {
			prefix = "."
		}
		_, err = fmt.Fprintf(c.conn.W, "%s%s\r\n", prefix, line)
		if err != nil {
			return err
		}
		if eof {
			break
		}
	}

	_, _, err = c.Command(".", 240)
	if err != nil {
		return err
	}
	return nil
}

// Quit sends the QUIT command and closes the connection to the server.
func (c *Conn) Quit() error {
	_, _, err := c.Command("QUIT", 0)
	c.conn.Close()
	return err
}

// Functions after this point are mostly copy-pasted from http
// (though with some modifications). They should be factored out to
// a common library.

// Read a line of bytes (up to \n) from b.
// Give up if the line exceeds maxLineLength.
// The returned bytes are a pointer into storage in
// the bufio, so they are only valid until the next bufio read.
func readLineBytes(b *bufio.Reader) (p []byte, err error) {
	if p, err = b.ReadSlice('\n'); err != nil {
		// We always know when EOF is coming.
		// If the caller asked for a line, there should be a line.
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}

	// Chop off trailing white space.
	var i int
	for i = len(p); i > 0; i-- {
		if c := p[i-1]; c != ' ' && c != '\r' && c != '\t' && c != '\n' {
			break
		}
	}
	return p[0:i], nil
}

var colon = []byte{':'}

// Read a key/value pair from b.
// A key/value has the form Key: Value\r\n
// and the Value can continue on multiple lines if each continuation line
// starts with a space/tab.
func readKeyValue(b *bufio.Reader) (key, value string, err error) {
	line, e := readLineBytes(b)
	if e == io.ErrUnexpectedEOF {
		return "", "", nil
	} else if e != nil {
		return "", "", e
	}
	if len(line) == 0 {
		return "", "", nil
	}

	// Scan first line for colon.
	i := bytes.Index(line, colon)
	if i < 0 {
		goto Malformed
	}

	key = string(line[0:i])
	if strings.Index(key, " ") >= 0 {
		// Key field has space - no good.
		goto Malformed
	}

	// Skip initial space before value.
	for i++; i < len(line); i++ {
		if line[i] != ' ' && line[i] != '\t' {
			break
		}
	}
	value = string(line[i:])

	// Look for extension lines, which must begin with space.
	for {
		c, e := b.ReadByte()
		if c != ' ' && c != '\t' {
			if e != io.EOF {
				b.UnreadByte()
			}
			break
		}

		// Eat leading space.
		for c == ' ' || c == '\t' {
			if c, e = b.ReadByte(); e != nil {
				if e == io.EOF {
					e = io.ErrUnexpectedEOF
				}
				return "", "", e
			}
		}
		b.UnreadByte()

		// Read the rest of the line and add to value.
		if line, e = readLineBytes(b); e != nil {
			return "", "", e
		}
		value += " " + string(line)
	}
	return key, value, nil

Malformed:
	return "", "", ProtocolError("malformed header line: " + string(line))
}

// Internal. Parses headers in NNTP articles. Most of this is stolen from the http package,
// and it should probably be split out into a generic RFC822 header-parsing package.
func readHeader(r *bufio.Reader) (res *Article, err error) {
	res = new(Article)
	res.Header = make(map[string][]string)
	for {
		var key, value string
		if key, value, err = readKeyValue(r); err != nil {
			return nil, err
		}
		if key == "" {
			break
		}
		key = http.CanonicalHeaderKey(key)
		// RFC 3977 says nothing about duplicate keys' values being equivalent to
		// a single key joined with commas, so we keep all values seperate.
		oldvalue, present := res.Header[key]
		if present {
			sv := []string{}
			sv = append(sv, oldvalue...)
			sv = append(sv, value)
			res.Header[key] = sv
		} else {
			res.Header[key] = []string{value}
		}
	}
	return res, nil
}
