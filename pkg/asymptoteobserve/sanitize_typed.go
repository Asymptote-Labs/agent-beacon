package asymptoteobserve

import (
	"encoding"
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// This file is how SanitizeEvent (privacy.go) sanitizes the typed parts of an event and decides
// whether it is too large, without the JSON round trips it used:
//
//   - MCP, SystemContext and GenAI were sanitized by sanitizeTyped: marshal the value, decode it
//     into a map, run SanitizeMap over the map, marshal the map, and decode it back into the type.
//     Each field is now converted directly into what that round trip leaves of it
//     (genericSanitizer), and the round trip itself runs only for a value the conversion does not
//     know, or one the round trip fails on, so its fallback is kept too.
//   - Truncated was set by marshaling the whole event and comparing the length with maxBytes. An
//     upper bound on that length is now added up first (jsonSizeBound), and the event is marshaled
//     only when the bound exceeds maxBytes.
//
// sanitize_typed_test.go checks the conversion's effects one by one (TestSanitizeEventTypedFields),
// that it copies every field of the types it converts (TestTypedConversionKeepsEveryField), that
// the bound never undercounts (TestJSONSizeBoundIsABound), and that Truncated is set exactly when
// the marshaled event exceeds maxBytes (TestSanitizeEventTruncatesAtItsMarshaledSize).

// genericSanitizer converts typed fields and the values interface{} fields hold into what
// sanitizeTyped's round trip leaves of them, sanitized the way SanitizeMap and SanitizeSlice
// sanitize the decoded value:
//
//   - A string passes through JSON with each byte of invalid UTF-8 replaced by U+FFFD, as the
//     encoder writes it, and is then sanitized.
//   - A number in an interface{} decodes as float64, and a float32 as the float64 nearest its
//     shortest decimal form. An integer field reads back the float64 as the encoder writes it, its
//     shortest decimal form, so beyond 2^53 it changes value (2^63-1024 comes back as
//     9223372036854775000), and where that form is out of range (from about 2^63-512 up, and at
//     -2^63) the decode fails, which fails the round trip.
//   - NaN and infinities fail the marshal, which fails the round trip.
//   - A nil map, slice or pointer, in an interface{} or a field, and an omitempty field that is
//     empty, decode as nil; an empty map or slice in an interface{} stays empty, not nil. A non-nil
//     pointer to an empty struct decodes as an empty struct.
//   - `json:"-"` fields (ResultText, SystemInstructionsText) are not written, so they come back
//     empty.
//
// failed is set for anything it does not convert (a value of another type, a map key that is not
// valid UTF-8, which the encoder can merge with another, nesting deeper than maxGenericDepth) and
// for what fails the round trip; the caller then runs sanitizeTyped, which handles each of these
// as it always did.
type genericSanitizer struct {
	limit  int
	failed bool
}

// maxGenericDepth bounds the nesting genericSanitizer converts and jsonSizeBound adds up. The
// decoder fails beyond 10000 levels and the encoder detects cycles after 1000.
const maxGenericDepth = 100

// text is a typed string field, none of which is named for a credential
// (TestTypedFieldNamesAreNotSecret).
func (g *genericSanitizer) text(s string) string {
	return CleanString(jsonString(s), g.limit, true)
}

// jsonString is s as the encoder writes it and the decoder reads it back: each byte that is not
// part of a valid UTF-8 encoding becomes U+FFFD.
func jsonString(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteString("\uFFFD")
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// integer is an integer field's value after the round trip.
func (g *genericSanitizer) integer(v int64) int64 {
	if -1<<53 <= v && v <= 1<<53 {
		return v
	}
	n, err := strconv.ParseInt(strconv.FormatFloat(float64(v), 'f', -1, 64), 10, 64)
	if err != nil {
		g.failed = true
	}
	return n
}

func (g *genericSanitizer) intp(p *int) *int {
	if p == nil {
		return nil
	}
	v := int(g.integer(int64(*p)))
	return &v
}

func (g *genericSanitizer) int64p(p *int64) *int64 {
	if p == nil {
		return nil
	}
	v := g.integer(*p)
	return &v
}

func (g *genericSanitizer) float64p(p *float64) *float64 {
	if p == nil {
		return nil
	}
	if math.IsNaN(*p) || math.IsInf(*p, 0) {
		g.failed = true
		return nil
	}
	v := *p
	return &v
}

func boolp(p *bool) *bool {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// strings is a []string field, which SanitizeSlice sanitizes: none is named for a credential.
func (g *genericSanitizer) strings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		s = jsonString(s)
		if s != "" && i > 0 && IsSecretFlag(jsonString(in[i-1])) {
			out[i] = redactedValue
			continue
		}
		out[i] = CleanString(s, g.limit, true)
	}
	return out
}

// field is the value of an interface{} field, none of which is named for a credential.
func (g *genericSanitizer) field(v any) any {
	return g.value(v, "", 1)
}

// value converts v, held under key in a map, and sanitizes it as SanitizeMap does.
func (g *genericSanitizer) value(v any, key string, depth int) any {
	if depth > maxGenericDepth {
		g.failed = true
		return nil
	}
	switch x := v.(type) {
	case string:
		x = jsonString(x)
		if x != "" && IsSecretField(key, x) {
			return redactedValue
		}
		return CleanString(x, g.limit, true)
	case []any, []string:
		items, ok := g.items(x, depth)
		if !ok || items == nil {
			return nil
		}
		if IsSecretKeyName(key) {
			return g.redactAll(items, depth)
		}
		return g.slice(items, depth)
	}
	return g.scalarOrObject(v, depth)
}

// scalarOrObject converts v when it is neither a string nor a list, which SanitizeMap,
// SanitizeSlice and redactAllStrings all treat the same.
func (g *genericSanitizer) scalarOrObject(v any, depth int) any {
	switch x := v.(type) {
	case nil:
		return nil
	case map[string]any:
		if x == nil {
			return nil
		}
		out := make(map[string]any, len(x))
		for k, item := range x {
			if !utf8.ValidString(k) {
				g.failed = true
				return nil
			}
			out[k] = g.value(item, k, depth+1)
		}
		return out
	case map[string]string:
		if x == nil {
			return nil
		}
		out := make(map[string]any, len(x))
		for k, item := range x {
			if !utf8.ValidString(k) {
				g.failed = true
				return nil
			}
			out[k] = g.value(item, k, depth+1)
		}
		return out
	case bool:
		return x
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			g.failed = true
			return nil
		}
		return x
	case float32:
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			g.failed = true
			return nil
		}
		f, _ := strconv.ParseFloat(strconv.FormatFloat(float64(x), 'g', -1, 32), 64)
		return f
	case int:
		return float64(x)
	case int8:
		return float64(x)
	case int16:
		return float64(x)
	case int32:
		return float64(x)
	case int64:
		return float64(x)
	case uint:
		return float64(x)
	case uint8:
		return float64(x)
	case uint16:
		return float64(x)
	case uint32:
		return float64(x)
	case uint64:
		return float64(x)
	}
	g.failed = true
	return nil
}

