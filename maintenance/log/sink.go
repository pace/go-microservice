package log

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type sinkKey struct{}

const defaultSinkSize = 1000

// ContextWithSink wraps the given context in a new context with
// the given Sink stored as value.
func ContextWithSink(ctx context.Context, sink *Sink) context.Context {
	l := log.Ctx(ctx).Output(sink)
	ctx = l.WithContext(ctx)
	return context.WithValue(ctx, sinkKey{}, sink)
}

// SinkFromContext returns the Sink of the given context if it exists
func SinkFromContext(ctx context.Context) (*Sink, bool) {
	sink, ok := ctx.Value(sinkKey{}).(*Sink)
	return sink, ok
}

// SinkContextTransfer gets the sink from the sourceCtx
// and returns a new context based on targetCtx with the
// extracted sink. If no sink is present this is a noop
func SinkContextTransfer(sourceCtx, targetCtx context.Context) context.Context {
	sink, ok := SinkFromContext(sourceCtx)
	if !ok {
		return targetCtx
	}

	return context.WithValue(targetCtx, sinkKey{}, sink)
}

// Sink respresents a log sink which is used to store
// logs, created with log.Ctx(ctx), inside the context
// and use them at a later point in time
type Sink struct {
	Silent     bool
	customSize int
	ring       stringRing
	init       sync.Once

	output  io.Writer
	rwmutex sync.RWMutex
}

// NewSink initializes a new sink. This will deprecate the public properties
// of the sink struct sometime in the future
func NewSink(opts ...SinkOption) *Sink {
	sink := &Sink{}
	for _, opt := range opts {
		opt(sink)
	}
	// init ring buffer
	sinkSize := defaultSinkSize
	if sink.customSize > 0 {
		sinkSize = sink.customSize
	}
	sink.ring = newStringRing(sinkSize)

	return sink
}

// handlerWithSink returns a mux.MiddlewareFunc which adds a Sink
// to the request context. All logs corresponding to the request
// will be printed and stored in the Sink for later use. Optionally
// several path prefixes like "/health" can be provided to decrease
// log spamming. All url paths with these prefixes will set the Sink
// to silent and all logs will only reach the Sink but not the
// actual log output.
func handlerWithSink(silentPrefixes ...string) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var sink Sink
			for _, prefix := range silentPrefixes {
				if strings.HasPrefix(r.URL.Path, prefix) {
					sink.Silent = true
				}
			}

			ctx := ContextWithSink(r.Context(), &sink)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ToJSON returns a copy of the currently available
// logs in the Sink as json formatted []byte.
func (s *Sink) ToJSON() []byte {
	s.rwmutex.RLock()
	defer s.rwmutex.RUnlock()

	return []byte("[" + strings.Join(s.ring.GetContent(), ",") + "]")
}

// Pretty returns the logs as string while using the
// zerolog.ConsoleWriter to format them in a human
// readable way
func (s *Sink) Pretty() string {
	buf := &bytes.Buffer{}
	writer := &zerolog.ConsoleWriter{
		Out:        buf,
		NoColor:    true,
		TimeFormat: "2006-01-02 15:04:05",
	}

	s.rwmutex.Lock()
	defer s.rwmutex.Unlock()
	for _, str := range s.ring.GetContent() {
		n, err := strings.NewReader(str).WriteTo(writer)
		if err != nil {
			log.Warn().Err(err).Msg("log.Sink.Pretty failed")
		} else if int(n) != len(str) {
			log.Warn().Msg("log.Sink.Pretty failed: could not return all logs")
		}
	}

	return buf.String()
}

// Write implements the io.Writer interface. This makes it
// possible to use the Sink as output in the zerolog.Output()
// func. Write stores all incoming logs in its internal store
// and calls Write() on the default output writer.
func (s *Sink) Write(b []byte) (int, error) {
	// make sure the buffer is safe to write to
	s.init.Do(s.initBuffer)

	s.rwmutex.Lock()
	if s.output == nil {
		s.output = logOutput
	}

	s.ring.writeString(string(b))
	s.rwmutex.Unlock()

	if s.Silent {
		return len(b), nil
	}

	return s.output.Write(b)
}

// this is required for cases where a sink is created directly
// because then the ring will not be created via newStringRing
// and its size may be 0 (causes div by zero error)
func (s *Sink) initBuffer() {
	if s.ring.size == 0 {
		s.ring.size = defaultSinkSize
	}
}

type stringRing struct {
	data    []string
	nextPos int
	size    int
}

func newStringRing(size int) stringRing {
	return stringRing{
		size: size,
	}
}

func (r *stringRing) writeString(c string) {
	// until we hit the size limit, just append to grow the buffer
	if len(r.data) < r.size {
		r.data = append(r.data, c)
		return
	}
	if len(r.data) < r.size-1 {
		// default case: ring has not reached maximum size yet
		// so just append and increase
		r.data[r.nextPos] = c
		r.nextPos++
	} else {
		// overflow case: start overwriting at the beginning
		r.nextPos = r.nextPos % r.size
		r.data[r.nextPos] = c
		r.nextPos++
	}
}

// GetContent returns the content of the buffer in the order it was written
func (r *stringRing) GetContent() []string {
	// default case: write pointer has not started overflowing
	if len(r.data) < r.size {
		return r.data
	} else {
		out := r.data[r.nextPos:]
		out = append(out, r.data[:r.nextPos]...)
		return out
	}
}

type SinkOption func(*Sink)

func Silent() SinkOption {
	return func(s *Sink) {
		s.Silent = true
	}
}

func CustomSize(size int) SinkOption {
	return func(s *Sink) {
		s.customSize = size
	}
}
