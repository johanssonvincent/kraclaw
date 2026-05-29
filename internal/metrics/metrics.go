package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	SandboxesCreated = promauto.NewCounter(prometheus.CounterOpts{
		Name: "kraclaw_sandboxes_created_total",
		Help: "Total number of sandboxes created",
	})

	SandboxesCompleted = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "kraclaw_sandboxes_completed_total",
		Help: "Total number of sandboxes completed by status",
	}, []string{"status"})

	SandboxDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "kraclaw_sandbox_duration_seconds",
		Help:    "Duration of sandbox executions",
		Buckets: prometheus.ExponentialBuckets(1, 2, 12), // 1s to ~68min
	})

	ActiveSandboxes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "kraclaw_active_sandboxes",
		Help: "Number of currently active sandboxes",
	})

	MessagesReceived = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "kraclaw_messages_received_total",
		Help: "Total messages received by channel",
	}, []string{"channel"})

	MessagesSent = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "kraclaw_messages_sent_total",
		Help: "Total messages sent by channel",
	}, []string{"channel"})

	QueueDepth = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kraclaw_queue_depth",
		Help: "Number of pending messages per group",
	}, []string{"group"})

	ProxyRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "kraclaw_proxy_requests_total",
		Help: "Total credential proxy requests",
	}, []string{"status"})

	ProxyDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "kraclaw_proxy_request_duration_seconds",
		Help:    "Duration of credential proxy requests",
		Buckets: prometheus.DefBuckets,
	})

	TasksExecuted = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "kraclaw_tasks_executed_total",
		Help: "Total scheduled tasks executed by status",
	}, []string{"status"})

	IPCMessagesProcessed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "kraclaw_ipc_messages_processed_total",
		Help: "Total IPC messages processed from agents",
	})

	SandboxSpawnDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "kraclaw_sandbox_spawn_duration_seconds",
		Help:    "Cold-start latency per phase (phase label). ensure_stream: IPC stream/consumer pre-creation, measured before CreateSandbox. crd_create: successful CreateSandbox call only. pod_scheduled/pod_ready: measured from the Sandbox CreationTimestamp to the respective condition. first_output: from CreateSandbox start to the first agent IPC output.",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 3, 5, 8, 13, 21},
	}, []string{"phase"})
)