// items returns a list as it decodes: nil for a nil list, its items otherwise, with strings as
// the encoder writes them.
func (g *genericSanitizer) items(v any, depth int) ([]any, bool) {
	if depth > maxGenericDepth {
		g.failed = true
		return nil, false
	}
	switch x := v.(type) {
	case []any:
		if x == nil {
			return nil, true
		}
		out := make([]any, len(x))
		for i, item := range x {
			if s, ok := item.(string); ok {
				item = jsonString(s)
			}
			out[i] = item
		}
		return out, true
	case []string:
		if x == nil {
			return nil, true
		}
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = jsonString(item)
		}
		return out, true
	}
	return nil, false
}

// slice is SanitizeSlice over decoded items. A flag named for a secret is read from the item as
// decoded, before it is sanitized in place.
func (g *genericSanitizer) slice(items []any, depth int) []any {
	var prev any
	for i, item := range items {
		previous := prev
		prev = item
		switch x := item.(type) {
		case string:
			if x != "" && i > 0 && followsSecretFlag(previous) {
				items[i] = redactedValue
				continue
			}
			items[i] = CleanString(x, g.limit, true)
		case []any, []string:
			nested, ok := g.items(x, depth+1)
			if !ok || nested == nil {
				items[i] = nil
				continue
			}
			items[i] = g.slice(nested, depth+1)
		default:
			items[i] = g.scalarOrObject(item, depth+1)
		}
	}
	return items
}

// redactAll is redactAllStrings over decoded items.
func (g *genericSanitizer) redactAll(items []any, depth int) []any {
	for i, item := range items {
		switch x := item.(type) {
		case string:
			if x != "" {
				items[i] = redactedValue
			}
		case []any, []string:
			nested, ok := g.items(x, depth+1)
			if !ok || nested == nil {
				items[i] = nil
				continue
			}
			items[i] = g.redactAll(nested, depth+1)
		default:
			items[i] = g.scalarOrObject(item, depth+1)
		}
	}
	return items
}

