package codec

import (
	"encoding/binary"
	"encoding/json/jsontext"
	"fmt"
	"hash/maphash"
	"math/bits"
	"sync"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"github.com/asymptote-labs/agent-beacon/gateway/llm"
)

// Skimming decodes a request in proportion to what is new. Clients resend the whole
// conversation on every call, but analysis reads only the system messages, the tool
// definitions, the tail (everything after the model's last turn), the model's last turn and the
// calls the tail's tool results answer. A skim decodes those in full and measures the rest: it
// reads each history message as a raw value (jsontext validates it, so a body DecodeRequest
// rejects is rejected too), scans it for its role, the volume of its parts and the IDs of the
// calls it makes, and keeps only that. Measuring takes no decoded strings: text lengths come from
// the raw string bytes, compact JSON lengths from the raw value.
//
// The measure is exact by construction or not taken: a measurer mirrors the decoder of its
// protocol for the shapes clients send, and gives up on anything it does not mirror exactly (a
// member name that matches case-insensitively, a repeated composite member, a block type whose
// decoding it does not copy, a value of the wrong type). A message it gives up on is decoded in
// full, which also surfaces any error the full decode would report, and measured from its parts.

// SkimRequest decodes a request body of protocol p as DecodeRequest does, except that messages
// analysis does not read are elided: every message keeps its place and role, but an elided
// message has no parts, and the volume of its content is in Request.Elided, by channel, exactly
// as counting its decoded parts would have measured it.
//
// Fully decoded are every system message; every message from the start of the final run of
// assistant messages before the tail through the end; and every earlier message holding a tool
// call that a tool result in those references, so that binding a tail tool result to its call
// finds what a full decode finds. Every other field is as DecodeRequest decodes it.
func SkimRequest(p llm.Protocol, body []byte) (*llm.Request, error) {
	c, ok := For(p)
	if !ok {
		return nil, fmt.Errorf("codec: unknown protocol %q", p)
	}
	rc := c.(requestCodec)
	seeds := memoSeeds[p]
	// Content this process decoded before (history, tools, system prompt) is cut out of the
	// body and taken from the cache (see planSkim). Anything that goes wrong on that path, an
	// invalid body included, is decided by the plain skim of the body itself, which also
	// reports the error.
	if plan := planSkim(body, p, seeds); plan != nil {
		in := &skimInput{plan: plan, seeds: seeds}
		if req, err := rc.request(plan.shell, in); err == nil && plan.visited(in) {
			return req, nil
		}
	}
	return rc.request(body, &skimInput{seeds: seeds})
}

// requestCodec decodes a request in full (skim nil) or skimmed.
type requestCodec interface {
	request(body []byte, skim *skimInput) (*llm.Request, error)
}

// skimCodec is what a protocol supplies to skim its message array.
type skimCodec interface {
	// measure scans element raw, already validated, appending the messages it stands for to
	// sk.msgs and the IDs of their calls to sk.ids. It reports false, leaving partial appends
	// for the caller to drop, when it cannot tell exactly what decoding raw would yield.
	measure(sk *skimmer, raw []byte) bool
	// decode decodes element raw as the full decoder does, appending its messages to msgs; known
	// is false for an element the protocol does not know, typ naming it.
	decode(raw []byte, msgs []llm.Message) (_ []llm.Message, known bool, typ string, err error)
}

// partSum is the measured volume of a list of parts.
type partSum struct {
	text   llm.Volume // non-empty text parts; their channel is the message's role's
	asst   llm.Volume // reasoning and tool calls
	result llm.Volume // tool results: their joined text
	media  int        // media parts in the list
	// resultMedia counts the media parts of the list's tool results.
	resultMedia int
}

func (ps *partSum) addText(n int) {
	if n > 0 {
		ps.text.Items++
		ps.text.Bytes += n
	}
}

func (ps *partSum) addReasoning(n int) {
	if n > 0 {
		ps.asst.Items++
		ps.asst.Bytes += n
	}
}

// addCall counts a call part, whose plain text is its name, a space and its arguments.
func (ps *partSum) addCall(nameLen, argsLen int) {
	ps.asst.Items++
	ps.asst.Bytes += nameLen + 1 + argsLen
}

