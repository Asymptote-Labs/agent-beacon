package main

import "context"

type chaosAction uint8

const (
	chaosNone chaosAction = iota
	chaosMalformed
	chaosCancel
	chaosSlowloris
	chaosGzipBomb
	chaosUnknownPath
)

var chaosNames = [...]string{"none", "malformed json", "cancel", "slowloris 1KB/s", "gzip bomb", "unknown path"}

// pickChaos maps a uniform draw to the chaos mix: 5% malformed JSON, 2% client cancels, 2%
// slowloris, 1% gzip bomb (Envoy replies), 1% unknown paths, the rest normal turns.
func pickChaos(x float64) chaosAction {
	switch {
	case x < 0.05:
		return chaosMalformed
	case x < 0.07:
		return chaosCancel
	case x < 0.09:
		return chaosSlowloris
	case x < 0.10:
		return chaosGzipBomb
	case x < 0.11:
		return chaosUnknownPath
	}
	return chaosNone
}

func (w *worker) chaos(ctx context.Context, a chaosAction) {
	switch {
	case w.tgt == tEnvoy:
		w.envoyChaos(ctx, a)
	case a == chaosGzipBomb:
		// A compressed reply is an Envoy concern; HTTP callouts carry decoded JSON.
		p := w.plan()
		w.httpTurn(ctx, p)
		w.advance(p)
	default:
		w.httpChaos(ctx, a)
	}
}
