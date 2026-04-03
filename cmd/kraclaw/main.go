package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	agentsandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/johanssonvincent/kraclaw/internal/channel"
	"github.com/johanssonvincent/kraclaw/internal/channel/tui"
	"github.com/johanssonvincent/kraclaw/internal/config"
	"github.com/johanssonvincent/kraclaw/internal/credproxy"
	"github.com/johanssonvincent/kraclaw/internal/ipc"
	"github.com/johanssonvincent/kraclaw/internal/orchestrator"
	"github.com/johanssonvincent/kraclaw/internal/provider"
	"github.com/johanssonvincent/kraclaw/internal/queue"
	"github.com/johanssonvincent/kraclaw/internal/sandbox"
	"github.com/johanssonvincent/kraclaw/internal/server"
	"github.com/johanssonvincent/kraclaw/internal/store"
)

var (
	version = "dev"
	scheme  = runtime.NewScheme()
)

func init() {
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		panic(fmt.Sprintf("failed to add client-go scheme: %v", err))
	}
	if err := agentsandboxv1alpha1.AddToScheme(scheme); err != nil {
		panic(fmt.Sprintf("failed to add agent-sandbox scheme: %v", err))
	}
}

func main() {
	startedAt := time.Now().UTC()
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	log := setupLogger(cfg.Logging)
	log.Info("kraclaw starting", "version", version)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Info("received shutdown signal", "signal", sig)
		cancel()
	}()

	// Connect MySQL and run migrations
	mysqlStore, err := store.NewMySQLStore(cfg.MySQL.DSN, cfg.MySQL.MaxOpenConns, cfg.MySQL.MaxIdleConns, cfg.MySQL.ConnMaxLifetime)
	if err != nil {
		log.Error("failed to initialise MySQL store", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := mysqlStore.Close(); err != nil {
			log.Warn("mysql store close", "error", err)
		}
	}()
	log.Info("connected to MySQL, migrations complete")

	// Connect NATS
	nc, err := connectNATS(cfg.NATS, log)
	if err != nil {
		log.Error("failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := nc.Drain(); err != nil {
			log.Error("nats drain", "error", err)
		}
	}()
	log.Info("connected to NATS")

	ipcBroker, err := ipc.NewNATSBroker(nc, log)
	if err != nil {
		log.Error("failed to create IPC broker", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := ipcBroker.Close(); err != nil {
			log.Warn("ipc broker close", "error", err)
		}
	}()

	natsQueue, err := queue.NewNATSQueue(nc, mysqlStore, log)
	if err != nil {
		log.Error("failed to create NATS queue", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := natsQueue.Close(); err != nil {
			log.Warn("nats queue close", "error", err)
		}
	}()

	var (
		kubeConfig  *rest.Config
		k8sClient   kubernetes.Interface
		ctrlClient  client.WithWatch
		sandboxCtrl *sandbox.Controller
	)
	kubeConfig, k8sClient, err = connectKubernetes(cfg.K8s)
	if err != nil {
		log.Warn("failed to connect to Kubernetes; sandbox admin APIs will be degraded", "error", err)
	} else {
		ctrlClient, err = client.NewWithWatch(kubeConfig, client.Options{Scheme: scheme})
		if err != nil {
			log.Warn("failed to create controller-runtime client; sandbox admin APIs will be degraded", "error", err)
		} else {
			agentImages := map[string]string{
				provider.ProviderAnthropic: cfg.K8s.AgentImageAnthropic,
				provider.ProviderOpenAI:    cfg.K8s.AgentImageOpenAI,
			}
			sandboxCtrl, err = sandbox.New(k8sClient, ctrlClient, kubeConfig, cfg.K8s.Namespace, cfg.K8s.AgentImage, agentImages, cfg.NATS.URL, cfg.K8s.SandboxProxyURL)
			if err != nil {
				log.Error("failed to create sandbox controller", "error", err)
				return
			}
			log.Info("connected to Kubernetes", "namespace", cfg.K8s.Namespace)
		}
	}

	// Create TUI channel and orchestrator
	tuiChannel := tui.New(log)

	// Register TUI channel factory in the default registry.
	// The factory returns the pre-created instance so server and orchestrator share state.
	channel.DefaultRegistry.Register("tui", func(cfg channel.ChannelConfig) (channel.Channel, error) {
		tuiChannel.SetConfig(cfg)
		return tuiChannel, nil
	})

	orch, err := orchestrator.New(cfg, mysqlStore, natsQueue, ipcBroker, sandboxCtrl, channel.DefaultRegistry, log)
	if err != nil {
		log.Error("failed to create orchestrator", "error", err)
		return
	}
	orchErr := make(chan error, 1)
	go func() {
		orchErr <- orch.Start(ctx)
	}()

	// Wait briefly for orchestrator startup failure (e.g. no channels, MySQL error).
	select {
	case err := <-orchErr:
		if err != nil && ctx.Err() == nil {
			log.Error("orchestrator failed to start", "error", err)
			os.Exit(1)
		}
	case <-time.After(5 * time.Second):
		log.Info("orchestrator running")
	}

	// Monitor orchestrator for late failures (same pattern as proxy monitor below).
	go func() {
		if err := <-orchErr; err != nil && ctx.Err() == nil {
			log.Error("orchestrator died, initiating shutdown", "error", err)
			cancel()
		}
	}()

	// Start credential proxy
	var proxy *credproxy.Proxy

	// Multi-provider proxy is needed when:
	// 1. Per-group credential encryption is configured, OR
	// 2. OpenAI platform credentials are set (needs provider-aware routing)
	needsMultiProvider := cfg.Proxy.CredentialEncryptionKey != "" || cfg.Proxy.OpenAIAPIKey != ""

	if needsMultiProvider {
		var resolver credproxy.CredentialResolver
		if cfg.Proxy.CredentialEncryptionKey != "" {
			enc, err := credproxy.NewEncryptor(cfg.Proxy.CredentialEncryptionKey)
			if err != nil {
				log.Error("failed to create credential encryptor", "error", err)
				os.Exit(1)
			}
			credStore, err := credproxy.NewCredentialStore(mysqlStore.DB(), enc)
			if err != nil {
				log.Error("failed to create credential store", "error", err)
				os.Exit(1)
			}
			resolver = credproxy.NewDefaultResolver(credStore, cfg.Proxy)
		} else {
			resolver = credproxy.NewDefaultResolver(nil, cfg.Proxy)
		}
		proxy, err = credproxy.NewMultiProviderProxy(cfg.Proxy, resolver)
		if err != nil {
			log.Error("failed to create multi-provider credential proxy", "error", err)
			os.Exit(1)
		}
		log.Info("credential proxy configured with multi-provider support")
	} else {
		proxy, err = credproxy.New(cfg.Proxy)
		if err != nil {
			log.Error("failed to create credential proxy", "error", err)
			os.Exit(1)
		}
	}
	proxyErr := make(chan error, 1)
	go func() {
		proxyErr <- proxy.Start(ctx)
	}()

	// Wait briefly for proxy startup failure (e.g., port conflict).
	select {
	case err := <-proxyErr:
		if err != nil && ctx.Err() == nil {
			log.Error("credential proxy failed to start", "error", err)
			os.Exit(1)
		}
	case <-time.After(2 * time.Second):
		log.Info("credential proxy started", "addr", cfg.Proxy.Addr)
	}

	// Monitor proxy for late failures.
	go func() {
		if err := <-proxyErr; err != nil && ctx.Err() == nil {
			log.Error("credential proxy died, initiating shutdown", "error", err)
			cancel() // triggers graceful shutdown via context cancellation
		}
	}()

	// Start gRPC + REST server
	srv, err := server.New(server.Config{
		GRPCAddr:              cfg.Server.GRPCAddr,
		RESTAddr:              cfg.Server.RESTAddr,
		MetricsPath:           cfg.Metrics.Path,
		GRPCInsecure:          cfg.Server.GRPCInsecure,
		GRPCTLSCertFile:       cfg.Server.GRPCTLSCertFile,
		GRPCTLSKeyFile:        cfg.Server.GRPCTLSKeyFile,
		GRPCTLSClientCAFile:   cfg.Server.GRPCTLSClientCAFile,
		GRPCAllowedCIDRs:      cfg.Server.GRPCAllowedCIDRs,
		GRPCReflectionEnabled: cfg.Server.GRPCReflectionEnabled,
		IPC:                   ipcBroker,
		Version:               version,
		StartedAt:             startedAt,
		Store:                 mysqlStore,
		Sandbox:               sandboxCtrl,
		Kubernetes:            k8sClient,
		DB:                    mysqlStore.DB(),
		TUIChannel:            tuiChannel,
		Channels:              []channel.Channel{tuiChannel},
		Log:                   log,
	})
	if err != nil {
		log.Error("failed to create server", "error", err)
		os.Exit(1)
	}

	log.Info("starting servers",
		"grpc", cfg.Server.GRPCAddr,
		"rest", cfg.Server.RESTAddr,
		"proxy", cfg.Proxy.Addr,
	)

	if err := srv.Start(ctx); err != nil {
		log.Error("server error", "error", err)
	}

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	srv.Stop(shutdownCtx)
	if err := orch.Stop(shutdownCtx); err != nil {
		log.Error("orchestrator shutdown error", "error", err)
	}
	if err := proxy.Stop(shutdownCtx); err != nil {
		log.Error("proxy shutdown error", "error", err)
	}

	log.Info("kraclaw stopped")
}

func setupLogger(cfg config.LoggingConfig) *slog.Logger {
	var handler slog.Handler
	opts := &slog.HandlerOptions{}

	switch cfg.Level {
	case "debug":
		opts.Level = slog.LevelDebug
	case "warn":
		opts.Level = slog.LevelWarn
	case "error":
		opts.Level = slog.LevelError
	default:
		opts.Level = slog.LevelInfo
	}

	if cfg.Format == "text" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	return slog.New(handler)
}

func connectNATS(cfg config.NATSConfig, log *slog.Logger) (*natsgo.Conn, error) {
	nc, err := natsgo.Connect(cfg.URL,
		natsgo.DisconnectErrHandler(func(_ *natsgo.Conn, err error) {
			log.Error("nats disconnected", "error", err)
		}),
		natsgo.ReconnectHandler(func(nc *natsgo.Conn) {
			log.Warn("nats reconnected", "url", nc.ConnectedUrl())
		}),
		natsgo.ClosedHandler(func(_ *natsgo.Conn) {
			log.Error("nats connection permanently closed")
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := nc.Flush(); err != nil {
		nc.Close()
		return nil, fmt.Errorf("flush: %w", err)
	}
	return nc, nil
}

func connectKubernetes(cfg config.K8sConfig) (*rest.Config, kubernetes.Interface, error) {
	var (
		kubeConfig *rest.Config
		err        error
	)
	if cfg.InCluster {
		kubeConfig, err = rest.InClusterConfig()
	} else {
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			loadingRules,
			&clientcmd.ConfigOverrides{},
		)
		kubeConfig, err = clientConfig.ClientConfig()
	}
	if err != nil {
		return nil, nil, err
	}
	clientset, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		return nil, nil, err
	}
	return kubeConfig, clientset, nil
}