func sanitizeMCP(in *MCPInfo) *MCPInfo {
	g := genericSanitizer{limit: DefaultRawStringLimit}
	out := &MCPInfo{Server: g.text(in.Server), Tool: g.text(in.Tool)}
	if in.Method != nil {
		out.Method = &MCPMethodInfo{Name: g.text(in.Method.Name)}
	}
	if in.Protocol != nil {
		out.Protocol = &MCPProtocolInfo{Version: g.text(in.Protocol.Version)}
	}
	if in.Resource != nil {
		out.Resource = &MCPResourceInfo{URI: g.text(in.Resource.URI)}
	}
	if in.Session != nil {
		out.Session = &MCPSessionInfo{ID: g.text(in.Session.ID)}
	}
	return out
}

func sanitizeSystemContext(in *SystemContextInfo) *SystemContextInfo {
	g := genericSanitizer{limit: DefaultRawStringLimit}
	out := &SystemContextInfo{Kind: g.text(in.Kind), Source: g.text(in.Source)}
	if len(in.Skills) > 0 {
		out.Skills = make([]SkillRefInfo, len(in.Skills))
		for i, s := range in.Skills {
			out.Skills[i] = SkillRefInfo{SkillName: g.text(s.SkillName), SkillNameHash: g.text(s.SkillNameHash)}
		}
	}
	return out
}

// sanitizeGenAI is sanitizeTyped for GenAI, except that a converted tool's Description is left for
// SanitizeEvent to fill.
func sanitizeGenAI(in *GenAIInfo) *GenAIInfo {
	g := genericSanitizer{limit: DefaultRawStringLimit}
	out := &GenAIInfo{}
	if a := in.Agent; a != nil {
		out.Agent = &GenAIAgentInfo{Description: g.text(a.Description), ID: g.text(a.ID), Name: g.text(a.Name), Version: g.text(a.Version)}
	}
	if c := in.Context; c != nil {
		out.Context = &GenAIContextInfo{UsedTokens: g.int64p(c.UsedTokens), LimitTokens: g.int64p(c.LimitTokens)}
	}
	if c := in.Conversation; c != nil {
		out.Conversation = &GenAIConversationInfo{ID: g.text(c.ID)}
	}
	if d := in.DataSource; d != nil {
		out.DataSource = &GenAIDataSourceInfo{ID: g.text(d.ID)}
	}
	if e := in.Embeddings; e != nil {
		out.Embeddings = &GenAIEmbeddingsInfo{DimensionCount: g.intp(e.DimensionCount)}
	}
	if e := in.Evaluation; e != nil {
		out.Evaluation = &GenAIEvaluationInfo{Explanation: g.text(e.Explanation), Name: g.text(e.Name)}
		if s := e.Score; s != nil {
			out.Evaluation.Score = &GenAIEvaluationScoreInfo{Label: g.text(s.Label), Value: g.float64p(s.Value)}
		}
	}
	if i := in.Input; i != nil {
		out.Input = &GenAIInputInfo{Messages: g.field(i.Messages)}
	}
	if o := in.Operation; o != nil {
		out.Operation = &GenAIOperationInfo{Name: g.text(o.Name)}
	}
	if o := in.Output; o != nil {
		out.Output = &GenAIOutputInfo{Messages: g.field(o.Messages), Type: g.text(o.Type)}
	}
	if p := in.Prompt; p != nil {
		out.Prompt = &GenAIPromptInfo{Name: g.text(p.Name)}
	}
	if p := in.Provider; p != nil {
		out.Provider = &GenAIProviderInfo{Name: g.text(p.Name)}
	}
	if r := in.Request; r != nil {
		out.Request = &GenAIRequestInfo{
			ChoiceCount:      g.intp(r.ChoiceCount),
			EncodingFormats:  g.strings(r.EncodingFormats),
			FrequencyPenalty: g.float64p(r.FrequencyPenalty),
			MaxTokens:        g.intp(r.MaxTokens),
			Model:            g.text(r.Model),
			PresencePenalty:  g.float64p(r.PresencePenalty),
			Seed:             g.intp(r.Seed),
			StopSequences:    g.strings(r.StopSequences),
			Stream:           boolp(r.Stream),
			Temperature:      g.float64p(r.Temperature),
			TopK:             g.float64p(r.TopK),
			TopP:             g.float64p(r.TopP),
		}
	}
	if r := in.Response; r != nil {
		out.Response = &GenAIResponseInfo{
			FinishReasons:    g.strings(r.FinishReasons),
			ID:               g.text(r.ID),
			Model:            g.text(r.Model),
			TimeToFirstChunk: g.float64p(r.TimeToFirstChunk),
		}
	}
	if r := in.Retrieval; r != nil {
		out.Retrieval = &GenAIRetrievalInfo{Documents: g.field(r.Documents), QueryText: g.text(r.QueryText)}
	}
	out.SystemInstructions = g.field(in.SystemInstructions)
	if t := in.Token; t != nil {
		out.Token = &GenAITokenInfo{Type: g.text(t.Type)}
	}
	if t := in.Tool; t != nil {
		// Description is left empty: SanitizeEvent replaces a non-empty one, from the description as
		// given, and an empty one is empty either way.
		out.Tool = &GenAIToolInfo{Definitions: g.field(t.Definitions), Name: g.text(t.Name), Type: g.text(t.Type)}
		if c := t.Call; c != nil {
			out.Tool.Call = &GenAIToolCallInfo{Arguments: g.field(c.Arguments), ID: g.text(c.ID), Result: g.field(c.Result)}
		}
	}
	if u := in.Usage; u != nil {
		out.Usage = &GenAIUsageInfo{
			CostUSD:      g.float64p(u.CostUSD),
			InputTokens:  g.int64p(u.InputTokens),
			OutputTokens: g.int64p(u.OutputTokens),
		}
		if c := u.CacheCreation; c != nil {
			out.Usage.CacheCreation = &GenAIUsageCacheCreationInfo{InputTokens: g.int64p(c.InputTokens), Ephemeral1hInputTokens: g.int64p(c.Ephemeral1hInputTokens)}
		}
		if c := u.CacheRead; c != nil {
			out.Usage.CacheRead = &GenAIUsageCacheReadInfo{InputTokens: g.int64p(c.InputTokens)}
		}
		if r := u.Reasoning; r != nil {
			out.Usage.Reasoning = &GenAIUsageReasoningInfo{OutputTokens: g.int64p(r.OutputTokens)}
		}
	}
	if w := in.Workflow; w != nil {
		out.Workflow = &GenAIWorkflowInfo{Name: g.text(w.Name)}
	}
	if g.failed {
		return sanitizeTyped(in, PrivacyOptions{RedactSecrets: true, StringLimit: DefaultRawStringLimit})
	}
	return out
}

