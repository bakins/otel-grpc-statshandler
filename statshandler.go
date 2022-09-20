// Package statshandler implements a grpc.StatsHandler that records
// OpenTelemetry traces and metrics
package statshandler

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/global"
	"go.opentelemetry.io/otel/metric/instrument/syncfloat64"
	"go.opentelemetry.io/otel/metric/instrument/syncint64"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.12.0"
	"go.opentelemetry.io/otel/trace"
	grpc_codes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
)

const (
	// DefaultInstrumentationName is the default used when creating meters and tracers.
	DefaultInstrumentationName = "otel-grpc-statshandler"
)

// ServerHandler implements https://pkg.go.dev/google.golang.org/grpc/stats#ServerHandler
// It records OpenTelemetry metrics and traces.
type ServerHandler struct {
	handler
}

// ServerHandler implements https://pkg.go.dev/google.golang.org/grpc/stats#ServerHandler
// It records OpenTelemetry metrics and traces.
type ClientHandler struct {
	handler
}

type handler struct {
	tracer             trace.Tracer
	propogator         propagation.TextMapPropagator
	rpcDuration        syncfloat64.Histogram
	rpcRequestSize     syncint64.Histogram
	rpcResponseSize    syncint64.Histogram
	rpcRequestsPerRPC  syncint64.Histogram
	rpcResponsesPerRPC syncint64.Histogram
	spanKind           trace.SpanKind
}

func newHandler(spanKind trace.SpanKind, options []Option) (handler, error) {
	c := config{}

	for _, o := range options {
		o.apply(&c)
	}

	if c.meterProvider == nil {
		c.meterProvider = global.MeterProvider()
	}

	if c.tracerProvider == nil {
		c.tracerProvider = otel.GetTracerProvider()
	}

	if c.propagator == nil {
		c.propagator = otel.GetTextMapPropagator()
	}

	if c.instrumentationName == "" {
		c.instrumentationName = DefaultInstrumentationName
	}

	// metrics from https://opentelemetry.io/docs/reference/specification/metrics/semantic_conventions/rpc/#rpc-server

	meter := c.meterProvider.Meter(c.instrumentationName)

	prefix := "rpc.server"
	if spanKind == trace.SpanKindClient {
		prefix = "rpc.client"
	}

	rpcDuration, err := meter.SyncFloat64().Histogram(prefix + ".duration")
	if err != nil {
		return handler{}, err
	}

	rpcRequestSize, err := meter.SyncInt64().Histogram(prefix + ".request.size")
	if err != nil {
		return handler{}, err
	}

	rpcResponseSize, err := meter.SyncInt64().Histogram(prefix + ".response.size")
	if err != nil {
		return handler{}, err
	}

	rpcRequestsPerRPC, err := meter.SyncInt64().Histogram(prefix + ".requests_per_rpc")
	if err != nil {
		return handler{}, err
	}

	rpcResponsesPerRPC, err := meter.SyncInt64().Histogram(prefix + ".responses_per_rpc")
	if err != nil {
		return handler{}, err
	}

	h := handler{
		tracer:             c.tracerProvider.Tracer(c.instrumentationName),
		propogator:         c.propagator,
		spanKind:           spanKind,
		rpcDuration:        rpcDuration,
		rpcRequestSize:     rpcRequestSize,
		rpcResponseSize:    rpcResponseSize,
		rpcRequestsPerRPC:  rpcRequestsPerRPC,
		rpcResponsesPerRPC: rpcResponsesPerRPC,
	}

	return h, nil
}

func NewServerHandler(options ...Option) (*ServerHandler, error) {
	h, err := newHandler(trace.SpanKindServer, options)
	if err != nil {
		return nil, err
	}

	s := ServerHandler{
		handler: h,
	}

	return &s, nil
}

func NewClientHandler(options ...Option) (*ClientHandler, error) {
	h, err := newHandler(trace.SpanKindClient, options)
	if err != nil {
		return nil, err
	}

	c := ClientHandler{
		handler: h,
	}

	return &c, nil
}

// Option applies an option value when creating a Handler
type Option interface {
	apply(*config)
}

type optionFunc func(*config)

func (f optionFunc) apply(c *config) {
	f(c)
}

type config struct {
	propagator          propagation.TextMapPropagator
	tracerProvider      trace.TracerProvider
	meterProvider       metric.MeterProvider
	instrumentationName string
}

// WithInstrumentationName returns an Option to use the TracerProvider when
// creating a Tracer.
func WithInstrumentationName(name string) Option {
	return optionFunc(func(c *config) {
		c.instrumentationName = name
	})
}

// WithTracerProvider returns an Option to use the TracerProvider when
// creating a Tracer.
func WithTracerProvider(p trace.TracerProvider) Option {
	return optionFunc(func(c *config) {
		c.tracerProvider = p
	})
}

// WithMeterProvider returns an Option to use the MetricProvider when
// creating metrics.
func WithMeterProvider(p metric.MeterProvider) Option {
	return optionFunc(func(c *config) {
		c.meterProvider = p
	})
}

// WithPropagator returns an Option to use the Propagator when extracting
// and injecting trace context from requests.
func WithPropagators(p propagation.TextMapPropagator) Option {
	return optionFunc(func(c *config) {
		c.propagator = p
	})
}

type rpcObserve struct {
	attributes       []attribute.KeyValue
	startTime        time.Time
	messagesReceived int64
	messagesSent     int64
}

// context key copied from net/http
type contextKey struct {
	name string
}

var rpcObserveKey = &contextKey{"rpc-observe"}

// TagRPC implements per-RPC context management.
func (s *ServerHandler) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	return s.handler.TagRPC(ctx, info)
}

