package asymptotetrace

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

type EmitResult struct {
	Accepted bool
	Dropped  bool
}

type Client struct {
	opts     Options
	commands chan workerCommand
	done     chan struct{}
	stats    statsCounter

	closeMu   sync.Mutex
	accepting atomic.Bool
	closing   atomic.Bool
}

type workerCommand struct {
	kind     workerCommandKind
	envelope Envelope
	ctx      context.Context
	ack      chan error
}

type workerCommandKind int

const (
	workerCommandEmit workerCommandKind = iota
	workerCommandFlush
	workerCommandStop
)

func Start(opts Options) *Client {
	opts = opts.withDefaults()
	client := &Client{
		opts:     opts,
		commands: make(chan workerCommand, opts.QueueSize),
		done:     make(chan struct{}),
	}
	client.accepting.Store(true)
	go client.run()
	return client
}

func (c *Client) Emit(envelope Envelope) (EmitResult, error) {
	envelope = envelope.withDefaults()
	if err := envelope.Validate(); err != nil {
		return EmitResult{}, err
	}
	if !c.accepting.Load() {
		c.stats.dropped.Add(1)
		return EmitResult{Dropped: true}, nil
	}

	select {
	case c.commands <- workerCommand{kind: workerCommandEmit, envelope: envelope}:
		c.stats.accepted.Add(1)
		return EmitResult{Accepted: true}, nil
	default:
		c.stats.dropped.Add(1)
		return EmitResult{Dropped: true}, nil
	}
}

func (c *Client) Flush(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return c.sendBarrier(ctx, workerCommandFlush)
}

func (c *Client) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c.closing.Load() {
		select {
		case <-c.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	if c.closing.Load() {
		select {
		case <-c.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	ack := make(chan error, 1)
	command := workerCommand{kind: workerCommandStop, ctx: ctx, ack: ack}
	select {
	case <-c.done:
		return nil
	case c.commands <- command:
		c.closing.Store(true)
		c.accepting.Store(false)
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case err := <-ack:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) Stats() Stats {
	return c.stats.snapshot()
}

func (c *Client) sendBarrier(ctx context.Context, kind workerCommandKind) error {
	ack := make(chan error, 1)
	command := workerCommand{kind: kind, ctx: ctx, ack: ack}
	select {
	case <-c.done:
		return nil
	case c.commands <- command:
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case err := <-ack:
		return err
	case <-c.done:
		if kind == workerCommandStop {
			return nil
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) run() {
	defer close(c.done)

	ticker := time.NewTicker(c.opts.FlushInterval)
	defer ticker.Stop()

	batch := make([]Envelope, 0, c.opts.BatchSize)
	flushBatch := func(ctx context.Context) error {
		var firstErr error
		if len(batch) > 0 {
			if err := c.opts.Sink.WriteBatch(ctx, batch); err != nil {
				c.stats.recordError(err)
				firstErr = err
			} else {
				c.stats.written.Add(uint64(len(batch)))
				batch = batch[:0]
			}
		}
		if firstErr == nil {
			if err := c.opts.Sink.Flush(ctx); err != nil {
				c.stats.recordError(err)
				firstErr = err
			}
		}
		return firstErr
	}

	for {
		select {
		case command := <-c.commands:
			switch command.kind {
			case workerCommandEmit:
				batch = append(batch, c.prepareEnvelope(command.envelope))
				if len(batch) >= c.opts.BatchSize {
					_ = flushBatch(context.Background())
				}
			case workerCommandFlush:
				command.ack <- flushBatch(command.ctx)
			case workerCommandStop:
				// Drain any remaining emit commands that may have been
				// enqueued concurrently after the stop command.
			drainLoop:
				for {
					select {
					case pending := <-c.commands:
						if pending.kind == workerCommandEmit {
							batch = append(batch, c.prepareEnvelope(pending.envelope))
						}
					default:
						break drainLoop
					}
				}
				err := flushBatch(command.ctx)
				if closeErr := c.opts.Sink.Close(); closeErr != nil {
					c.stats.recordError(closeErr)
					if err == nil {
						err = closeErr
					}
				}
				command.ack <- err
				return
			}
		case <-ticker.C:
			_ = flushBatch(context.Background())
		}
	}
}

func (c *Client) prepareEnvelope(envelope Envelope) Envelope {
	out := envelope.copy()
	if out.Raw != nil {
		out.Raw = SanitizeMap(out.Raw, PrivacyOptions{
			RedactSecrets: true,
			StringLimit:   DefaultRawStringLimit,
		})
		out.Raw = RetentionAwareRaw(out.Raw, c.opts.ContentRetention)
	}
	return out
}
