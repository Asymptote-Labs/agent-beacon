package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func TestTraceListLargePageIsEmpty(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		t.Run(strconv.FormatBool(indexed), func(t *testing.T) {
			event := testSchemaEvent("2026-10-13T12:00:00Z", "claude_code", "prompt.submitted", "prompt", "")
			event.Session = &schema.SessionInfo{ID: "page-control"}
			var path string
			if indexed {
				path = traceStoreIndexedLog(t, marshalEvents(t, event))
			} else {
				path = traceStoreBlockedLog(t, marshalEvents(t, event))
			}
			handler, err := Handler(Options{UserMode: true, LogPath: path})
			if err != nil {
				t.Fatal(err)
			}
			for _, page := range []int{1, 2, int(^uint(0) >> 1)} {
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/traces?limit=10&page="+strconv.Itoa(page), nil))
				if rec.Code != http.StatusOK {
					t.Fatalf("page %d: status %d: %s", page, rec.Code, rec.Body.String())
				}
				var got TraceListResultV1
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				want := 0
				if page == 1 {
					want = 1
				}
				if got.Page != page || got.Limit != 10 || got.Returned != want || got.TotalMatched != 1 || len(got.Traces) != want || got.Truncated {
					t.Fatalf("page %d: %+v", page, got)
				}
			}
		})
	}
}

func TestTraceWindowLargeContextDoesNotOverflow(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		t.Run(strconv.FormatBool(indexed), func(t *testing.T) {
			event := testSchemaEvent("2026-10-13T12:00:00Z", "claude_code", "prompt.submitted", "prompt", "")
			event.Session = &schema.SessionInfo{ID: "window-control"}
			var path string
			if indexed {
				path = traceStoreIndexedLog(t, marshalEvents(t, event))
			} else {
				path = traceStoreBlockedLog(t, marshalEvents(t, event))
			}
			for _, width := range []int{1, int(^uint(0) >> 1)} {
				got, found, err := ShowTrace(path, "session:claude_code:window-control", TraceQuery{AroundEvent: 1, Before: width, After: width})
				if err != nil || !found {
					t.Fatalf("show: found %v, err %v", found, err)
				}
				if len(got.Events) != 1 || got.Range.ReturnedEvents != 1 || got.Range.Offset != 1 {
					t.Fatalf("width %d: %+v", width, got)
				}
			}
		})
	}
}
