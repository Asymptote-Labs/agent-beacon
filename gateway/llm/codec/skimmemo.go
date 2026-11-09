package codec

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"encoding/json/jsontext"
	"errors"
	"hash/maphash"
	"math"
	"math/bits"
	"slices"
	"sync"
	"unsafe"

	"github.com/asymptote-labs/agent-beacon/gateway/llm"
)

// Memoized skimming. A session's calls resend its history byte for byte, and with it the same
// tool catalog and system prompt, so almost all of a request is content this process decoded on
// the session's previous call. What skimming a message produced (its role, measured volume and
// call IDs, its decoded parts for a system message, or that the measurer gave up on it), and the
// decoded value of each resent top-level member (the tools; Anthropic's system, Responses'
// instructions), is kept in a process-wide cache keyed by a seeded hash of the bytes, and a
// request whose content hits the cache is decoded without reading that content again.
//
// The decoder cannot skip a value unread, so a memoized skim cuts content out of the body
// instead (planSkim). A boundary scan finds the top-level members and the extent of each
// element of the message array. The rest of the body, with the array
// replaced by a placeholder "[]" and each cut member's value by a placeholder null (the shell),
// is decoded as usual. The elements are taken from the extents: from the cache on a hit,
// otherwise validated with jsontext and skimmed as before, then cached. A cut member's value is
// the cached one on a hit, otherwise its extent decoded alone, as the request decoder decodes
// the member, then cached.
//
// Why a planned skim accepts exactly the bodies the plain skim accepts, and decodes them alike:
//   - The shell is the body's bytes outside what was cut, verbatim, around the placeholders.
//     jsontext decodes it in full, and the skim checks that the decoder reaches each placeholder
//     exactly once, as the value of the member it was cut from, at its offset. So each
//     placeholder stands for the value of that one top-level member, whatever the boundary scan
//     believed, and the body is valid if and only if the values cut out are.
//   - A cut member's value is a complete valid value that decodes to what is used: on a miss it
//     is decoded alone (which validates it as one complete value) and cut only if that succeeds;
//     on a hit its bytes hash, under a process-random seed for its protocol and kind, to bytes
//     that so decoded earlier. A decode that fails leaves the member in the shell, to fail there.
//   - The array's own syntax (brackets, whitespace, commas, no trailing comma) is checked by the
//     boundary scan exactly as jsontext checks it; the scan gives up on anything else.
//   - Each element extent is a complete valid value: on a miss jsontext validates the extent
//     alone (Value.IsValid with the decoder's options) before it is skimmed or cached, so the
//     cache only ever holds extents that are complete valid values; on a hit, the extent's
//     bytes hash to bytes validated earlier. An extent a boundary scan confused by adversarial
//     input cuts out is not a complete value, so it misses (barring a 64-bit seeded-hash
//     collision, which also needs equal lengths) and fails validation.
//   - Valid elements joined by valid separators make a valid array whose elements are exactly
//     those extents: tokenizing a concatenation of complete values is tokenizing each.
//   - Nesting: in the body, members sit one level deep and elements two. The scan gives up on
//     any value nested deeper than maxPlanDepth, far inside jsontext's limit, so validating a
//     value alone never accepts what the body's depth would reject.
// Whenever any of this fails, or the planned decode reports any error, SkimRequest skims the
// original body plainly, which also reports the error a plain skim reports.
//
// Cached decoded values (tools, system parts) are shared by every request that hits them: they
// are immutable, as llm.Request documents.

// memoKind is what a cache entry holds; each kind hashes under its own seed.
type memoKind uint8

const (
	memoMessage memoKind = iota // a message array element
	memoTools                   // a top-level tools array, decoded
	memoSystem                  // a top-level system prompt, decoded
	memoKinds
)

// memoSeeds are the cache's hash seeds, by protocol and kind, since what content decodes to
// depends on both. They are random per process, so collisions cannot be aimed at.
type memoSeedSet [memoKinds]maphash.Seed

var memoSeeds = map[llm.Protocol]*memoSeedSet{
	llm.AnthropicMessages: newMemoSeeds(),
	llm.OpenAIChat:        newMemoSeeds(),
	llm.OpenAIResponses:   newMemoSeeds(),
}

func newMemoSeeds() *memoSeedSet {
	var s memoSeedSet
	for i := range s {
		s[i] = maphash.MakeSeed()
	}
	return &s
}

