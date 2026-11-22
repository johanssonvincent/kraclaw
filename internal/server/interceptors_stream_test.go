package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	testPanicMethod   = "/kraclaw.test.Streams/Boom"
	testHealthyMethod = "/kraclaw.test.Streams/Quiet"
)

// streamTestService is a raw service descriptor: it lets the test drive the
// interceptors with a handler that panics, which a generated service cannot do.
func streamTestService() *grpc.ServiceDesc {
	return &grpc.ServiceDesc{
		ServiceName: "kraclaw.test.Streams",
		HandlerType: (*any)(nil),
		Streams: []grpc.StreamDesc{
			{
				StreamName:    "Boom",
				ServerStreams: true,
				Handler: func(any, grpc.ServerStream) error {
					var uninitialised *struct{ field string }

					return errors.New("unreachable: " + uninitialised.field) //nolint:staticcheck // deliberate nil dereference
				},
			},
			{
				StreamName:    "Quiet",
				ServerStreams: true,
				Handler:       func(any, grpc.ServerStream) error { return nil },
			},
		},
		Metadata: "stream_test.proto",
	}
}

func startStreamTestGRPC(t *testing.T, log *slog.Logger) grpc.ClientConnInterface {
	t.Helper()

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(
		grpc.ChainStreamInterceptor(
			loggingStreamInterceptor(log),
			recoveryStreamInterceptor(log),
		),
	)
	srv.RegisterService(streamTestService(), struct{}{})

	go func() {
		if err := srv.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Logf("grpc serve: %v", err)
		}
	}()

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}

	t.Cleanup(func() {
		_ = conn.Close()
		srv.Stop()
		_ = lis.Close()
	})

	return conn
}

// recvEmpty opens a server-streaming call and blocks for its first message.
// A handler that returns immediately surfaces as io.EOF.
func recvEmpty(ctx context.Context, conn grpc.ClientConnInterface, method string) error {
	stream, err := conn.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, method)
	if err != nil {
		return err
	}

	return stream.RecvMsg(&emptypb.Empty{})
}

// TestStreamInterceptorRecoversPanic is the regression guard for a panic in a
// streaming RPC handler. Before the stream interceptor existed the panic
// escaped gRPC and killed the process, taking down every other RPC with it.
func TestStreamInterceptorRecoversPanic(t *testing.T) {
	var logged bytes.Buffer

	log := slog.New(slog.NewJSONHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
	conn := startStreamTestGRPC(t, log)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := recvEmpty(ctx, conn, testPanicMethod)
	if err == nil {
		t.Fatal("panicking stream returned no error, want codes.Internal")
	}

	if got := status.Code(err); got != codes.Internal {
		t.Fatalf("panicking stream returned code %v (%v), want %v", got, err, codes.Internal)
	}

	// The server must still serve: a panic in one stream may not affect others.
	err = recvEmpty(ctx, conn, testHealthyMethod)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("healthy stream after panic returned %v, want io.EOF", err)
	}

	// The panic has to be observable server-side, with the method name.
	logs := logged.String()
	if !strings.Contains(logs, "gRPC stream panic recovered") {
		t.Fatalf("panic was not logged, got logs: %s", logs)
	}

	if !strings.Contains(logs, testPanicMethod) {
		t.Fatalf("panic log does not name the method %q, got logs: %s", testPanicMethod, logs)
	}

	if !strings.Contains(logs, "stack") {
		t.Fatalf("panic log has no stack trace, got logs: %s", logs)
	}
}

// TestStreamInterceptorLogsFailure checks the streaming logger records the
// method and the resulting status code for an ordinary (non-panic) failure.
func TestStreamInterceptorLogsFailure(t *testing.T) {
	var logged bytes.Buffer

	log := slog.New(slog.NewJSONHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
	conn := startStreamTestGRPC(t, log)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := recvEmpty(ctx, conn, testHealthyMethod); !errors.Is(err, io.EOF) {
		t.Fatalf("healthy stream returned %v, want io.EOF", err)
	}

	logs := logged.String()
	if !strings.Contains(logs, testHealthyMethod) {
		t.Fatalf("stream log does not name the method %q, got logs: %s", testHealthyMethod, logs)
	}

	if !strings.Contains(logs, codes.OK.String()) {
		t.Fatalf("stream log does not record code %q, got logs: %s", codes.OK.String(), logs)
	}
}