// addResult counts a tool result whose parts c measured: its plain text is c's text parts
// joined by newlines.
func (ps *partSum) addResult(c *partSum) {
	ps.result.Items++
	ps.result.Bytes += c.text.Bytes + max(c.text.Items-1, 0)
	ps.resultMedia += c.media
}

// addParts appends the parts c measured.
func (ps *partSum) addParts(c *partSum) {
	ps.text.Items += c.text.Items
	ps.text.Bytes += c.text.Bytes
	ps.asst.Items += c.asst.Items
	ps.asst.Bytes += c.asst.Bytes
	ps.result.Items += c.result.Items
	ps.result.Bytes += c.result.Bytes
	ps.media += c.media
	ps.resultMedia += c.resultMedia
}

// messageChannels are the channels a message's parts arrive on, indexing volumes.
var messageChannels = [...]llm.Channel{llm.ChannelSystem, llm.ChannelUser, llm.ChannelAssistant, llm.ChannelResult, llm.ChannelMedia}

// volumes is content volume on messageChannels.
type volumes [len(messageChannels)]llm.Volume

func channelIndex(ch llm.Channel) int {
	switch ch {
	case llm.ChannelSystem:
		return 0
	case llm.ChannelUser:
		return 1
	case llm.ChannelAssistant:
		return 2
	case llm.ChannelResult:
		return 3
	}
	return 4
}

func (v *volumes) addVolumes(o *volumes) {
	for i := range v {
		v[i].Items += o[i].Items
		v[i].Bytes += o[i].Bytes
	}
}

func (v *volumes) add(ch llm.Channel, items, bytes int) {
	x := &v[channelIndex(ch)]
	x.Items += items
	x.Bytes += bytes
}

// addSum counts measured parts of a message of role, as llm.Inventory.AddMessages counts them.
func (v *volumes) addSum(ps *partSum, role llm.Role) {
	v.add(llm.Part{Kind: llm.PartText}.Channel(role), ps.text.Items, ps.text.Bytes)
	v.add(llm.ChannelAssistant, ps.asst.Items, ps.asst.Bytes)
	v.add(llm.ChannelResult, ps.result.Items, ps.result.Bytes)
	v.add(llm.ChannelMedia, ps.media+ps.resultMedia, 0)
}

// addParts counts decoded parts of a message of role with sign, as llm.Inventory.AddMessages
// counts them.
func (v *volumes) addParts(role llm.Role, parts []llm.Part, sign int) {
	for _, p := range parts {
		v.add(p.Channel(role), sign, sign*p.PlainTextLen())
		if p.Kind == llm.PartToolResult {
			for _, rp := range p.Result.Parts {
				if rp.Kind == llm.PartMedia {
					v.add(llm.ChannelMedia, sign, 0)
				}
			}
		}
	}
}

// inventory is v as an inventory, nil when it counts nothing.
func (v *volumes) inventory() llm.Inventory {
	var inv llm.Inventory
	for i, x := range v {
		if x.Items == 0 {
			continue
		}
		if inv == nil {
			inv = llm.Inventory{}
		}
		inv[messageChannels[i]] = x
	}
	return inv
}

// skimMsg is one message of a skimmed array.
type skimMsg struct {
	role llm.Role
	// raw is the array element the message comes from; an element can stand for two messages.
	raw []byte
	// ids are the message's call IDs, sk.ids[idLo:idHi].
	idLo, idHi int32
	// parts is 1 + the index in sk.parts of the message's decoded content, 0 before decoding.
	parts int32
	// measured reports that the message's volume is in sk.measured.
	measured bool
	keep     bool
}

// skimmer holds a skim of one message array. Skimmers are pooled: their slices grow with a
// session's length, and a gateway skims every call of every session.
type skimmer struct {
	codec skimCodec
	seed  maphash.Seed // the protocol's memo seed
	raw   []byte       // the element being measured
	msgs  []skimMsg
	ids   [][]byte
	parts [][]llm.Part
	// vols are the volumes of the messages the element being measured pushed, for its memo entry.
	vols []volumes
	// measured is the volume of the measured messages.
	measured volumes
	// unknown lists the types of elements the codec does not know, in order.
	unknown []string
}

