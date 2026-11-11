package envoy

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"

	"github.com/asymptote-labs/agent-beacon/gateway"
)

// budget is a Handler that accounts for admission: what each Admit reserved, what is still held,
// and whether to refuse.
type budget struct {
	recorder
	mu          sync.Mutex
	refuse      *gateway.Verdict
	admits      []int64
	outstanding int64
}

func (b *budget) Admit(_ context.Context, n int64) (func(), gateway.Verdict, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.refuse != nil {
		return nil, *b.refuse, false
	}
	b.admits = append(b.admits, n)
	b.outstanding += n
	return func() {
		b.mu.Lock()
		b.outstanding -= n
		b.mu.Unlock()
	}, gateway.Verdict{}, true
}

func (b *budget) state() ([]int64, int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.admits), b.outstanding
}

// A body is admitted at its headers, for its declared length, before Envoy sends it; a reply
// that declares no length reserves a share at its headers and the rest when it is complete; and
// everything is released by the end of the exchange.
func TestAdmissionAtHeaders(t *testing.T) {
	h := &budget{}
	big := `{"x":"` + strings.Repeat("a", 1<<20+500) + `"}`
	exchangeWith(t, Config{}, h, []*extprocv3.ProcessingRequest{
		reqHeaders(":method", "POST", ":path", "/v1/messages", "content-length", "1234"),
		reqBody(`{"model":"m","messages":[]}`),
		respHeaders(":status", "200"),
		respBody(big, true),
	})
	admits, outstanding := h.state()
	if want := []int64{1234, unknownBody, int64(len(big)) - unknownBody}; !slices.Equal(admits, want) {
		t.Errorf("admitted %v, want %v (declared request, reply share, reply rest)", admits, want)
	}
	if outstanding != 0 {
		t.Errorf("%d bytes still reserved after the exchange", outstanding)
	}
	if len(h.calls) != 2 {
		t.Errorf("analyzed %d phases, want 2", len(h.calls))
	}
}

// A body refused at its headers is not analyzed: a refusal that allows lets the call pass, one
// that blocks is answered at once, before Envoy sends the body.
func TestRefusedAtHeaders(t *testing.T) {
	headers := reqHeaders(":method", "POST", ":path", "/v1/messages", "content-length", "27")
	open := &budget{refuse: &gateway.Verdict{Action: gateway.Allow}}
	got := kinds(exchangeWith(t, Config{}, open, []*extprocv3.ProcessingRequest{headers, reqBody(`{"model":"m","messages":[]}`)}))
	if !slices.Equal(got, []string{"continue", "continue"}) || len(open.calls) != 0 {
		t.Errorf("allowing refusal: answers %v, %d analyzed; want continue, continue and none", got, len(open.calls))
	}
	closed := &budget{refuse: &gateway.Verdict{Action: gateway.Block, Reason: "overloaded"}}
	got = kinds(exchangeWith(t, Config{}, closed, []*extprocv3.ProcessingRequest{headers, reqBody(`{}`)}))
	if !slices.Equal(got, []string{"immediate"}) || len(closed.calls) != 0 {
		t.Errorf("blocking refusal: answers %v, %d analyzed; want an immediate block at the headers", got, len(closed.calls))
	}
}

// An exchange that ends before its body arrives releases what its headers reserved.
func TestReservationReleasedOnEarlyEnd(t *testing.T) {
	h := &budget{}
	exchangeWith(t, Config{}, h, []*extprocv3.ProcessingRequest{reqHeaders(":method", "POST", ":path", "/v1/messages", "content-length", "4096")})
	if admits, outstanding := h.state(); !slices.Equal(admits, []int64{4096}) || outstanding != 0 {
		t.Errorf("admitted %v with %d bytes still held, want [4096] and none", admits, outstanding)
	}
}
