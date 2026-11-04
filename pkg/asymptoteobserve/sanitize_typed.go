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
// whether it is too large:
//
//   - MCP, SystemContext and GenAI are converted field by field into what a JSON round trip
//     (marshal, decode into a map, SanitizeMap, marshal, decode back into the type) leaves of
//     them (genericSanitizer). A value JSON cannot represent, which made that round trip fail, is
//     replaced by nil, and everything else is sanitized as it is when the round trip succeeds.
//   - Truncated is set when the event's JSON is longer than maxBytes. An upper bound on that length
//     is added up first (jsonSizeBound), and the event is marshaled only when the bound exceeds
//     maxBytes.
//
// sanitize_typed_test.go checks the conversion's effects one by one (TestSanitizeEventTypedFields),
// that values JSON cannot represent never leave text unsanitized (TestSanitizeGenAIUnrepresentable),
// that it copies every field of the types it converts (TestTypedConversionKeepsEveryField), that
// the bound never undercounts (TestJSONSizeBoundIsABound), and that Truncated is set exactly when
// the marshaled event exceeds maxBytes (TestSanitizeEventTruncatesAtItsMarshaledSize).

// genericSanitizer converts typed fields and the values interface{} fields hold into what a JSON
// round trip leaves of them, sanitized with opts the way SanitizeMap and SanitizeSlice sanitize
// the decoded value:
//
//   - A string passes through JSON with each byte of invalid UTF-8 replaced by U+FFFD, as the
//     encoder writes it, and is then sanitized.
//   - A number in an interface{} decodes as float64, and a float32 as the float64 nearest its
//     shortest decimal form. An integer field reads back the float64 as the encoder writes it, its
//     shortest decimal form, so beyond 2^53 it changes value (2^63-1024 comes back as
//     9223372036854775000).
//   - A nil map, slice or pointer, in an interface{} or a field, and an omitempty field that is
//     empty, decode as nil; an empty map or slice in an interface{} stays empty, not nil. A non-nil
//     pointer to an empty struct decodes as an empty struct.
//   - `json:"-"` fields (ResultText, SystemInstructionsText) are not written, so they come back
//     empty.
//
// A value JSON cannot represent is replaced by nil: a NaN or infinite float, in a field or an
// interface{} value; an integer field whose float64 reads back out of range (from about 2^63-512
// up, and at -2^63); and a value the conversion does not handle itself (another type, a map whose
// keys are not valid UTF-8, which the encoder can merge) when encoding/json fails to marshal it or
// to decode it back (a channel, a function, a struct holding NaN, json.Number("1e400")). An
// interface{} field nested deeper than maxGenericDepth, or holding a cycle, is marshaled and
// decoded whole, and is nil when that fails. The result is what the round trip gives for the same
// value with those parts nil, so every string in it is cleaned as on the round trip.
type genericSanitizer struct {
	opts PrivacyOptions
}

func newGenericSanitizer(opts PrivacyOptions) genericSanitizer {
	if opts.StringLimit <= 0 {
		opts.StringLimit = DefaultStringLimit
	}
	return genericSanitizer{opts: opts}
}

// maxGenericDepth bounds the nesting genericSanitizer converts itself and jsonSizeBound adds up.
// The decoder fails beyond 10000 levels and the encoder detects cycles after 1000.
const maxGenericDepth = 100