var skimmers = sync.Pool{New: func() any { return new(skimmer) }}

// release returns sk to the pool, first dropping every reference into the body and the decoded
// parts it holds, so that a pooled skimmer keeps no request alive.
func (sk *skimmer) release() {
	clear(sk.msgs[:cap(sk.msgs)])
	clear(sk.ids[:cap(sk.ids)])
	clear(sk.parts[:cap(sk.parts)])
	*sk = skimmer{msgs: sk.msgs[:0], ids: sk.ids[:0], parts: sk.parts[:0], vols: sk.vols[:0]}
	skimmers.Put(sk)
}

// push appends a measured message whose call IDs are sk.ids[idLo:].
func (sk *skimmer) push(role llm.Role, sum *partSum, idLo int) {
	var v volumes
	v.addSum(sum, role)
	sk.measured.addVolumes(&v)
	sk.vols = append(sk.vols, v)
	sk.msgs = append(sk.msgs, skimMsg{role: role, raw: sk.raw, idLo: int32(idLo), idHi: int32(len(sk.ids)), measured: true})
}

// skimInput says where a skim reads the message array from: the decoder, or, when planned with
// the array cut, the element extents planSkim found in the original body, the decoder holding a
// placeholder.
type skimInput struct {
	plan   *skimPlan
	seeds  *memoSeedSet
	visits int // times the decode reached the message array
}

// messages skims the message array at dec, returning the messages, the volume of the elided
// ones, and the types of unknown elements. The messages slice has room to insert one more.
func (in *skimInput) messages(dec *jsontext.Decoder, codec skimCodec) ([]llm.Message, llm.Inventory, []string, error) {
	sk := skimmers.Get().(*skimmer)
	defer sk.release()
	sk.codec, sk.seed = codec, in.seeds[memoMessage]
	in.visits++
	var err error
	if in.plan == nil || !in.plan.cutMessages {
		err = readArray(dec, func() error {
			raw, err := dec.ReadValue()
			if err != nil {
				return eofUnexpected(err)
			}
			return sk.add(raw, true)
		})
	} else {
		// The decoder must be at the placeholder: the shell's top-level member, reached once.
		v, rerr := dec.ReadValue()
		if rerr != nil || string(v) != "[]" || dec.InputOffset() != int64(in.plan.messagesEnd) || in.visits != 1 {
			return nil, nil, nil, errPlan
		}
		for _, raw := range in.plan.elems {
			if err = sk.add(raw, false); err != nil {
				break
			}
		}
	}
	if err != nil {
		return nil, nil, nil, err
	}
	return sk.finish()
}

// parse decodes an element the codec would not measure, appending its messages.
func (sk *skimmer) parse(raw []byte) error {
	decoded, known, typ, err := sk.codec.decode(raw, nil)
	if err != nil {
		return err
	}
	if !known {
		sk.unknown = append(sk.unknown, typ)
	}
	for _, m := range decoded {
		lo := len(sk.ids)
		for _, p := range m.Parts {
			if p.Kind == llm.PartToolCall {
				sk.ids = append(sk.ids, unsafe.Slice(unsafe.StringData(p.Call.ID), len(p.Call.ID)))
			}
		}
		sk.parts = append(sk.parts, m.Parts)
		sk.msgs = append(sk.msgs, skimMsg{role: m.Role, raw: raw, idLo: int32(lo), idHi: int32(len(sk.ids)), parts: int32(len(sk.parts))})
	}
	return nil
}

// sameElem reports that two messages come from the same element.
func sameElem(a, b *skimMsg) bool {
	return unsafe.SliceData(a.raw) == unsafe.SliceData(b.raw) && len(a.raw) == len(b.raw)
}