// arrayMember names the top-level member holding a protocol's messages.
func arrayMember(p llm.Protocol) string {
	if p == llm.OpenAIResponses {
		return "input"
	}
	return "messages"
}

// memoMember is a resent top-level member whose decoded value is cached: decode decodes its
// value alone, to what the request decoder makes of it.
type memoMember struct {
	name   string
	kind   memoKind
	decode func(raw []byte) (any, error)
}

var memoMembers = map[llm.Protocol][]memoMember{
	llm.AnthropicMessages: {{"tools", memoTools, decodeAnthropicTools}, {"system", memoSystem, decodeAnthropicSystem}},
	llm.OpenAIChat:        {{"tools", memoTools, decodeChatTools}},
	llm.OpenAIResponses:   {{"tools", memoTools, decodeResponsesTools}, {"instructions", memoSystem, decodeResponsesInstructions}},
}

// The cache holds up to memoCapacity entries and about memoMaxBytes in memoShards shards; a
// shard past its share of either is reset. An entry is charged memoEntryCost (an ~100-byte
// value, its key and map overhead), plus 16 bytes and the ID per call ID it holds, plus the
// length of the JSON it decoded when it holds a decoded value (tools, system parts), which the
// decoded value approximates. A value too large for a shard's share is not cached.
const (
	memoShards     = 64
	memoCapacity   = 1 << 18
	memoMaxBytes   = 256 << 20
	memoShardCap   = memoCapacity / memoShards
	memoShardBytes = memoMaxBytes / memoShards
	memoMaxValue   = memoShardBytes / 4
	memoEntryCost  = 128
)

// memoMsg is one measured message: its role, its volume on messageChannels (items, bytes), its
// call IDs, and, once decoded, a system message's parts.
type memoMsg struct {
	vol      [len(messageChannels)][2]uint32
	ids      []string
	parts    []llm.Part
	hasParts bool
	role     uint8 // index in memoRoles
}

var memoRoles = [...]llm.Role{llm.RoleSystem, llm.RoleUser, llm.RoleAssistant, llm.RoleTool}

// memoEntry is what skimming one element produced, or a member's decoded value.
type memoEntry struct {
	size   uint32 // the content's length, checked on lookup
	cost   uint32 // bytes charged to the shard
	n      uint8  // the messages an element stands for: 0 to 2
	gaveUp bool   // the measurer gave up on the element; it is decoded
	first  memoMsg
	second *memoMsg
	value  any // a member's decoded value
}

type memoShard struct {
	mu    sync.RWMutex
	m     map[uint64]memoEntry
	bytes int
	_     [32]byte // keeps shards' locks on separate cache lines
}

var skimMemo [memoShards]memoShard

func memoShardOf(key uint64) *memoShard { return &skimMemo[key>>(64-6)] }

func memoGet(key uint64, size int) (memoEntry, bool) {
	sh := memoShardOf(key)
	sh.mu.RLock()
	e, ok := sh.m[key]
	sh.mu.RUnlock()
	return e, ok && int(e.size) == size
}

func memoPut(key uint64, e memoEntry) {
	e.cost += memoEntryCost + e.first.idsCost()
	if e.second != nil {
		e.cost += e.second.idsCost()
	}
	sh := memoShardOf(key)
	sh.mu.Lock()
	if old, ok := sh.m[key]; ok {
		sh.bytes -= int(old.cost)
	}
	if sh.m == nil || len(sh.m) >= memoShardCap || sh.bytes+int(e.cost) > memoShardBytes {
		sh.m, sh.bytes = make(map[uint64]memoEntry), 0
	}
	sh.m[key] = e
	sh.bytes += int(e.cost)
	sh.mu.Unlock()
}

func (m *memoMsg) idsCost() uint32 {
	n := 0
	for _, id := range m.ids {
		n += 16 + len(id)
	}
	return uint32(n)
}

// resetSkimMemo empties the cache.
func resetSkimMemo() {
	for i := range skimMemo {
		sh := &skimMemo[i]
		sh.mu.Lock()
		sh.m, sh.bytes = nil, 0
		sh.mu.Unlock()
	}
}

var errInvalidElement = errors.New("skim: invalid message")

