package asymptoteobserve

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"
)

// SanitizeEvent's typed conversion has every effect the JSON round trip it replaced had, checked
// one by one: invalid UTF-8, integers beyond 2^53, float32, empty omitempty fields, json:"-" fields,
// secret-named keys judged with their values, secret flags in lists, nil and empty lists in
// interface{} values, and the tool description at the prompt-text limit.
func TestSanitizeEventTypedFields(t *testing.T) {
	beyond := int64(1<<53 + 1)
	count := int64(1200)
	arguments := map[string]any{
		"api_key": "plain", "token_type": "bearer", "max_tokens": "4096", "n": 3, "f": float32(0.1),
		"args": []string{"-y", "--api-key", "k", "ok"}, "cookie": []any{"a", "", []any{"b"}},
		"empty": []any{}, "nil": []any(nil), "bad": "x\xffy",
	}
	e := Event{
		MCP:           &MCPInfo{Server: "notes\xff", Tool: "get"},
		SystemContext: &SystemContextInfo{Kind: SystemContextSkillListing, Skills: []SkillRefInfo{}},
		GenAI: &GenAIInfo{
			Usage:   &GenAIUsageInfo{InputTokens: &beyond, OutputTokens: &count},
			Request: &GenAIRequestInfo{StopSequences: []string{"--password", "hunter2", "END"}, EncodingFormats: []string{}},
			Tool: &GenAIToolInfo{Name: "mcp__notes__get", Description: "Fetch a note. token=abcdefgh12345678", Call: &GenAIToolCallInfo{
				ID: "t1", ResultText: "asserted", Arguments: arguments,
				Result: "export GITHUB_TOKEN=ghp_0123456789abcdefghijklmnopqrstuvwxyz",
			}},
			Agent:                  &GenAIAgentInfo{},
			SystemInstructionsText: "asserted",
		},
	}
	got := SanitizeEvent(e, 65536)

	if got.MCP.Server != "notes\uFFFD" || got.MCP.Tool != "get" {
		t.Errorf("mcp = %+v", got.MCP)
	}
	if got.SystemContext.Kind != SystemContextSkillListing || got.SystemContext.Skills != nil {
		t.Errorf("system context = %+v, want the empty skills list gone", got.SystemContext)
	}
	g := got.GenAI
	if *g.Usage.InputTokens != 1<<53 || *g.Usage.OutputTokens != 1200 {
		t.Errorf("usage = %d, %d; want 2^53+1 read back as 2^53, and 1200", *g.Usage.InputTokens, *g.Usage.OutputTokens)
	}
	if want := []string{"--password", "[REDACTED]", "END"}; !reflect.DeepEqual(g.Request.StopSequences, want) || g.Request.EncodingFormats != nil {
		t.Errorf("request = %+v", g.Request)
	}
	if g.Agent == nil || *g.Agent != (GenAIAgentInfo{}) {
		t.Errorf("an empty agent block = %+v, want kept empty", g.Agent)
	}
	if g.SystemInstructionsText != "" || g.Tool.Call.ResultText != "" {
		t.Errorf("derived fields kept: %q, %q", g.SystemInstructionsText, g.Tool.Call.ResultText)
	}
	if g.Tool.Description != "Fetch a note. token=[REDACTED]" {
		t.Errorf("description = %q", g.Tool.Description)
	}
	if g.Tool.Call.Result != "export GITHUB_TOKEN=[REDACTED]" {
		t.Errorf("result = %q", g.Tool.Call.Result)
	}
	want := map[string]any{
		"api_key": "[REDACTED]", "token_type": "bearer", "max_tokens": "4096", "n": float64(3), "f": 0.1,
		"args": []any{"-y", "--api-key", "[REDACTED]", "ok"}, "cookie": []any{"[REDACTED]", "", []any{"[REDACTED]"}},
		"empty": []any{}, "nil": nil, "bad": "x\uFFFDy",
	}
	if !reflect.DeepEqual(g.Tool.Call.Arguments, want) {
		t.Errorf("arguments = %#v\nwant %#v", g.Tool.Call.Arguments, want)
	}
	if arguments["api_key"] != "plain" || e.GenAI.Tool.Call.ResultText != "asserted" {
		t.Error("SanitizeEvent changed the caller's gen_ai")
	}
}

