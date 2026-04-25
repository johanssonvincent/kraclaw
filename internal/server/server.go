package server

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/client-go/kubernetes"

	"github.com/johanssonvincent/kraclaw/internal/auth/chatgpt"
	"github.com/johanssonvincent/kraclaw/internal/channel"
	"github.com/johanssonvincent/kraclaw/internal/channel/tui"
	"github.com/johanssonvincent/kraclaw/internal/credproxy"
	"github.com/johanssonvincent/kraclaw/internal/ipc"
	"github.com/johanssonvincent/kraclaw/internal/provider"
	"github.com/johanssonvincent/kraclaw/internal/sandbox"
	"github.com/johanssonvincent/kraclaw/internal/store"
	kraclawv1 "github.com/johanssonvincent/kraclaw/pkg/pb/kraclawv1"
)

// Server manages the gRPC server and REST gateway.
type Server struct {
	grpcServer   *grpc.Server
	restServer   *http.Server
	grpcAddr     string
	restAddr     string
	allowedCIDRs []*net.IPNet
	events       *eventHub
	sandbox      *sandbox.Controller
	db           *sql.DB
	log          *slog.Logger
}

// Config holds server configuration.
type Config struct {
	GRPCAddr              string
	RESTAddr              string
	MetricsPath           string
	GRPCInsecure          bool
	GRPCTLSCertFile       string
	GRPCTLSKeyFile        string
	GRPCTLSClientCAFile   string
	GRPCAllowedCIDRs      string
	GRPCReflectionEnabled bool
	IPC                   ipc.IPCBroker
	Version               string
	StartedAt             time.Time
	Store                 store.Store
	Sandbox               *sandbox.Controller
	Kubernetes            kubernetes.Interface
	DB                    *sql.DB
	TUIChannel            *tui.TUI
	Channels              []channel.Channel
	Log                   *slog.Logger

	// ChatGPTClient drives the ChatGPT OAuth device-code flow. When nil, the
	// AuthService is not registered.
	ChatGPTClient *chatgpt.Client
	// CredentialStore persists ChatGPT OAuth tokens. When nil, the AuthService
	// is not registered (the Anthropic-only legacy proxy path doesn't need it).
	CredentialStore *credproxy.CredentialStore
	// Providers is the shared provider registry. When nil, services that need
	// it construct their own provider.NewRegistry() — pass an instance here
	// when multiple services should share the same registry.
	Providers *provider.Registry
}

// New creates a new Server.
func New(cfg Config) (*Server, error) {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.StartedAt.IsZero() {
		cfg.StartedAt = time.Now().UTC()
	}

	allowedCIDRs, err := parseAllowedCIDRs(cfg.GRPCAllowedCIDRs)
	if err != nil {
		return nil, err
	}

	grpcOpts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(
			loggingInterceptor(cfg.Log),
			recoveryInterceptor(cfg.Log),
		),
	}

	if cfg.GRPCInsecure {
		cfg.Log.Warn("gRPC server running WITHOUT TLS — use only for in-pod access")
	} else {
		tlsConfig, err := loadGRPCTLSConfig(cfg.GRPCTLSCertFile, cfg.GRPCTLSKeyFile, cfg.GRPCTLSClientCAFile)
		if err != nil {
			return nil, err
		}
		grpcOpts = append(grpcOpts, grpc.Creds(credentials.NewTLS(tlsConfig)))
	}

	grpcSrv := grpc.NewServer(grpcOpts...)

	// Register health service
	healthSrv := health.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcSrv, healthSrv)
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	if cfg.GRPCReflectionEnabled {
		reflection.Register(grpcSrv)
	}

	events := newEventHub(cfg.Log)
	registerAPIServices(grpcSrv, cfg, events)

	// REST gateway with health and metrics
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthzHandler())
	mux.HandleFunc("/readyz", readyzHandler(cfg.DB))
	if cfg.MetricsPath != "" {
		mux.Handle(cfg.MetricsPath, promhttp.Handler())
	}

	restSrv := &http.Server{
		Addr:         cfg.RESTAddr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	return &Server{
		grpcServer:   grpcSrv,
		restServer:   restSrv,
		grpcAddr:     cfg.GRPCAddr,
		restAddr:     cfg.RESTAddr,
		allowedCIDRs: allowedCIDRs,
		events:       events,
		sandbox:      cfg.Sandbox,
		db:           cfg.DB,
		log:          cfg.Log,
	}, nil
}

// GRPCServer returns the underlying gRPC server for service registration.
func (s *Server) GRPCServer() *grpc.Server {
	return s.grpcServer
}

// Start starts both the gRPC and REST servers.
func (s *Server) Start(ctx context.Context) error {
	errCh := make(chan error, 2)

	if s.sandbox != nil {
		go s.watchSandboxEvents(ctx)
	}

	// Start gRPC server
	go func() {
		lis, err := net.Listen("tcp", s.grpcAddr)
		if err != nil {
			errCh <- fmt.Errorf("gRPC listen: %w", err)
			return
		}
		lis = &allowlistListener{
			Listener: lis,
			allowed:  s.allowedCIDRs,
			log:      s.log,
		}
		s.log.Info("gRPC server listening", "addr", s.grpcAddr)
		if err := s.grpcServer.Serve(lis); err != nil {
			errCh <- fmt.Errorf("gRPC serve: %w", err)
		}
	}()

	// Start REST gateway
	go func() {
		s.log.Info("REST gateway listening", "addr", s.restAddr)
		if err := s.restServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("REST serve: %w", err)
		}
	}()

	// Wait for context cancellation or error
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return nil
	}
}

// Stop gracefully stops both servers.
func (s *Server) Stop(ctx context.Context) {
	s.log.Info("shutting down servers")
	s.grpcServer.GracefulStop()
	if err := s.restServer.Shutdown(ctx); err != nil {
		s.log.Error("REST server shutdown error", "error", err)
	}
}

func (s *Server) watchSandboxEvents(ctx context.Context) {
	ch, err := s.sandbox.WatchSandboxes(ctx)
	if err != nil {
		s.log.Error("failed to watch sandbox events", "error", err)
		s.events.publish(&kraclawv1.Event{
			Timestamp: timestamppb.Now(),
			Type:      "error",
			Source:    "sandbox",
			Message:   fmt.Sprintf("failed to watch sandbox events: %v", err),
		})
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			s.events.publish(sandboxEventToProto(evt))
		}
	}
}

func healthzHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
}

func readyzHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		// Check MySQL
		if err := db.PingContext(ctx); err != nil {
			http.Error(w, fmt.Sprintf("mysql: %v", err), http.StatusServiceUnavailable)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
}