// add skims one array element. validated reports that the decoder already validated it in
// place; an element cut out by a plan is validated here, on a miss, before anything else.
func (sk *skimmer) add(raw []byte, validated bool) error {
	if len(raw) > math.MaxUint32 {
		return sk.skim(raw, 0, false)
	}
	key := maphash.Bytes(sk.seed, raw)
	if e, ok := memoGet(key, len(raw)); ok {
		return sk.replay(&e, raw)
	}
	if !validated && !jsontext.Value(raw).IsValid(decodeOptions) {
		return errInvalidElement
	}
	return sk.skim(raw, key, true)
}

// skim measures or decodes a valid element, caching the result when remember is set.
func (sk *skimmer) skim(raw []byte, key uint64, remember bool) error {
	sk.raw = raw
	sk.vols = sk.vols[:0]
	msgs, ids, measured := len(sk.msgs), len(sk.ids), sk.measured
	if sk.codec.measure(sk, raw) {
		if remember {
			sk.remember(key, raw, msgs)
		}
		return nil
	}
	sk.msgs, sk.ids, sk.measured = sk.msgs[:msgs], sk.ids[:ids], measured
	if err := sk.parse(raw); err != nil {
		return err
	}
	if remember {
		memoPut(key, memoEntry{size: uint32(len(raw)), gaveUp: true})
	}
	return nil
}

// remember caches the messages measuring raw appended, sk.msgs[lo:], unless one does not fit an
// entry.
func (sk *skimmer) remember(key uint64, raw []byte, lo int) {
	e := memoEntry{size: uint32(len(raw)), n: uint8(len(sk.msgs) - lo)}
	if e.n > 2 {
		return
	}
	for k := range int(e.n) {
		m := &sk.msgs[lo+k]
		mm := memoMsg{role: math.MaxUint8}
		for r, role := range memoRoles {
			if role == m.role {
				mm.role = uint8(r)
			}
		}
		if mm.role == math.MaxUint8 {
			return
		}
		for c, v := range sk.vols[k] {
			if v.Items < 0 || v.Bytes < 0 || v.Items > math.MaxUint32 || v.Bytes > math.MaxUint32 {
				return
			}
			mm.vol[c] = [2]uint32{uint32(v.Items), uint32(v.Bytes)}
		}
		if m.idHi > m.idLo {
			mm.ids = make([]string, m.idHi-m.idLo)
			for i, id := range sk.ids[m.idLo:m.idHi] {
				mm.ids[i] = string(id)
			}
		}
		if k == 0 {
			e.first = mm
		} else {
			e.second = &mm
		}
	}
	memoPut(key, e)
}

// rememberParts adds to the cached entry of a measured element the parts its system messages
// decoded to, sk.msgs[lo:hi], so that the next request hitting it need not decode it. Only
// elements of at most memoMaxValue bytes are charged with their parts.
func (sk *skimmer) rememberParts(raw []byte, lo, hi int) {
	if len(raw) > memoMaxValue {
		return
	}
	key := maphash.Bytes(sk.seed, raw)
	e, ok := memoGet(key, len(raw))
	if !ok || e.gaveUp || int(e.n) != hi-lo {
		return
	}
	if e.second != nil {
		second := *e.second
		e.second = &second
	}
	for k := range hi - lo {
		m := &e.first
		if k == 1 {
			m = e.second
		}
		if memoRoles[m.role] == llm.RoleSystem {
			m.parts, m.hasParts = sk.parts[sk.msgs[lo+k].parts-1], true
		}
	}
	e.cost = uint32(len(raw))
	memoPut(key, e)
}

// replay appends the messages a cached element stands for, as measuring it appended them.
func (sk *skimmer) replay(e *memoEntry, raw []byte) error {
	if e.gaveUp {
		return sk.parse(raw)
	}
	for k := range int(e.n) {
		m := &e.first
		if k == 1 {
			m = e.second
		}
		lo := len(sk.ids)
		for _, id := range m.ids {
			sk.ids = append(sk.ids, unsafe.Slice(unsafe.StringData(id), len(id)))
		}
		for c, v := range m.vol {
			sk.measured[c].Items += int(v[0])
			sk.measured[c].Bytes += int(v[1])
		}
		msg := skimMsg{role: memoRoles[m.role], raw: raw, idLo: int32(lo), idHi: int32(len(sk.ids)), measured: true}
		if m.hasParts {
			sk.parts = append(sk.parts, m.parts)
			msg.parts = int32(len(sk.parts))
		}
		sk.msgs = append(sk.msgs, msg)
	}
	return nil
}