// keep marks message i kept, decoding the element it comes from and checking that it yields the
// messages the measure counted.
func (sk *skimmer) keep(i int) error {
	sk.msgs[i].keep = true
	if sk.msgs[i].parts != 0 {
		return nil
	}
	lo, hi := i, i+1
	for lo > 0 && sameElem(&sk.msgs[lo-1], &sk.msgs[i]) {
		lo--
	}
	for hi < len(sk.msgs) && sameElem(&sk.msgs[hi], &sk.msgs[i]) {
		hi++
	}
	decoded, _, _, err := sk.codec.decode(sk.msgs[i].raw, nil)
	if err != nil {
		return err
	}
	if len(decoded) != hi-lo {
		return fmt.Errorf("skim: an element decodes to %d messages, measured %d", len(decoded), hi-lo)
	}
	system := false
	for k, m := range decoded {
		sm := &sk.msgs[lo+k]
		if m.Role != sm.role {
			return fmt.Errorf("skim: an element decodes to role %s, measured %s", m.Role, sm.role)
		}
		sk.parts = append(sk.parts, m.Parts)
		sm.parts = int32(len(sk.parts))
		system = system || m.Role == llm.RoleSystem
	}
	if system && sk.msgs[lo].measured {
		// A system message is decoded on every call: keep its parts with its measure.
		sk.rememberParts(sk.msgs[i].raw, lo, hi)
	}
	return nil
}

func (sk *skimmer) finish() ([]llm.Message, llm.Inventory, []string, error) {
	n := len(sk.msgs)
	if n == 0 {
		return nil, nil, sk.unknown, nil
	}
	// Kept: the final run of assistant messages and everything after it (all of it when the
	// model has not spoken), and system messages.
	start := n
	for start > 0 && sk.msgs[start-1].role != llm.RoleAssistant {
		start--
	}
	for start > 0 && sk.msgs[start-1].role == llm.RoleAssistant {
		start--
	}
	for i := range sk.msgs {
		if i >= start || sk.msgs[i].role == llm.RoleSystem {
			if err := sk.keep(i); err != nil {
				return nil, nil, nil, err
			}
		}
	}
	// And the messages holding the calls the kept tool results answer.
	var referenced map[string]bool
	for _, m := range sk.msgs[start:] {
		for _, p := range sk.parts[m.parts-1] {
			if p.Kind == llm.PartToolResult {
				if referenced == nil {
					referenced = map[string]bool{}
				}
				referenced[p.Result.CallID] = true
			}
		}
	}
	for i := range start {
		m := &sk.msgs[i]
		if referenced == nil || m.keep {
			continue
		}
		for _, id := range sk.ids[m.idLo:m.idHi] {
			if referenced[string(id)] {
				if err := sk.keep(i); err != nil {
					return nil, nil, nil, err
				}
				break
			}
		}
	}
	// The elided volume is that of the measured messages less those kept, plus that of the
	// decoded messages not kept.
	elided := sk.measured
	out := make([]llm.Message, n, n+1)
	for i := range sk.msgs {
		m := &sk.msgs[i]
		out[i].Role = m.role
		var parts []llm.Part
		if m.parts != 0 {
			parts = sk.parts[m.parts-1]
		}
		switch {
		case m.keep:
			out[i].Parts = parts
			if m.measured {
				elided.addParts(m.role, parts, -1)
			}
		case !m.measured:
			out[i].Elided = true
			elided.addParts(m.role, parts, 1)
		default:
			out[i].Elided = true
		}
	}
	return out, elided.inventory(), sk.unknown, nil
}

// rawScan walks a JSON value jsontext has validated, so it checks no syntax.
type rawScan struct {
	b []byte
	i int
}

func (s *rawScan) ws() {
	for s.i < len(s.b) {
		switch s.b[s.i] {
		case ' ', '\t', '\r', '\n':
			s.i++
		default:
			return
		}
	}
}

// peek is the first byte of the next value: '{', '[', '"', 'n', 't', 'f', or a number's.
func (s *rawScan) peek() byte {
	s.ws()
	return s.b[s.i]
}

// Strings are scanned eight bytes at a time: a word holding no quote, backslash or (when
// measuring) non-ASCII byte is plain text, and the escapes of text (\n, \t, \") are passed
// within their word. Only the lowest byte a mask flags is exact (a borrow can flag a byte
// above a match), so after each escape the rest of the word is tested afresh.
const (
	lsb = 0x0101010101010101
	msb = 0x8080808080808080
)