// A gen_ai block holding a value JSON cannot represent is sanitized like any other: every string
// cleaned and cut, and the value that cannot be represented replaced by nil. Each case puts one
// such value next to credentials and over-long text in the block's text fields.
func TestSanitizeGenAIUnrepresentable(t *testing.T) {
	nan, inf, negInf := math.NaN(), math.Inf(1), math.Inf(-1)
	maxInt64, minInt := int64(math.MaxInt64), int(math.MinInt64)
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	cases := map[string]struct {
		set   func(g *GenAIInfo)
		check func(t *testing.T, g *GenAIInfo)
	}{
		"NaN float field": {
			func(g *GenAIInfo) { g.Request = &GenAIRequestInfo{Temperature: &nan, Model: "m"} },
			func(t *testing.T, g *GenAIInfo) {
				if g.Request.Temperature != nil || g.Request.Model != "m" {
					t.Errorf("request = %+v", g.Request)
				}
			},
		},
		"infinite float fields": {
			func(g *GenAIInfo) {
				g.Request = &GenAIRequestInfo{TopP: &inf}
				g.Usage = &GenAIUsageInfo{CostUSD: &negInf}
			},
			func(t *testing.T, g *GenAIInfo) {
				if g.Request.TopP != nil || g.Usage.CostUSD != nil {
					t.Errorf("request %+v, usage %+v", g.Request, g.Usage)
				}
			},
		},
		"integer fields out of range": {
			func(g *GenAIInfo) {
				g.Usage = &GenAIUsageInfo{InputTokens: &maxInt64}
				g.Request = &GenAIRequestInfo{Seed: &minInt}
			},
			func(t *testing.T, g *GenAIInfo) {
				if g.Usage.InputTokens != nil || g.Request.Seed != nil {
					t.Errorf("usage %+v, request %+v", g.Usage, g.Request)
				}
			},
		},
		"NaN in a semconv value": {
			func(g *GenAIInfo) {
				g.Output = &GenAIOutputInfo{Messages: []any{map[string]any{"score": nan, "text": "ok"}}}
			},
			func(t *testing.T, g *GenAIInfo) {
				want := []any{map[string]any{"score": nil, "text": "ok"}}
				if !reflect.DeepEqual(g.Output.Messages, want) {
					t.Errorf("messages = %#v", g.Output.Messages)
				}
			},
		},
		"values json cannot marshal": {
			func(g *GenAIInfo) {
				g.Retrieval = &GenAIRetrievalInfo{Documents: []any{
					make(chan int), func() {}, struct{ F float64 }{nan}, json.Number("1e400"), "kept",
				}}
			},
			func(t *testing.T, g *GenAIInfo) {
				if want := []any{nil, nil, nil, nil, "kept"}; !reflect.DeepEqual(g.Retrieval.Documents, want) {
					t.Errorf("documents = %#v", g.Retrieval.Documents)
				}
			},
		},
		"a cycle": {
			func(g *GenAIInfo) { g.Retrieval = &GenAIRetrievalInfo{Documents: cyclic, QueryText: "q"} },
			func(t *testing.T, g *GenAIInfo) {
				if g.Retrieval.Documents != nil || g.Retrieval.QueryText != "q" {
					t.Errorf("retrieval = %#v", g.Retrieval)
				}
			},
		},
	}
	secret := "ghp_0123456789abcdefghijklmnopqrstuvwxyz"
	long := strings.Repeat("x", 5000)
	for name, tc := range cases {
		build := func() *GenAIInfo {
			g := &GenAIInfo{
				SystemInstructions: SystemInstructionParts("export GITHUB_TOKEN=" + secret),
				Tool: &GenAIToolInfo{
					Description: long + " api_key=" + secret,
					Call: &GenAIToolCallInfo{
						Arguments: map[string]any{"api_key": "plain-secret", "path": long},
						Result:    "token=abcdefgh12345678 " + long,
					},
				},
			}
			tc.set(g)
			return g
		}
		t.Run(name, func(t *testing.T) {
			for _, got := range []*GenAIInfo{
				SanitizeEvent(Event{GenAI: build()}, 65536).GenAI,
				SanitizeGenAI(build(), PrivacyOptions{RedactSecrets: true, StringLimit: DefaultRawStringLimit}),
			} {
				data, err := json.Marshal(got)
				if err != nil {
					t.Fatalf("sanitized gen_ai does not marshal: %v", err)
				}
				if text := string(data); strings.Contains(text, secret[4:]) || strings.Contains(text, "abcdefgh12345678") || strings.Contains(text, "plain-secret") {
					t.Errorf("credential left in %.300s", text)
				}
				result := got.Tool.Call.Result.(string)
				if len(result) > DefaultRawStringLimit || !strings.HasSuffix(result, "...[truncated]") {
					t.Errorf("result not cut to %d bytes: %d", DefaultRawStringLimit, len(result))
				}
				if path := got.Tool.Call.Arguments.(map[string]any)["path"].(string); len(path) > DefaultRawStringLimit {
					t.Errorf("argument not cut: %d bytes", len(path))
				}
				if d := got.Tool.Description; len(d) > DefaultStringLimit || !strings.HasSuffix(d, "...[truncated]") {
					t.Errorf("description not cut to %d bytes: %d", DefaultStringLimit, len(d))
				}
				tc.check(t, got)
			}
			// With redaction off, as the collector can be configured, text is still cut.
			got := SanitizeGenAI(build(), PrivacyOptions{StringLimit: DefaultRawStringLimit})
			if result := got.Tool.Call.Result.(string); len(result) > DefaultRawStringLimit {
				t.Errorf("result not cut with redaction off: %d bytes", len(result))
			}
			tc.check(t, got)
		})
	}
}