// skimPlan is a body cut for a memoized skim.
type skimPlan struct {
	shell []byte // the body with what was cut replaced by placeholders
	// cutMessages reports that the message array was cut: its elements are elems, extents of the
	// body, and its placeholder "[]" ends at messagesEnd in the shell.
	cutMessages bool
	messagesEnd int
	elems       [][]byte
	cuts        []skimCut
}

// skimCut is a member whose value was cut out for its cached or separately decoded value.
type skimCut struct {
	name           string
	placeholderEnd int // in the shell, just past its placeholder null
	value          any
	visited        bool
}

// visited reports that the decode reached every placeholder.
func (p *skimPlan) visited(in *skimInput) bool {
	if p.cutMessages && in.visits != 1 {
		return false
	}
	for i := range p.cuts {
		if !p.cuts[i].visited {
			return false
		}
	}
	return true
}

// maxPlanDepth bounds a cut value's nesting; jsontext allows 10000 in the body.
const maxPlanDepth = 1000

// span is an extent of the body being cut, with its placeholder.
type span struct {
	start, end  int
	placeholder string
	cut         int // index in cuts, or -1 for the message array
}

// planSkim cuts body for a memoized skim of protocol p, or returns nil when there is nothing
// worth cutting or the body does not look like an object the scan follows exactly. It never
// decides validity; see the soundness notes above.
func planSkim(body []byte, p llm.Protocol, seeds *memoSeedSet) *skimPlan {
	members, array := memoMembers[p], arrayMember(p)
	var (
		spans  []span
		plan   skimPlan
		seen   uint
		arrayI = -1
	)
	i := skipWS(body, 0)
	if i >= len(body) || body[i] != '{' {
		return nil
	}
	for i++; ; {
		if i = skipWS(body, i); i >= len(body) || body[i] != '"' {
			return nil
		}
		nameStart := i
		if i, _ = scanString(body, i); i < 0 {
			return nil
		}
		name := body[nameStart+1 : i-1]
		if i = skipWS(body, i); i >= len(body) || body[i] != ':' {
			return nil
		}
		if i = skipWS(body, i+1); i >= len(body) {
			return nil
		}
		start := i
		switch {
		case string(name) == array && body[i] == '[':
			if arrayI >= 0 {
				return nil // a repeated array is the decoder's to resolve
			}
			arrayI = start
			// The array is cut whether or not its messages are cached: the shell then holds
			// only the rest of the body, and the elements are validated one by one instead.
			plan.elems, i = scanArray(body, start)
			if i < 0 {
				return nil
			}
			plan.cutMessages = true
			spans = append(spans, span{start, i, "[]", -1})
		default:
			end, depth := scanValue(body, i)
			if end < 0 || depth > maxPlanDepth {
				return nil
			}
			i = end
			for k, m := range members {
				if string(name) != m.name {
					continue
				}
				if seen&(1<<k) != 0 {
					return nil // a repeated member is the decoder's to resolve
				}
				seen |= 1 << k
				if v, ok := memberValue(body[start:end], m, seeds); ok {
					spans = append(spans, span{start, end, "null", len(plan.cuts)})
					plan.cuts = append(plan.cuts, skimCut{name: m.name, value: v})
				}
			}
		}
		if i = skipWS(body, i); i >= len(body) {
			return nil
		}
		if body[i] == '}' {
			break
		}
		if body[i] != ',' {
			return nil
		}
		i++
	}
	if len(spans) == 0 {
		return nil
	}
	slices.SortFunc(spans, func(a, b span) int { return cmp.Compare(a.start, b.start) })
	n, last := len(body), 0
	for _, s := range spans {
		n += len(s.placeholder) - (s.end - s.start)
	}
	plan.shell = make([]byte, 0, n)
	for _, s := range spans {
		plan.shell = append(append(plan.shell, body[last:s.start]...), s.placeholder...)
		last = s.end
		if s.cut < 0 {
			plan.messagesEnd = len(plan.shell)
		} else {
			plan.cuts[s.cut].placeholderEnd = len(plan.shell)
		}
	}
	plan.shell = append(plan.shell, body[last:]...)
	if !plan.cutMessages {
		plan.elems = nil
	}
	return &plan
}