// quoteOrEscape flags the bytes of w that are '"' or '\\'.
func quoteOrEscape(w uint64) uint64 {
	q, e := w^(lsb*'"'), w^(lsb*'\\')
	return ((q-lsb)&^q | (e-lsb)&^e) & msb
}

// special flags the bytes of w that are '"', '\\' or not ASCII. Shifting w right brings in zero
// bytes, which are none of these.
func special(w uint64) uint64 { return quoteOrEscape(w) | w&msb }

// str consumes a string, returning it quoted as in the input and whether it holds an escape.
func (s *rawScan) str() (q []byte, esc bool) {
	end, esc := scanString(s.b, s.i)
	q, s.i = s.b[s.i:end], end
	return q, esc
}

// text consumes a string, returning the length unquote decodes it to: escapes count as the
// bytes they decode to, a \u escape of a lone surrogate and each byte of invalid UTF-8 as the
// replacement character. adj is what the decoded length differs from the raw length by.
func (s *rawScan) text() int {
	b, start := s.b, s.i+1
	i, adj := start, 0
	for {
		if i+8 <= len(b) {
			w := binary.LittleEndian.Uint64(b[i:])
			m := special(w)
			if m == 0 {
				i += 8
				continue
			}
			p := bits.TrailingZeros64(m) >> 3
			for p < 7 && byte(w>>(p*8)) == '\\' && byte(w>>((p+1)*8)) != 'u' {
				adj-- // a two-byte escape decodes to one byte
				if p += 2; p >= 8 {
					break
				}
				if m = special(w >> (p * 8)); m == 0 {
					p = 8
					break
				}
				p += bits.TrailingZeros64(m) >> 3
			}
			if i += p; p >= 8 {
				continue
			}
		}
		switch c := b[i]; {
		case c == '"':
			s.i = i + 1
			return i - start + adj
		case c == '\\' && b[i+1] == 'u':
			r := hex4(b[i+2 : i+6])
			i += 6
			switch {
			case !utf16.IsSurrogate(r):
				adj += utf8.RuneLen(r) - 6
			case r < 0xdc00 && b[i] == '\\' && b[i+1] == 'u' && utf16.DecodeRune(r, hex4(b[i+2:i+6])) != utf8.RuneError:
				adj += 4 - 12
				i += 6
			default:
				adj += len(string(utf8.RuneError)) - 6
			}
		case c == '\\':
			adj--
			i += 2
		case c < utf8.RuneSelf:
			i++
		default:
			r, size := utf8.DecodeRune(b[i:])
			if r == utf8.RuneError && size == 1 {
				adj += len(string(utf8.RuneError)) - 1
			}
			i += size
		}
	}
}

// skip consumes a value.
func (s *rawScan) skip() {
	switch s.peek() {
	case '"':
		s.str()
	case '{', '[':
		depth := 0
		for {
			switch s.b[s.i] {
			case '"':
				s.str()
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					s.i++
					return
				}
			}
			s.i++
		}
	default:
		for s.i < len(s.b) {
			switch s.b[s.i] {
			case ',', '}', ']', ' ', '\t', '\r', '\n':
				return
			}
			s.i++
		}
	}
}

// value consumes a value, returning it as in the input.
func (s *rawScan) value() []byte {
	s.ws()
	start := s.i
	s.skip()
	return s.b[start:s.i]
}

// open consumes the '{' or '[' that peek returned.
func (s *rawScan) open() { s.i++ }

// member advances to the next member of an open object, returning its name as in the input;
// false at the object's end, which it consumes.
func (s *rawScan) member() (name []byte, ok bool) {
	s.ws()
	if s.b[s.i] == ',' {
		s.i++
		s.ws()
	}
	if s.b[s.i] == '}' {
		s.i++
		return nil, false
	}
	q, esc := s.str()
	s.ws()
	s.i++ // ':'
	if esc {
		return nil, true // never a plain name
	}
	return q[1 : len(q)-1], true
}

// elem advances to the next element of an open array; false at its end, which it consumes.
func (s *rawScan) elem() bool {
	s.ws()
	if s.b[s.i] == ',' {
		s.i++
		s.ws()
	}
	if s.b[s.i] == ']' {
		s.i++
		return false
	}
	return true
}

