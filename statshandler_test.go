package statshandler_test

import (
	"context"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metrictest"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.12.0"
	otelTrace "go.opentelemetry.io/otel/trace"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	pb "google.golang.org/grpc/examples/helloworld/helloworld"
	"google.golang.org/grpc/status"

	statshandler "github.com/bakins/otel-grpc-statshandler"
)

func ExampleServerHandler() {
	handler, err := statshandler.NewServerHandler()
	if err != nil {
		// handle error
	}

	server := grpc.NewServer(grpc.StatsHandler(handler))

	_ = server // use server
}

func ExampleClientHandler() {
	handler, err := statshandler.NewClientHandler()
	if err != nil {
		// handle error
	}

	conn, err := grpc.Dial("myaddress:port", grpc.WithStatsHandler(handler))
	if err != nil {
		// handle error
	}

	client := pb.NewGreeterClient(conn)

	_ = client // use client
}

func TestServerHandler(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := trace.NewTracerProvider(trace.WithSpanProcessor(sr))

	mp, exp := metrictest.NewTestMeterProvider()

	handler, err := statshandler.NewServerHandler(
		statshandler.WithTracerProvider(tp),
		statshandler.WithMeterProvider(mp),
	)
	require.NoError(t, err)

	g := grpc.NewServer(grpc.StatsHandler(handler))

	pb.RegisterGreeterServer(g, &testServer{})

	svr := httptest.NewServer(h2c.NewHandler(g, &http2.Server{}))
	defer svr.Close()

	u, err := url.Parse(svr.URL)
	require.NoError(t, err)

	conn, err := grpc.Dial(u.Host, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)

	defer conn.Close()

	client := pb.NewGreeterClient(conn)

	resp, err := client.SayHello(context.Background(), &pb.HelloRequest{Name: "world"})
	require.NoError(t, err)

	require.Equal(t, "hello world", resp.Message)

	spans := sr.Ended()
	require.Len(t, spans, 1)
	span := spans[0]

	require.Equal(t, otelTrace.SpanKindServer, span.SpanKind())
	require.Equal(t, "/helloworld.Greeter/SayHello", span.Name())
	require.Len(t, span.Events(), 2)

	require.Equal(t, "message", span.Events()[0].Name)
	require.Len(t, span.Events()[0].Attributes, 3)
	require.Equal(t, "message", span.Events()[1].Name)
	require.Len(t, span.Events()[1].Attributes, 3)

	require.Len(t, span.Attributes(), 4)

	traceAttributes := attribute.NewSet(span.Attributes()...)

	expectedAttributes := map[attribute.Key]attribute.Value{
		semconv.RPCSystemKey:         attribute.StringValue("grpc"),
		semconv.RPCServiceKey:        attribute.StringValue("helloworld.Greeter"),
		semconv.RPCMethodKey:         attribute.StringValue("SayHello"),
		semconv.RPCGRPCStatusCodeKey: attribute.Int64Value(0),
	}

	for key, value := range expectedAttributes {
		got, ok := traceAttributes.Value(key)
		require.True(t, ok, "did not find expected span attribute %s", key)
		require.Equal(t, value.AsInterface(), got.AsInterface(), "unexpected value for span attribute %s", key)
	}

	err = exp.Collect(context.Background())
	require.NoError(t, err)

	require.Len(t, exp.GetRecords(), 5)

	expectedKeys := []string{
		"rpc.server.duration",
		"rpc.server.requests_per_rpc",
		"rpc.server.responses_per_rpc",
	}

	for _, key := range expectedKeys {
		val, err := exp.GetByName(key)
		require.NoError(t, err, "failed to get metric for %s", key)

		require.Len(t, val.Attributes, 4, "unexpected number of metric attributes for %s", key)

		attributes := attribute.NewSet(val.Attributes...)

		for key, value := range expectedAttributes {
			got, ok := attributes.Value(key)
			require.True(t, ok, "did not find expected metric attribute %s", key)
			require.Equal(t, value.AsInterface(), got.AsInterface(), "unexpected value for metric attribute %s", key)
		}
	}

	for _, key := range []string{"rpc.server.request.size", "rpc.server.response.size"} {
		val, err := exp.GetByName(key)
		require.NoError(t, err, "failed to get metric for %s", key)
		require.Len(t, val.Attributes, 3, "unexpected number of metric attributes for %s", key)
	}
}