// text is a typed string field, none of which is named for a credential
// (TestTypedFieldNamesAreNotSecret).
func (g genericSanitizer) text(s string) string {
	return CleanString(jsonString(s), g.opts.StringLimit, g.opts.RedactSecrets)
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

// integer is an integer field's value after the round trip, and false where it reads back out of
// range.
func integer(v int64) (int64, bool) {
	if -1<<53 <= v && v <= 1<<53 {
		return v, true
	}
	n, err := strconv.ParseInt(strconv.FormatFloat(float64(v), 'f', -1, 64), 10, 64)
	return n, err == nil
}

func intp(p *int) *int {
	if p == nil {
		return nil
	}
	// An int is never out of int's range, but the bound keeps the conversion below checked.
	n, ok := integer(int64(*p))
	if !ok || n < math.MinInt || n > math.MaxInt {
		return nil
	}
	v := int(n)
	return &v
}

func int64p(p *int64) *int64 {
	if p == nil {
		return nil
	}
	v, ok := integer(*p)
	if !ok {
		return nil
	}
	return &v
}

func float64p(p *float64) *float64 {
	if p == nil || math.IsNaN(*p) || math.IsInf(*p, 0) {
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
func (g genericSanitizer) strings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		s = jsonString(s)
		if g.opts.RedactSecrets && s != "" && i > 0 && IsSecretFlag(jsonString(in[i-1])) {
			out[i] = redactedValue
			continue
		}
		out[i] = CleanString(s, g.opts.StringLimit, g.opts.RedactSecrets)
	}
	return out
}

// field is the value of an interface{} field, none of which is named for a credential: decoded as
// the round trip decodes it, then sanitized as SanitizeMap sanitizes a value under such a name. A
// value nested deeper than maxGenericDepth (or holding a cycle) is marshaled and decoded whole.
func (g genericSanitizer) field(v any) any {
	d := decoded(v, 0)
	if _, deep := d.(tooDeep); deep {
		d = roundTrip(v)
	}
	switch x := d.(type) {
	case string:
		return CleanString(x, g.opts.StringLimit, g.opts.RedactSecrets)
	case map[string]any:
		return SanitizeMap(x, g.opts)
	case []any:
		return SanitizeSlice(x, g.opts)
	default:
		return x
	}
}

// tooDeep is what decoded returns for a value nested deeper than maxGenericDepth, and for any
// value holding one.
type tooDeep struct{}

// decoded returns v as encoding/json marshals it and decodes it back into an interface{} (nil,
// bool, float64, string, []any or map[string]any), and nil for a value JSON cannot represent. The
// shapes JSON decodes to, and the Go values in-process callers usually hold, are converted directly;
// anything else is marshaled and decoded on its own, which is what the encoder does with it as part
// of the whole.
func decoded(v any, depth int) any {
	if depth > maxGenericDepth {
		return tooDeep{}
	}
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return jsonString(x)
	case bool:
		return x
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil
		}
		return x
	case float32:
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
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
	case []any:
		if x == nil {
			return nil
		}
		out := make([]any, len(x))
		for i, item := range x {
			if out[i] = decoded(item, depth+1); out[i] == (tooDeep{}) {
				return tooDeep{}
			}
		}
		return out
	case []string:
		if x == nil {
			return nil
		}
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = jsonString(item)
		}
		return out
	case map[string]any:
		if x == nil {
			return nil
		}
		out := make(map[string]any, len(x))
		for k, item := range x {
			if !utf8.ValidString(k) {
				// Keys that differ only in invalid bytes are one key once encoded.
				return roundTrip(x)
			}
			if out[k] = decoded(item, depth+1); out[k] == (tooDeep{}) {
				return tooDeep{}
			}
		}
		return out
	case map[string]string:
		if x == nil {
			return nil
		}
		out := make(map[string]any, len(x))
		for k, item := range x {
			if !utf8.ValidString(k) {
				return roundTrip(x)
			}
			out[k] = jsonString(item)
		}
		return out
	}
	return roundTrip(v)
}

// roundTrip marshals v and decodes it into an interface{}, and is nil when either fails.
func roundTrip(v any) any {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}

// jsonValue is a value of a type JSON decoding does not produce, as encoding/json marshals it and
// decodes it back: a string, a []interface{}, a map[string]interface{}, a float64, a bool, or nil.
// SanitizeMap, SanitizeSlice and CleanValue convert such values with it.
//
// A value encoding/json cannot write whole (a NaN somewhere in it, a channel, a map whose keys are
// not strings, a cycle) is converted part by part instead: the items of a slice or an array, the
// values of a map, the fields of a struct, what a pointer or an interface holds. Only the parts it
// cannot represent become nil, so one bad value does not take the rest of a list with it. Fields
// are named as encoding/json names them (a tag's name, `-`, omitempty and omitzero, the fields of
// an embedded struct promoted); a `,string` option is not applied on this path. A part that has
// its own marshaler and fails is nil.
func jsonValue(value any) any {
	w := jsonWalk{seen: map[uintptr]bool{}}
	return w.value(value, 0)
}

type jsonWalk struct {
	// seen holds the pointers, maps and slices on the path being converted, so a cycle ends in nil.
	seen map[uintptr]bool
}