// memberValue is the decoded value of member m's raw value: cached, or decoded alone and
// cached. It reports false when raw does not decode, so that the member is left to the decoder.
func memberValue(raw []byte, m memoMember, seeds *memoSeedSet) (any, bool) {
	key := maphash.Bytes(seeds[m.kind], raw)
	if e, ok := memoGet(key, len(raw)); ok {
		return e.value, true
	}
	v, err := m.decode(raw)
	if err != nil {
		return nil, false
	}
	if len(raw) <= memoMaxValue {
		memoPut(key, memoEntry{size: uint32(len(raw)), cost: uint32(len(raw)), value: v})
	}
	return v, true
}

// scanArray scans the array at body[start], returning its elements and the offset just past it,
// or -1.
func scanArray(body []byte, start int) (elems [][]byte, end int) {
	i := skipWS(body, start+1)
	if i < len(body) && body[i] == ']' {
		return nil, i + 1
	}
	for {
		if i >= len(body) {
			return nil, -1
		}
		end, depth := scanValue(body, i)
		if end < 0 || depth > maxPlanDepth {
			return nil, -1
		}
		elems = append(elems, body[i:end])
		if i = skipWS(body, end); i >= len(body) {
			return nil, -1
		}
		switch body[i] {
		case ',':
			i = skipWS(body, i+1) // a value must follow: a ']' here fails scanValue
		case ']':
			return elems, i + 1
		default:
			return nil, -1
		}
	}
}

// errPlan reports a planned skim whose placeholders are not where the plan put them.
var errPlan = errors.New("skim: the request is not where the plan cut it")

// member reports whether the plan cut member name, returning its value. It reads the member's
// placeholder, which must be where the plan put it and read once.
func (in *skimInput) member(dec *jsontext.Decoder, name string) (any, bool, error) {
	if in == nil || in.plan == nil {
		return nil, false, nil
	}
	for i := range in.plan.cuts {
		c := &in.plan.cuts[i]
		if c.name != name {
			continue
		}
		v, err := dec.ReadValue()
		if err != nil || string(v) != "null" || dec.InputOffset() != int64(c.placeholderEnd) || c.visited {
			return nil, true, errPlan
		}
		c.visited = true
		return c.value, true, nil
	}
	return nil, false, nil
}

func skipWS(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\r', '\n':
			i++
		default:
			return i
		}
	}
	return i
}

// scanString returns the offset just past the string at b[i], or -1 if it does not end, and
// whether it holds an escape. It follows quotes and escapes only; jsontext validates the rest.
// A quote ends the string unless an odd run of backslashes precedes it, the rule each backslash
// escaping the next byte gives; text escapes (\n, \t) then cost nothing, as the scan jumps from
// quote to quote.
func scanString(b []byte, i int) (end int, esc bool) {
	start := i + 1
	// Most strings are member names and short values: try a few words before searching.
	for i = start; i+8 <= len(b) && i < start+32; i += 8 {
		w := binary.LittleEndian.Uint64(b[i:])
		if m := quoteOrEscape(w); m != 0 {
			if p := bits.TrailingZeros64(m) >> 3; byte(w>>(p*8)) == '"' {
				return i + p + 1, false
			}
			break // an escape: the search below follows it
		}
	}
	for i = start; ; {
		k := bytes.IndexByte(b[i:], '"')
		if k < 0 {
			return -1, true
		}
		q := i + k
		j := q
		for j > start && b[j-1] == '\\' {
			j--
		}
		if (q-j)%2 == 0 {
			return q + 1, bytes.IndexByte(b[start:q], '\\') >= 0
		}
		i = q + 1
	}
}

// scanValue returns the offset just past the value at b[i] and its nesting depth, or -1. It
// follows strings and brackets only; what lies between is for jsontext to validate.
func scanValue(b []byte, i int) (end, depth int) {
	switch b[i] {
	case '"':
		end, _ = scanString(b, i)
		return end, 0
	case '{', '[':
		level := 0
		for i < len(b) {
			switch b[i] {
			case '"':
				if i, _ = scanString(b, i); i < 0 {
					return -1, 0
				}
				continue
			case '{', '[':
				level++
				depth = max(depth, level)
			case '}', ']':
				if level--; level == 0 {
					return i + 1, depth
				}
			}
			i++
		}
		return -1, 0
	case ',', ']', '}', ':':
		return -1, 0
	}
	start := i
	for i < len(b) {
		switch b[i] {
		case ',', ']', '}', ' ', '\t', '\r', '\n':
			return i, 0
		}
		i++
	}
	if i == start {
		return -1, 0
	}
	return i, 0
}