func TestClientHandler(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := trace.NewTracerProvider(trace.WithSpanProcessor(sr))

	mp, exp := metrictest.NewTestMeterProvider()

	handler, err := statshandler.NewClientHandler(
		statshandler.WithTracerProvider(tp),
		statshandler.WithMeterProvider(mp),
	)
	require.NoError(t, err)

	g := grpc.NewServer()

	pb.RegisterGreeterServer(g, &testServer{})

	svr := httptest.NewServer(h2c.NewHandler(g, &http2.Server{}))
	defer svr.Close()

	u, err := url.Parse(svr.URL)
	require.NoError(t, err)

	conn, err := grpc.Dial(u.Host, grpc.WithStatsHandler(handler), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)

	defer conn.Close()

	client := pb.NewGreeterClient(conn)

	resp, err := client.SayHello(context.Background(), &pb.HelloRequest{Name: "world"})
	require.NoError(t, err)

	require.Equal(t, "hello world", resp.Message)

	spans := sr.Ended()
	require.Len(t, spans, 1)
	span := spans[0]

	require.Equal(t, otelTrace.SpanKindClient, span.SpanKind())
	require.Equal(t, "/helloworld.Greeter/SayHello", span.Name())
	require.Len(t, span.Events(), 2)

	require.Equal(t, "message", span.Events()[0].Name)
	require.Len(t, span.Events()[0].Attributes, 3)
	require.Equal(t, "message", span.Events()[1].Name)
	require.Len(t, span.Events()[1].Attributes, 3)

	require.Len(t, span.Attributes(), 4)

	traceAttributes := attribute.NewSet(span.Attributes()...)

	expectedAttributes := map[attribute.Key]attribute.Value{
		semconv.RPCSystemKey:         attribute.StringValue("grpc"),
		semconv.RPCServiceKey:        attribute.StringValue("helloworld.Greeter"),
		semconv.RPCMethodKey:         attribute.StringValue("SayHello"),
		semconv.RPCGRPCStatusCodeKey: attribute.Int64Value(0),
	}

	for key, value := range expectedAttributes {
		got, ok := traceAttributes.Value(key)
		require.True(t, ok, "did not find expected span attribute %s", key)
		require.Equal(t, value.AsInterface(), got.AsInterface(), "unexpected value for span attribute %s", key)
	}

	err = exp.Collect(context.Background())
	require.NoError(t, err)

	require.Len(t, exp.GetRecords(), 5)

	expectedKeys := []string{
		"rpc.client.duration",
		"rpc.client.requests_per_rpc",
		"rpc.client.responses_per_rpc",
	}

	for _, key := range expectedKeys {
		val, err := exp.GetByName(key)
		require.NoError(t, err, "failed to get metric for %s", key)

		require.Len(t, val.Attributes, 4, "unexpected number of metric attributes for %s", key)

		attributes := attribute.NewSet(val.Attributes...)

		for key, value := range expectedAttributes {
			got, ok := attributes.Value(key)
			require.True(t, ok, "did not find expected metric attribute %s", key)
			require.Equal(t, value.AsInterface(), got.AsInterface(), "unexpected value for metric attribute %s", key)
		}
	}

	for _, key := range []string{"rpc.client.request.size", "rpc.client.response.size"} {
		val, err := exp.GetByName(key)
		require.NoError(t, err, "failed to get metric for %s", key)
		require.Len(t, val.Attributes, 3, "unexpected number of metric attributes for %s", key)
	}
}

type testServer struct {
	pb.UnimplementedGreeterServer
	code codes.Code
}

// SayHello implements helloworld.GreeterServer
func (s *testServer) SayHello(ctx context.Context, in *pb.HelloRequest) (*pb.HelloReply, error) {
	if s.code != codes.OK {
		return nil, status.Error(s.code, s.code.String())
	}

	return &pb.HelloReply{Message: "hello " + in.GetName()}, nil
}
