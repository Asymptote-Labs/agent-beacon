package privacyprocessor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor/processortest"
)

const credential = "synthetic-private-value"

func secretAttrs(t *testing.T, attrs pcommon.Map) {
	t.Helper()
	if err := attrs.FromRaw(map[string]any{
		"password":                  credential,
		"nested":                    map[string]any{"Cookie": []any{credential}, "text": "token=" + credential},
		"gen_ai.usage.input_tokens": int64(812),
		"model":                     "ordinary-model",
	}); err != nil {
		t.Fatal(err)
	}
}

func assertRedacted(t *testing.T, attrs pcommon.Map) {
	t.Helper()
	data, err := json.Marshal(attrs.AsRaw())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), credential) || !strings.Contains(string(data), "[REDACTED]") {
		t.Fatalf("secret reached downstream consumer: %s", data)
	}
	if count, ok := attrs.Get("gen_ai.usage.input_tokens"); !ok || count.Int() != 812 {
		t.Fatal("numeric token usage changed")
	}
	if model, ok := attrs.Get("model"); !ok || model.Str() != "ordinary-model" {
		t.Fatal("non-sensitive metadata changed")
	}
}

func TestLogsRedactedBeforeDownstreamConsumer(t *testing.T) {
	sink := new(consumertest.LogsSink)
	proc, err := NewFactory().CreateLogs(context.Background(), processortest.NewNopSettings(componentType), &Config{}, sink)
	if err != nil {
		t.Fatal(err)
	}
	logs := plog.NewLogs()
	resource := logs.ResourceLogs().AppendEmpty()
	secretAttrs(t, resource.Resource().Attributes())
	scope := resource.ScopeLogs().AppendEmpty()
	secretAttrs(t, scope.Scope().Attributes())
	for _, kind := range []string{"string", "map", "slice"} {
		record := scope.LogRecords().AppendEmpty()
		secretAttrs(t, record.Attributes())
		switch kind {
		case "string":
			record.Body().SetStr("Authorization: Bearer " + credential)
		case "map":
			secretAttrs(t, record.Body().SetEmptyMap())
		case "slice":
			if err := record.Body().FromRaw([]any{"token=" + credential, map[string]any{"password": credential}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A message-list attribute must still be parseable by the event converter;
	// applying the JSONL field cap at this boundary would corrupt its JSON.
	longMessages := `[{"role":"user","content":"` + strings.Repeat("x", 8192) + ` token=` + credential + `"}]`
	scope.LogRecords().At(0).Attributes().PutStr("gen_ai.input.messages", longMessages)
	if err := proc.ConsumeLogs(context.Background(), logs); err != nil {
		t.Fatal(err)
	}
	got := sink.AllLogs()[0].ResourceLogs().At(0)
	assertRedacted(t, got.Resource().Attributes())
	assertRedacted(t, got.ScopeLogs().At(0).Scope().Attributes())
	for recordIndex := 0; recordIndex < got.ScopeLogs().At(0).LogRecords().Len(); recordIndex++ {
		record := got.ScopeLogs().At(0).LogRecords().At(recordIndex)
		assertRedacted(t, record.Attributes())
		body, _ := json.Marshal(record.Body().AsRaw())
		if strings.Contains(string(body), credential) {
			t.Fatalf("body leaked: %s", body)
		}
	}
	encoded, _ := got.ScopeLogs().At(0).LogRecords().At(0).Attributes().Get("gen_ai.input.messages")
	if !json.Valid([]byte(encoded.Str())) || len(encoded.Str()) < 8192 {
		t.Fatal("encoded conversation was truncated before conversion")
	}
}

func TestTracesRedactedWithoutChangingIdentifiers(t *testing.T) {
	sink := new(consumertest.TracesSink)
	proc, err := NewFactory().CreateTraces(context.Background(), processortest.NewNopSettings(componentType), &Config{}, sink)
	if err != nil {
		t.Fatal(err)
	}
	traces := ptrace.NewTraces()
	resource := traces.ResourceSpans().AppendEmpty()
	secretAttrs(t, resource.Resource().Attributes())
	scope := resource.ScopeSpans().AppendEmpty()
	secretAttrs(t, scope.Scope().Attributes())
	span := scope.Spans().AppendEmpty()
	span.SetTraceID(pcommon.TraceID{1, 2, 3})
	span.SetSpanID(pcommon.SpanID{4, 5, 6})
	span.SetStartTimestamp(1234)
	span.SetName("token=" + credential)
	span.Status().SetMessage("token=" + credential)
	secretAttrs(t, span.Attributes())
	event := span.Events().AppendEmpty()
	event.SetName("token=" + credential)
	secretAttrs(t, event.Attributes())
	secretAttrs(t, span.Links().AppendEmpty().Attributes())
	if err := proc.ConsumeTraces(context.Background(), traces); err != nil {
		t.Fatal(err)
	}
	got := sink.AllTraces()[0].ResourceSpans().At(0)
	assertRedacted(t, got.Resource().Attributes())
	assertRedacted(t, got.ScopeSpans().At(0).Scope().Attributes())
	s := got.ScopeSpans().At(0).Spans().At(0)
	assertRedacted(t, s.Attributes())
	assertRedacted(t, s.Events().At(0).Attributes())
	assertRedacted(t, s.Links().At(0).Attributes())
	if s.TraceID() != (pcommon.TraceID{1, 2, 3}) || s.SpanID() != (pcommon.SpanID{4, 5, 6}) || s.StartTimestamp() != 1234 {
		t.Fatal("trace identity or timestamp changed")
	}
	for _, value := range []string{s.Name(), s.Status().Message(), s.Events().At(0).Name()} {
		if strings.Contains(value, credential) {
			t.Fatal("trace text leaked")
		}
	}
}

func TestEveryMetricKindRedactsLabelsAndExemplars(t *testing.T) {
	sink := new(consumertest.MetricsSink)
	proc, err := NewFactory().CreateMetrics(context.Background(), processortest.NewNopSettings(componentType), &Config{}, sink)
	if err != nil {
		t.Fatal(err)
	}
	metrics := pmetric.NewMetrics()
	resource := metrics.ResourceMetrics().AppendEmpty()
	secretAttrs(t, resource.Resource().Attributes())
	scope := resource.ScopeMetrics().AppendEmpty()
	secretAttrs(t, scope.Scope().Attributes())
	for _, kind := range []string{"gauge", "sum", "histogram", "exponential", "summary"} {
		metric := scope.Metrics().AppendEmpty()
		metric.SetName(kind)
		metric.SetDescription("token=" + credential)
		var attrs pcommon.Map
		var exemplars pmetric.ExemplarSlice
		switch kind {
		case "gauge":
			point := metric.SetEmptyGauge().DataPoints().AppendEmpty()
			point.SetIntValue(42)
			attrs, exemplars = point.Attributes(), point.Exemplars()
		case "sum":
			point := metric.SetEmptySum().DataPoints().AppendEmpty()
			point.SetIntValue(42)
			attrs, exemplars = point.Attributes(), point.Exemplars()
		case "histogram":
			point := metric.SetEmptyHistogram().DataPoints().AppendEmpty()
			point.SetCount(42)
			attrs, exemplars = point.Attributes(), point.Exemplars()
		case "exponential":
			point := metric.SetEmptyExponentialHistogram().DataPoints().AppendEmpty()
			point.SetCount(42)
			attrs, exemplars = point.Attributes(), point.Exemplars()
		case "summary":
			point := metric.SetEmptySummary().DataPoints().AppendEmpty()
			point.SetCount(42)
			attrs = point.Attributes()
		}
		secretAttrs(t, attrs)
		if kind != "summary" {
			secretAttrs(t, exemplars.AppendEmpty().FilteredAttributes())
		}
	}
	if err := proc.ConsumeMetrics(context.Background(), metrics); err != nil {
		t.Fatal(err)
	}
	got := sink.AllMetrics()[0]
	data, err := (&pmetric.JSONMarshaler{}).MarshalMetrics(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), credential) {
		t.Fatal("metric secret reached downstream consumer")
	}
	ms := got.ResourceMetrics().At(0).ScopeMetrics().At(0).Metrics()
	if ms.Len() != 5 || ms.At(0).Gauge().DataPoints().At(0).IntValue() != 42 || ms.At(1).Sum().DataPoints().At(0).IntValue() != 42 || ms.At(2).Histogram().DataPoints().At(0).Count() != 42 || ms.At(3).ExponentialHistogram().DataPoints().At(0).Count() != 42 || ms.At(4).Summary().DataPoints().At(0).Count() != 42 {
		t.Fatal("metric type, point count or numeric values changed")
	}
}