// plainName reports that a member name is lower-case ASCII without escapes. Decoding matches
// such a name to a field only when it is the field's name, whatever its case folding; any other
// name could match case-insensitively, so measurers give up on it.
func plainName(name []byte) bool {
	if name == nil {
		return false
	}
	for _, c := range name {
		if c >= utf8.RuneSelf || 'A' <= c && c <= 'Z' {
			return false
		}
	}
	return true
}

// strLen reads a string member into *n as its decoded length; null leaves *n as struct
// decoding leaves a string. It reports false for any other value.
func (s *rawScan) strLen(n *int) bool {
	switch s.peek() {
	case '"':
		*n = s.text()
		return true
	case 'n':
		s.skip()
		return true
	}
	return false
}

// strVal reads a string member into *v, decoded; null leaves *v as is.
func (s *rawScan) strVal(v *[]byte) bool {
	switch s.peek() {
	case '"':
		*v = unquoted(s.str())
		return true
	case 'n':
		s.skip()
		return true
	}
	return false
}

// isNull consumes a null if one is next.
func (s *rawScan) isNull() bool {
	if s.peek() == 'n' {
		s.skip()
		return true
	}
	return false
}

// unquoted decodes a string as unquote does. Strings measurers keep (roles, types, call IDs) are
// short and seldom escaped; only those escaped or not valid UTF-8 are copied.
func unquoted(q []byte, esc bool) []byte {
	inner := q[1 : len(q)-1]
	if !esc && utf8.Valid(inner) {
		return inner
	}
	b, _ := jsontext.AppendUnquote(nil, q)
	return b
}

func hex4(h []byte) rune {
	var r rune
	for _, c := range h[:4] {
		switch {
		case c <= '9':
			c -= '0'
		case c <= 'F':
			c -= 'A' - 10
		default:
			c -= 'a' - 10
		}
		r = r<<4 | rune(c)
	}
	return r
}

// compactLen is len(compactString(v)): v without the whitespace outside its strings.
func compactLen(v []byte) int {
	n := 0
	for i := 0; i < len(v); i++ {
		switch v[i] {
		case ' ', '\t', '\r', '\n':
		case '"':
			s := rawScan{b: v, i: i}
			s.str()
			n += s.i - i
			i = s.i - 1
		default:
			n++
		}
	}
	return n
}

// compactValLen reads a value as readCompact does, returning its length; null is "".
func (s *rawScan) compactValLen() int {
	if s.isNull() {
		return 0
	}
	return compactLen(s.value())
}

// textLen reads a value jsonText decodes, returning its length and whether it was set.
func (s *rawScan) jsonTextLen() (int, bool) {
	switch s.peek() {
	case 'n':
		s.skip()
		return 0, false
	case '"':
		return s.text(), true
	}
	return compactLen(s.value()), true
}

// intVal reads a member struct decoding puts in an int: an integer that fits, or null.
func (s *rawScan) intVal() bool {
	if s.isNull() {
		return true
	}
	v := s.value()
	if len(v) > 0 && v[0] == '-' {
		v = v[1:]
	}
	if len(v) == 0 || len(v) > 18 {
		return false
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// boolVal reads a member struct decoding puts in a bool.
func (s *rawScan) boolVal() bool {
	switch s.peek() {
	case 't', 'f', 'n':
		s.skip()
		return true
	}
	return false
}

// addID records a call ID, decoded, of the message being measured.
func (sk *skimmer) addID(id []byte) { sk.ids = append(sk.ids, id) }

// object reads an object, handing member each name, which must consume the member's value. It
// reports false when the value is not an object, a name is not plain, or member reports false.
func (s *rawScan) object(member func(name []byte) bool) bool {
	if s.peek() != '{' {
		return false
	}
	s.open()
	for {
		name, ok := s.member()
		if !ok {
			return true
		}
		if !plainName(name) || !member(name) {
			return false
		}
	}
}

// array reads an array, calling elem to consume each element; false when the value is not an
// array or elem reports false.
func (s *rawScan) array(elem func() bool) bool {
	if s.peek() != '[' {
		return false
	}
	s.open()
	for s.elem() {
		if !elem() {
			return false
		}
	}
	return true
}
