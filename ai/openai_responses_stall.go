package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"sync"
	"time"
)

type openAIResponsesStallKey struct{}

// pig divergence (D131): native OpenAI Responses uses the owner's request-start budget, independently of legacy idleness.
const defaultOpenAIResponsesStall = 180 * time.Second

func openAIResponsesStallTimeout(opts StreamOptions) time.Duration {
	if opts.StallTimeoutMs == nil {
		return defaultOpenAIResponsesStall
	}
	return time.Duration(min(max(int64(*opts.StallTimeoutMs), 0), math.MaxInt64/int64(time.Millisecond))) * time.Millisecond
}

type openAIResponsesStallError struct{ timeout time.Duration }

func (e *openAIResponsesStallError) Error() string {
	return fmt.Sprintf("SSE Event Timeout Error: no event for %dms", e.timeout.Milliseconds())
}

// roundTripOpenAIResponsesStall adds only the native OpenAI request budget. The unchanged D131 implementation still owns legacy byte/semantic idleness.
func (t *sseIdleTransport) roundTripOpenAIResponsesStall(request *http.Request, timeout time.Duration) (*http.Response, error) {
	ctx, cancel := context.WithCancelCause(request.Context())
	timer := time.AfterFunc(timeout, func() { cancel(&openAIResponsesStallError{timeout: timeout}) })
	response, err := t.roundTripLegacy(request.WithContext(ctx))
	if err != nil || response.Body == nil {
		timer.Stop()
		cause := context.Cause(ctx)
		cancel(nil)
		if stalled, ok := errors.AsType[*openAIResponsesStallError](cause); ok {
			return response, stalled
		}
		return response, err
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	body := &openAIResponsesStallBody{ReadCloser: response.Body, ctx: ctx, cancel: cancel, timer: timer, timeout: timeout, sse: mediaType == "text/event-stream"}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = body.closeBody()
		close(done)
	})
	body.stopAbort = func() {
		if stop() {
			close(done)
		}
		<-done
	}
	response.Body = body
	return response, nil
}

// openAIResponsesStallBody keeps the request budget through non-SSE error bodies. Only SSE progress, using D131's existing reasoning/keepalive policy, resets it.
type openAIResponsesStallBody struct {
	io.ReadCloser
	ctx       context.Context
	cancel    context.CancelCauseFunc
	timer     *time.Timer
	timeout   time.Duration
	sse       bool
	progress  sseIdleBody
	mu        sync.Mutex
	stopped   bool
	stopAbort func()
	closeOnce sync.Once
	closeErr  error
}

func (b *openAIResponsesStallBody) stopTimer() {
	b.mu.Lock()
	b.stopped = true
	b.timer.Stop()
	b.mu.Unlock()
}

func (b *openAIResponsesStallBody) closeBody() error {
	b.closeOnce.Do(func() { b.closeErr = b.ReadCloser.Close() })
	return b.closeErr
}

func (b *openAIResponsesStallBody) Read(buffer []byte) (int, error) {
	count, err := b.ReadCloser.Read(buffer)
	if err != nil {
		b.stopTimer()
		b.stopAbort()
		if stalled, ok := errors.AsType[*openAIResponsesStallError](context.Cause(b.ctx)); ok {
			return count, stalled
		}
		return count, err
	}
	if b.sse && count > 0 && b.progress.scan(buffer[:count]) {
		b.mu.Lock()
		if !b.stopped {
			b.timer.Reset(b.timeout)
		}
		b.mu.Unlock()
	}
	return count, nil
}

func (b *openAIResponsesStallBody) Close() error {
	b.stopTimer()
	b.stopAbort()
	err := b.closeBody()
	b.cancel(nil)
	return err
}