// Truncated is set exactly when the sanitized event's JSON is longer than maxBytes, whether the
// size bound decides it or the event is marshaled: on events right around the size, with values the
// bound gives up on, and on random events.
func TestSanitizeEventTruncatesAtItsMarshaledSize(t *testing.T) {
	const maxBytes = 65536
	check := func(what string, e Event) {
		t.Helper()
		got := SanitizeEvent(e, maxBytes)
		measured := got
		measured.Truncated = e.Truncated
		data, err := json.Marshal(measured)
		want := e.Truncated || err == nil && len(data) > maxBytes
		if got.Truncated != want {
			t.Fatalf("%s: truncated %v, want %v (%d bytes, %v)", what, got.Truncated, want, len(data), err)
		}
	}
	base := func(pad int) Event {
		raw := map[string]any{}
		for i := range 31 {
			raw[fmt.Sprintf("k%02d", i)] = strings.Repeat("<", 340)
		}
		raw["pad"] = strings.Repeat("a", pad)
		return Event{Vendor: Vendor, Message: "m", Raw: raw}
	}
	for pad := 0; pad < 1200; pad += 7 {
		check(fmt.Sprintf("pad %d", pad), base(pad))
	}
	for _, v := range []any{
		make(chan int), func() {}, math.NaN(), json.Number("12"), json.RawMessage(`{"a":1}`), time.Unix(0, 0),
		map[int]string{1: strings.Repeat("x", 70000)}, []byte(strings.Repeat("x", 70000)), struct{ A string }{strings.Repeat("x", 70000)},
	} {
		for _, pad := range []int{0, 600} {
			e := base(pad)
			e.Raw["v"] = v
			check(fmt.Sprintf("raw %T, pad %d", v, pad), e)
		}
	}
	r := rand.New(rand.NewPCG(1, 2))
	for i := range 1000 {
		check(fmt.Sprintf("random event %d", i), randomEvent(r))
	}
}

// The size bound never undercounts what json.Marshal writes.
func TestJSONSizeBoundIsABound(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	bounded := 0
	for i := range 2000 {
		e := randomEvent(r)
		data, err := json.Marshal(e)
		n, ok := jsonSizeBound(&e, math.MaxInt)
		if err == nil && ok {
			bounded++
			if n < len(data) {
				t.Fatalf("event %d: bound %d, marshaled %d bytes", i, n, len(data))
			}
		}
	}
	if bounded < 500 {
		t.Fatalf("only %d of 2000 events were bounded", bounded)
	}
}