// ---- the size bound ----

// jsonSizeBound returns an upper bound on len(json.Marshal(v)), giving up (ok false) once it passes
// limit, or at a value whose encoding it does not bound: a type with its own marshaler, a map whose
// keys are not strings, a struct with embedded or ",string" fields, a channel or a function, or
// nesting deeper than maxGenericDepth. A string is bounded at six bytes a byte, which no escape
// exceeds, and a number at 32 bytes.
func jsonSizeBound(v any, limit int) (n int, ok bool) {
	b := sizeBounder{limit: limit}
	b.any(v, 0)
	return b.n, b.ok()
}

type sizeBounder struct {
	n, limit int
	failed   bool
}

func (b *sizeBounder) ok() bool { return !b.failed && b.n <= b.limit }

func (b *sizeBounder) str(n int) { b.n += 2 + 6*n }

// any bounds a value held in an interface{}: the shapes JSON decodes to directly, anything else by
// reflection.
func (b *sizeBounder) any(v any, depth int) {
	if !b.ok() {
		return
	}
	switch x := v.(type) {
	case nil:
		b.n += 4
	case string:
		b.str(len(x))
	case bool:
		b.n += 5
	case float64, int, int64:
		b.n += 32
	case map[string]any:
		b.object(x, depth)
	case []any:
		if x == nil {
			b.n += 4
			return
		}
		if depth > maxGenericDepth {
			b.failed = true
			return
		}
		b.n += 2
		for _, item := range x {
			b.n++
			b.any(item, depth+1)
		}
	default:
		rv := reflect.ValueOf(v)
		b.value(rv, boundOf(rv.Type()), depth)
	}
}

func (b *sizeBounder) object(m map[string]any, depth int) {
	if m == nil {
		b.n += 4
		return
	}
	if depth > maxGenericDepth {
		b.failed = true
		return
	}
	b.n += 2
	for k, item := range m {
		b.str(len(k))
		b.n += 2
		b.any(item, depth+1)
		if !b.ok() {
			return
		}
	}
}