func (w jsonWalk) value(value any, depth int) any {
	switch value.(type) {
	case []string, map[string]string:
		return decoded(value, 0)
	}
	data, err := json.Marshal(value)
	if err == nil {
		var out any
		if json.Unmarshal(data, &out) == nil {
			return out
		}
	}
	if depth >= maxGenericDepth {
		return nil
	}
	return w.parts(reflect.ValueOf(value), depth+1)
}

func (w jsonWalk) parts(v reflect.Value, depth int) any {
	if !v.IsValid() {
		return nil
	}
	if t := v.Type(); t.Implements(jsonMarshalerType) || t.Implements(textMarshalerType) ||
		reflect.PointerTo(t).Implements(jsonMarshalerType) || reflect.PointerTo(t).Implements(textMarshalerType) {
		return nil
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if v.IsNil() {
			return nil
		}
		if w.seen[v.Pointer()] {
			return nil
		}
		w.seen[v.Pointer()] = true
		defer delete(w.seen, v.Pointer())
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return w.value(v.Elem().Interface(), depth)
	case reflect.Slice, reflect.Array:
		out := make([]any, v.Len())
		for i := range out {
			out[i] = w.value(v.Index(i).Interface(), depth)
		}
		return out
	case reflect.Map:
		out := make(map[string]any, v.Len())
		for iter := v.MapRange(); iter.Next(); {
			key, ok := jsonKey(iter.Key())
			if !ok {
				return nil
			}
			out[key] = w.value(iter.Value().Interface(), depth)
		}
		return out
	case reflect.Struct:
		out := map[string]any{}
		w.fields(out, v, depth)
		return out
	}
	return nil
}

// jsonKey is a map key as encoding/json writes it, and false for a key it cannot write.
func jsonKey(k reflect.Value) (string, bool) {
	switch k.Kind() {
	case reflect.String:
		return k.String(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(k.Int(), 10), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(k.Uint(), 10), true
	}
	if m, ok := k.Interface().(encoding.TextMarshaler); ok {
		text, err := m.MarshalText()
		return string(text), err == nil
	}
	return "", false
}

// fields adds the fields of struct v to out, under the names encoding/json gives them.
func (w jsonWalk) fields(out map[string]any, v reflect.Value, depth int) {
	t := v.Type()
	for i := range t.NumField() {
		f, fv := t.Field(i), v.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			if fv.Kind() == reflect.Pointer {
				if fv.IsNil() {
					continue
				}
				fv = fv.Elem()
			}
			if fv.Kind() == reflect.Struct {
				w.fields(out, fv, depth)
				continue
			}
		}
		if !f.IsExported() || !fv.CanInterface() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		if hasJSONOption(opts, "omitempty") && isEmptyJSONValue(fv) || hasJSONOption(opts, "omitzero") && fv.IsZero() {
			continue
		}
		if _, taken := out[name]; !taken {
			out[name] = w.value(fv.Interface(), depth)
		}
	}
}

func hasJSONOption(opts, option string) bool {
	for opts != "" {
		var next string
		next, opts, _ = strings.Cut(opts, ",")
		if next == option {
			return true
		}
	}
	return false
}

// isEmptyJSONValue is encoding/json's test for omitempty.
func isEmptyJSONValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Interface, reflect.Pointer:
		return v.IsZero()
	}
	return false
}

// SanitizeMCP returns a sanitized copy of an MCP block: every string converted as the JSON encoder
// writes it and cleaned with opts, as SanitizeEvent cleans it.
func SanitizeMCP(in *MCPInfo, opts PrivacyOptions) *MCPInfo {
	if in == nil {
		return nil
	}
	g := newGenericSanitizer(opts)
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
	g := newGenericSanitizer(PrivacyOptions{RedactSecrets: true, StringLimit: DefaultRawStringLimit})
	out := &SystemContextInfo{Kind: g.text(in.Kind), Source: g.text(in.Source)}
	if len(in.Skills) > 0 {
		out.Skills = make([]SkillRefInfo, len(in.Skills))
		for i, s := range in.Skills {
			out.Skills[i] = SkillRefInfo{SkillName: g.text(s.SkillName), SkillNameHash: g.text(s.SkillNameHash)}
		}
	}
	return out
}