// The typed conversion sanitizes typed string and list fields without asking whether their name
// marks them as a credential, which holds only while no such field has a name that does.
func TestTypedFieldNamesAreNotSecret(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type, path string)
	walk = func(typ reflect.Type, path string) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}
		seen[typ] = true
		for i := range typ.NumField() {
			f := typ.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			k := f.Type.Kind()
			if (k == reflect.String || k == reflect.Slice || k == reflect.Interface) && IsSecretKeyName(name) {
				t.Errorf("%s.%s (%q) is named for a credential; sanitize_typed.go must check it", path, f.Name, name)
			}
			walk(f.Type, path+"."+f.Name)
		}
	}
	for _, v := range []any{MCPInfo{}, SystemContextInfo{}, GenAIInfo{}} {
		walk(reflect.TypeOf(v), reflect.TypeOf(v).Name())
	}
}

// The typed conversion copies every field of the types it converts: with values sanitizing leaves
// alone, the result is the input, but for the fields that never survive the round trip (json:"-").
// A field added to these types and not to sanitize_typed.go fails here.
func TestTypedConversionKeepsEveryField(t *testing.T) {
	var fill func(v reflect.Value)
	fill = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.String:
			v.SetString("v")
		case reflect.Bool:
			v.SetBool(true)
		case reflect.Int, reflect.Int64:
			v.SetInt(7)
		case reflect.Float64:
			v.SetFloat(1.5)
		case reflect.Pointer:
			v.Set(reflect.New(v.Type().Elem()))
			fill(v.Elem())
		case reflect.Slice:
			v.Set(reflect.MakeSlice(v.Type(), 1, 1))
			fill(v.Index(0))
		case reflect.Interface:
			v.Set(reflect.ValueOf(map[string]any{"k": "v", "n": 1.5, "l": []any{"v", true, nil}}))
		case reflect.Struct:
			for i := range v.NumField() {
				fill(v.Field(i))
			}
		default:
			t.Fatalf("no value for a %s field", v.Kind())
		}
	}
	var mcp MCPInfo
	var context SystemContextInfo
	var genAI GenAIInfo
	for _, v := range []any{&mcp, &context, &genAI} {
		fill(reflect.ValueOf(v).Elem())
	}
	opts := PrivacyOptions{RedactSecrets: true, StringLimit: DefaultRawStringLimit}
	if got := SanitizeMCP(&mcp, opts); !reflect.DeepEqual(*got, mcp) {
		t.Errorf("mcp = %+v, want %+v", *got, mcp)
	}
	if got := sanitizeSystemContext(&context); !reflect.DeepEqual(*got, context) {
		t.Errorf("system context = %+v, want %+v", *got, context)
	}
	want := genAI
	want.SystemInstructionsText = ""
	tool := *want.Tool
	call := *tool.Call
	call.ResultText = ""
	tool.Call, want.Tool = &call, &tool
	if got := SanitizeGenAI(&genAI, opts); !reflect.DeepEqual(*got, want) {
		t.Errorf("gen_ai = %s\nwant %s", mustJSON(t, got), mustJSON(t, want))
	}
}