var (
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
	genericMapType    = reflect.TypeFor[map[string]any]()
)

// typeBound is what bounding a type needs to know of it, worked out once per type, with the
// bounds of the types it holds linked in, so a walk looks a type up only where an interface{}
// holds it.
type typeBound struct {
	kind reflect.Kind
	// custom is set when the type, or a pointer to it, marshals itself, or is a struct whose
	// fields json flattens or quotes.
	custom bool
	// generic is set for map[string]any, walked without reflection.
	generic   bool
	elem, key *typeBound
	fields    []fieldBound
}

type fieldBound struct {
	index int
	name  int // the encoded name with its quotes and colon, plus a comma
	bound *typeBound
}

var (
	typeBounds   sync.Map // reflect.Type -> *typeBound, complete
	typeBoundsMu sync.Mutex
)

func boundOf(t reflect.Type) *typeBound {
	if tb, ok := typeBounds.Load(t); ok {
		return tb.(*typeBound)
	}
	typeBoundsMu.Lock()
	defer typeBoundsMu.Unlock()
	building := map[reflect.Type]*typeBound{}
	tb := buildBound(t, building)
	for t, tb := range building {
		typeBounds.Store(t, tb)
	}
	return tb
}

func buildBound(t reflect.Type, building map[reflect.Type]*typeBound) *typeBound {
	if tb, ok := typeBounds.Load(t); ok {
		return tb.(*typeBound)
	}
	if tb, ok := building[t]; ok {
		return tb
	}
	tb := &typeBound{
		kind: t.Kind(), generic: t == genericMapType,
		custom: t.Implements(jsonMarshalerType) || t.Implements(textMarshalerType) ||
			reflect.PointerTo(t).Implements(jsonMarshalerType) || reflect.PointerTo(t).Implements(textMarshalerType),
	}
	building[t] = tb
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		tb.elem = buildBound(t.Elem(), building)
	case reflect.Map:
		tb.key, tb.elem = buildBound(t.Key(), building), buildBound(t.Elem(), building)
	case reflect.Struct:
		for i := range t.NumField() {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, opts, _ := strings.Cut(tag, ",")
			if f.Anonymous || strings.Contains(opts, "string") {
				tb.custom = true
				break
			}
			if !f.IsExported() {
				continue
			}
			// The encoder falls back to the Go name for a tag name it rejects; the longer of the
			// two covers both.
			tagged, _ := json.Marshal(name)
			named, _ := json.Marshal(f.Name)
			tb.fields = append(tb.fields, fieldBound{index: i, name: max(len(tagged), len(named)) + 2, bound: buildBound(f.Type, building)})
		}
	}
	return tb
}

func (b *sizeBounder) value(v reflect.Value, tb *typeBound, depth int) {
	if !b.ok() {
		return
	}
	if depth > maxGenericDepth {
		b.failed = true
		return
	}
	switch tb.kind {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			b.n += 4
			return
		}
	}
	if tb.custom {
		b.failed = true
		return
	}
	switch tb.kind {
	case reflect.String:
		b.str(v.Len())
	case reflect.Bool:
		b.n += 5
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		b.n += 32
	case reflect.Pointer:
		b.value(v.Elem(), tb.elem, depth+1)
	case reflect.Interface:
		b.any(v.Interface(), depth+1)
	case reflect.Struct:
		b.n += 2
		for _, f := range tb.fields {
			b.n += f.name
			b.value(v.Field(f.index), f.bound, depth+1)
		}
	case reflect.Map:
		if v.IsNil() {
			b.n += 4
			return
		}
		if tb.generic {
			b.object(v.Interface().(map[string]any), depth)
			return
		}
		if tb.key.kind != reflect.String || tb.key.custom {
			b.failed = true
			return
		}
		b.n += 2
		for it := v.MapRange(); it.Next() && b.ok(); {
			b.str(it.Key().Len())
			b.n += 2
			b.value(it.Value(), tb.elem, depth+1)
		}
	case reflect.Slice, reflect.Array:
		if tb.kind == reflect.Slice && v.IsNil() {
			b.n += 4
			return
		}
		if tb.kind == reflect.Slice && tb.elem.kind == reflect.Uint8 && !tb.elem.custom {
			b.n += 2 + (v.Len()+2)/3*4
			return
		}
		b.n += 2
		for i := range v.Len() {
			b.n++
			b.value(v.Index(i), tb.elem, depth+1)
		}
	default:
		b.failed = true
	}
}
