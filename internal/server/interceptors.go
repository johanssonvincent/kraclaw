package server

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func loggingInterceptor(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		duration := time.Since(start)

		attrs := []slog.Attr{
			slog.String("method", info.FullMethod),
			slog.Duration("duration", duration),
		}

		if err != nil {
			st, _ := status.FromError(err)
			attrs = append(attrs, slog.String("code", st.Code().String()))
			log.LogAttrs(ctx, slog.LevelError, "gRPC request failed", attrs...)
		} else {
			attrs = append(attrs, slog.String("code", codes.OK.String()))
			log.LogAttrs(ctx, slog.LevelDebug, "gRPC request", attrs...)
		}

		return resp, err
	}
}

// loggingStreamInterceptor is the streaming counterpart of
// loggingInterceptor. Streams (StreamEvents, StreamInbound,
// StreamSandboxOutput, StartChatGPTDeviceAuth) run for minutes at a time, so
// without it a failing stream leaves no server-side trace of the method, the
// duration, or the error code.
func loggingStreamInterceptor(log *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		err := handler(srv, ss)
		duration := time.Since(start)

		attrs := []slog.Attr{
			slog.String("method", info.FullMethod),
			slog.Duration("duration", duration),
			slog.Bool("stream", true),
		}

		if err != nil {
			st, _ := status.FromError(err)
			attrs = append(attrs, slog.String("code", st.Code().String()))
			log.LogAttrs(ss.Context(), slog.LevelError, "gRPC stream failed", attrs...)
		} else {
			attrs = append(attrs, slog.String("code", codes.OK.String()))
			log.LogAttrs(ss.Context(), slog.LevelDebug, "gRPC stream", attrs...)
		}

		return err
	}
}

// recoveryStreamInterceptor is the streaming counterpart of
// recoveryInterceptor. gRPC only recovers panics that an interceptor recovers,
// so without a stream interceptor a nil dereference inside any streaming
// handler terminates the whole control plane instead of failing one RPC.
func recoveryStreamInterceptor(log *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("gRPC stream panic recovered",
					"method", info.FullMethod,
					"panic", r,
					"stack", string(debug.Stack()),
				)

				err = status.Errorf(codes.Internal, "internal server error")
			}
		}()

		return handler(srv, ss)
	}
}

func recoveryInterceptor(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("gRPC panic recovered",
					"method", info.FullMethod,
					"panic", r,
					"stack", string(debug.Stack()),
				)

				err = status.Errorf(codes.Internal, "internal server error")
			}
		}()

		return handler(ctx, req)
	}
}