func mustJSON(t *testing.T, v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// ---- random events ----

func randomTestString(r *rand.Rand) string {
	pieces := []string{
		"token=abcdefgh12345678", `"api_key": "x y z"`, "--password hunter2", "Authorization: Bearer abc.def",
		"ghp_0123456789abcdefghijklmnopqrstuvwxyz", "postgres://user:pw@host/db", "input_tokens: 812",
		" ", "\n", "\t", `"`, "'", ",", "<&>", "\u2028", "\x01", "\xff", "é", "--api-key", "plain text",
	}
	var s strings.Builder
	for n := r.IntN(5); n >= 0; n-- {
		if r.IntN(10) == 0 {
			// Long text, around the limits.
			n := []int{100, 2040, 2048, 2060, 4090, 4100, 8200, 12300}[r.IntN(8)]
			s.WriteString(strings.Repeat("ab cd ", n/6+1)[:n])
			continue
		}
		s.WriteString(pieces[r.IntN(len(pieces))])
	}
	return s.String()
}

func randomTestKey(r *rand.Rand) string {
	keys := []string{"password", "api_key", "GITHUB_TOKEN", "Cookie", "max_tokens", "token_type", "name", "text", "args", "", "\xffkey"}
	return keys[r.IntN(len(keys))]
}

// randomGeneric returns a value of a shape an interface{} field or raw can hold: what JSON decodes
// to, and the Go values an in-process caller puts there.
func randomGeneric(r *rand.Rand, depth int) any {
	n := 16
	if depth > 3 {
		n = 9
	}
	switch r.IntN(n) {
	case 0:
		return nil
	case 1, 2, 3:
		return randomTestString(r)
	case 4:
		return r.IntN(2) == 0
	case 5:
		return []float64{0, math.Copysign(0, -1), 1.5, 1e21, 1e-7, 1 << 53, math.MaxFloat64, math.NaN(), math.Inf(1)}[r.IntN(9)]
	case 6:
		return []any{int(42), int64(math.MaxInt64), int64(math.MinInt64), int32(-7), uint64(math.MaxUint64), float32(0.1)}[r.IntN(6)]
	case 7:
		return []any{json.Number("12"), []byte("bytes"), struct{ N int }{3}, map[int]string{1: "x"}, []string{"--token", "t"}}[r.IntN(5)]
	case 8:
		return []any{[]any(nil), map[string]any(nil), []any{}, map[string]any{}}[r.IntN(4)]
	case 9, 10, 11, 12:
		out := make([]any, r.IntN(4))
		for i := range out {
			out[i] = randomGeneric(r, depth+1)
		}
		return out
	default:
		out := map[string]any{}
		for range r.IntN(4) {
			out[randomTestKey(r)] = randomGeneric(r, depth+1)
		}
		return out
	}
}

// fillRandom sets v, of any type an event holds, at random, leaving fields zero now and then.
func fillRandom(r *rand.Rand, v reflect.Value, depth int) {
	if depth > 0 && r.IntN(4) == 0 {
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(randomTestString(r))
	case reflect.Bool:
		v.SetBool(r.IntN(2) == 0)
	case reflect.Int, reflect.Int64, reflect.Int32:
		v.SetInt([]int64{0, 1, -1, 1<<53 + 1, math.MaxInt64, math.MinInt64}[r.IntN(6)])
	case reflect.Uint, reflect.Uint64:
		v.SetUint([]uint64{0, 1, math.MaxUint64}[r.IntN(3)])
	case reflect.Float64:
		v.SetFloat([]float64{0, 1.5, 1e21, 1e-7, math.NaN()}[r.IntN(5)])
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		fillRandom(r, p.Elem(), depth+1)
		v.Set(p)
	case reflect.Interface:
		if g := randomGeneric(r, depth); g != nil {
			v.Set(reflect.ValueOf(g))
		}
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), r.IntN(3), 3)
		for i := range s.Len() {
			fillRandom(r, s.Index(i), depth+1)
		}
		v.Set(s)
	case reflect.Map:
		if v.Type() == reflect.TypeFor[map[string]any]() {
			m := map[string]any{}
			for range r.IntN(5) {
				m[randomTestKey(r)] = randomGeneric(r, depth)
			}
			v.Set(reflect.ValueOf(m))
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fillRandom(r, v.Field(i), depth+1)
			}
		}
	}
}

func randomEvent(r *rand.Rand) Event {
	var e Event
	fillRandom(r, reflect.ValueOf(&e).Elem(), 0)
	if r.IntN(3) == 0 {
		// A large raw, near the size an event is marked truncated at.
		if e.Raw == nil {
			e.Raw = map[string]any{}
		}
		for i := range 28 + r.IntN(8) {
			e.Raw[fmt.Sprintf("k%d", i)] = strings.Repeat("<a>", 700+r.IntN(30))
		}
	}
	return e
}