// SanitizeGenAI returns a sanitized copy of a gen_ai block, as SanitizeEvent sanitizes it: every
// field converted as genericSanitizer describes and cleaned with opts, the semconv values
// (messages, tool arguments and results, system instructions, definitions, documents) as
// SanitizeMap cleans a decoded object. A tool's description is text the model reads, so it gets the
// prompt-text limit (DefaultStringLimit) rather than opts.StringLimit: under 2 KB a padded
// description would hide its instructions past the point the poisoning rule sees.
func SanitizeGenAI(in *GenAIInfo, opts PrivacyOptions) *GenAIInfo {
	if in == nil {
		return nil
	}
	g := newGenericSanitizer(opts)
	out := &GenAIInfo{}
	if a := in.Agent; a != nil {
		out.Agent = &GenAIAgentInfo{Description: g.text(a.Description), ID: g.text(a.ID), Name: g.text(a.Name), Version: g.text(a.Version)}
	}
	if c := in.Context; c != nil {
		out.Context = &GenAIContextInfo{UsedTokens: int64p(c.UsedTokens), LimitTokens: int64p(c.LimitTokens)}
	}
	if c := in.Conversation; c != nil {
		out.Conversation = &GenAIConversationInfo{ID: g.text(c.ID)}
	}
	if d := in.DataSource; d != nil {
		out.DataSource = &GenAIDataSourceInfo{ID: g.text(d.ID)}
	}
	if e := in.Embeddings; e != nil {
		out.Embeddings = &GenAIEmbeddingsInfo{DimensionCount: intp(e.DimensionCount)}
	}
	if e := in.Evaluation; e != nil {
		out.Evaluation = &GenAIEvaluationInfo{Explanation: g.text(e.Explanation), Name: g.text(e.Name)}
		if s := e.Score; s != nil {
			out.Evaluation.Score = &GenAIEvaluationScoreInfo{Label: g.text(s.Label), Value: float64p(s.Value)}
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
			ChoiceCount:      intp(r.ChoiceCount),
			EncodingFormats:  g.strings(r.EncodingFormats),
			FrequencyPenalty: float64p(r.FrequencyPenalty),
			MaxTokens:        intp(r.MaxTokens),
			Model:            g.text(r.Model),
			PresencePenalty:  float64p(r.PresencePenalty),
			Seed:             intp(r.Seed),
			StopSequences:    g.strings(r.StopSequences),
			Stream:           boolp(r.Stream),
			Temperature:      float64p(r.Temperature),
			TopK:             float64p(r.TopK),
			TopP:             float64p(r.TopP),
		}
	}
	if r := in.Response; r != nil {
		out.Response = &GenAIResponseInfo{
			FinishReasons:    g.strings(r.FinishReasons),
			ID:               g.text(r.ID),
			Model:            g.text(r.Model),
			TimeToFirstChunk: float64p(r.TimeToFirstChunk),
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
		out.Tool = &GenAIToolInfo{Definitions: g.field(t.Definitions), Name: g.text(t.Name), Type: g.text(t.Type)}
		if t.Description != "" {
			out.Tool.Description = CleanString(t.Description, DefaultStringLimit, opts.RedactSecrets)
		}
		if c := t.Call; c != nil {
			out.Tool.Call = &GenAIToolCallInfo{Arguments: g.field(c.Arguments), ID: g.text(c.ID), Result: g.field(c.Result)}
		}
	}
	if u := in.Usage; u != nil {
		out.Usage = &GenAIUsageInfo{
			CostUSD:      float64p(u.CostUSD),
			InputTokens:  int64p(u.InputTokens),
			OutputTokens: int64p(u.OutputTokens),
		}
		if c := u.CacheCreation; c != nil {
			out.Usage.CacheCreation = &GenAIUsageCacheCreationInfo{InputTokens: int64p(c.InputTokens), Ephemeral1hInputTokens: int64p(c.Ephemeral1hInputTokens)}
		}
		if c := u.CacheRead; c != nil {
			out.Usage.CacheRead = &GenAIUsageCacheReadInfo{InputTokens: int64p(c.InputTokens)}
		}
		if r := u.Reasoning; r != nil {
			out.Usage.Reasoning = &GenAIUsageReasoningInfo{OutputTokens: int64p(r.OutputTokens)}
		}
	}
	if w := in.Workflow; w != nil {
		out.Workflow = &GenAIWorkflowInfo{Name: g.text(w.Name)}
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