// TagRPC implements per-RPC context management.
func (c *ClientHandler) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	return c.handler.TagRPC(ctx, info)
}

func (h handler) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	md, _ := metadata.FromIncomingContext(ctx)
	h.propogator.Extract(ctx, &metadataSupplier{metadata: md})

	ctx, span := h.tracer.Start(ctx,
		info.FullMethodName,
		trace.WithSpanKind(h.spanKind),
	)

	// https://opentelemetry.io/docs/reference/specification/metrics/semantic_conventions/rpc/
	// https://opentelemetry.io/docs/reference/specification/trace/semantic_conventions/rpc/
	attributes := make([]attribute.KeyValue, 0, 4)
	attributes = append(attributes, semconv.RPCSystemGRPC)

	parts := strings.Split(info.FullMethodName, "/")
	if len(parts) == 3 {
		attributes = append(attributes, semconv.RPCServiceKey.String(parts[1]))
		attributes = append(attributes, semconv.RPCMethodKey.String(parts[2]))
	}

	span.SetAttributes(attributes...)

	observer := rpcObserve{
		startTime:  time.Now(),
		attributes: attributes,
	}

	return context.WithValue(ctx, rpcObserveKey, &observer)
}

var grpcStatusOK = status.New(grpc_codes.OK, "OK")

// HandleRPC implements per-RPC tracing and stats instrumentation.
func (s *ServerHandler) HandleRPC(ctx context.Context, rs stats.RPCStats) {
	s.handler.HandleRPC(ctx, rs)
}

// HandleRPC implements per-RPC tracing and stats instrumentation.
func (c *ClientHandler) HandleRPC(ctx context.Context, rs stats.RPCStats) {
	c.handler.HandleRPC(ctx, rs)
}

func (h handler) HandleRPC(ctx context.Context, rs stats.RPCStats) {
	span := trace.SpanFromContext(ctx)

	// this should never be null, but we always check, just to be sure.
	observer, _ := ctx.Value(rpcObserveKey).(*rpcObserve)

	switch rs := rs.(type) {
	case *stats.Begin:

	case *stats.InPayload:
		var id int64

		if observer != nil {
			id = atomic.AddInt64(&observer.messagesReceived, 1)
			h.rpcRequestSize.Record(ctx, int64(rs.Length), observer.attributes...)
		}

		span.AddEvent("message",
			trace.WithAttributes(
				semconv.MessageTypeReceived,
				semconv.MessageUncompressedSizeKey.Int(rs.Length),
				semconv.MessageIDKey.Int64(id),
			),
		)

	case *stats.OutPayload:
		var id int64

		if observer != nil {
			id = atomic.AddInt64(&observer.messagesSent, 1)
			h.rpcResponseSize.Record(ctx, int64(rs.Length), observer.attributes...)
		}

		span.AddEvent("message",
			trace.WithAttributes(
				semconv.MessageTypeSent,
				semconv.MessageUncompressedSizeKey.Int(rs.Length),
				semconv.MessageIDKey.Int64(id),
			),
		)

	case *stats.End:
		var rpcStatus *status.Status
		if rs.Error != nil {
			s, ok := status.FromError(rs.Error)
			if ok {
				rpcStatus = s
			} else {
				rpcStatus = status.New(grpc_codes.Internal, rs.Error.Error())
			}
		} else {
			rpcStatus = grpcStatusOK
		}

		if observer != nil {
			attributes := make([]attribute.KeyValue, 0, len(observer.attributes)+1)
			attributes = append(attributes, observer.attributes...)
			attributes = append(attributes, semconv.RPCGRPCStatusCodeKey.Int(int(rpcStatus.Code())))

			duration := time.Since(observer.startTime).Milliseconds()

			h.rpcDuration.Record(
				ctx,
				float64(duration),
				attributes...,
			)

			h.rpcRequestsPerRPC.Record(
				ctx,
				observer.messagesReceived,
				attributes...,
			)

			h.rpcResponsesPerRPC.Record(
				ctx,
				observer.messagesSent,
				attributes...,
			)
		}

		code := rpcStatus.Code()
		span.SetAttributes(semconv.RPCGRPCStatusCodeKey.Int64(int64(code)))
		if code != grpc_codes.OK {
			span.SetStatus(codes.Error, rpcStatus.Message())
		}

		span.End()
	}
}

// TagConn exists to satisfy gRPC stats.Handler.
func (s *ServerHandler) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	// no-op
	return ctx
}

// HandleConn exists to satisfy gRPC stats.Handler.
func (s *ServerHandler) HandleConn(_ context.Context, _ stats.ConnStats) {
	// no-op
}

// TagConn exists to satisfy gRPC stats.Handler.
func (c *ClientHandler) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	// no-op
	return ctx
}

// HandleConn exists to satisfy gRPC stats.Handler.
func (c *ClientHandler) HandleConn(_ context.Context, _ stats.ConnStats) {
	// no-op
}

// from https://github.com/open-telemetry/opentelemetry-go-contrib/blob/instrumentation/google.golang.org/grpc/otelgrpc/v0.34.0/instrumentation/google.golang.org/grpc/otelgrpc/grpctrace.go#L87
type metadataSupplier struct {
	metadata metadata.MD
}

var _ propagation.TextMapCarrier = &metadataSupplier{}

func (s *metadataSupplier) Get(key string) string {
	values := s.metadata.Get(key)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func (s *metadataSupplier) Set(key string, value string) {
	if s.metadata != nil {
		s.metadata.Set(key, value)
	}
}

func (s *metadataSupplier) Keys() []string {
	out := make([]string, 0, len(s.metadata))
	for key := range s.metadata {
		out = append(out, key)
	}
	return out
}
