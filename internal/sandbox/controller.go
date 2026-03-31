package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	agentsandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/johanssonvincent/kraclaw/internal/store"
)

const (
	labelManagedBy = "app.kubernetes.io/managed-by"
	labelRole      = "kraclaw.io/role"
	labelGroup     = "kraclaw.io/group"

	managedByValue = "kraclaw"
	roleAgent      = "agent"

	runAsUser = int64(1000)

	sandboxCreateMaxRetries  = 3
	sandboxCreateBaseBackoff = 100 * time.Millisecond
)

// Controller manages agent sandboxes via Sandbox resources.
type Controller struct {
	clientset   kubernetes.Interface
	ctrlClient  client.WithWatch
	config      *rest.Config
	namespace   string
	agentImage  string            // Legacy fallback
	agentImages map[string]string // provider -> image
	redisURL    string
	proxyURL    string
	log         *slog.Logger
}

// SandboxConfig holds the parameters for creating a new sandbox.
type SandboxConfig struct {
	GroupFolder   string
	GroupJID      string
	SessionID     string
	IsMain        bool
	Timeout       time.Duration
	Input         string // JSON-encoded input, written to Redis before Job creation
	AssistantName string
	Model         string
	SessionsPVC     string // PVC for .claude/ session transcripts (default: kraclaw-sessions)
	GroupsPVC       string // PVC for per-group workspace (default: kraclaw-groups)
	DataPVC         string // PVC for read-only global config (default: kraclaw-data)
	ContainerConfig *store.ContainerConfig
}

// Validate checks that required SandboxConfig fields are set.
func (c *SandboxConfig) Validate() error {
	if c.GroupFolder == "" {
		return fmt.Errorf("sandbox: GroupFolder is required")
	}
	if c.GroupJID == "" {
		return fmt.Errorf("sandbox: GroupJID is required")
	}
	return nil
}

// SandboxState represents the lifecycle state of a sandbox.
type SandboxState string

const (
	StatePending   SandboxState = "pending"
	StateRunning   SandboxState = "running"
	StateCompleted SandboxState = "completed"
	StateFailed    SandboxState = "failed"
)

// SandboxStatus represents the current state of a sandbox.
type SandboxStatus struct {
	Name      string
	Group     string
	State     SandboxState
	StartTime *time.Time
	EndTime   *time.Time
}

// New creates a new sandbox controller.
func New(clientset kubernetes.Interface, ctrlClient client.WithWatch, config *rest.Config, namespace, agentImage string, agentImages map[string]string, redisURL, proxyURL string) (*Controller, error) {
	if clientset == nil {
		return nil, fmt.Errorf("sandbox: kubernetes clientset is required")
	}
	if agentImages == nil {
		agentImages = map[string]string{}
	}
	return &Controller{
		clientset:   clientset,
		ctrlClient:  ctrlClient,
		config:      config,
		namespace:   namespace,
		agentImage:  agentImage,
		agentImages: agentImages,
		redisURL:    redisURL,
		proxyURL:    proxyURL,
		log:         slog.Default().With("component", "sandbox"),
	}, nil
}

// agentImageForProvider returns the container image for the given provider,
// falling back to the legacy agentImage when no provider-specific image is configured.
func (c *Controller) agentImageForProvider(provider string) string {
	if provider != "" {
		if img, ok := c.agentImages[provider]; ok && img != "" {
			return img
		}
	}
	return c.agentImage
}

// isTransientError reports whether err is likely a transient K8s API failure
// that can be retried. Non-transient errors (validation, conflict, forbidden,
// not-found) must not be retried as they will not self-resolve.
func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "unavailable") ||
		strings.Contains(msg, "temporary")
}

// CreateSandbox creates a new Sandbox resource for an agent.
// Transient K8s API errors are retried up to sandboxCreateMaxRetries times with
// exponential backoff. Non-transient errors (validation, conflict, forbidden) fail fast.
func (c *Controller) CreateSandbox(ctx context.Context, cfg SandboxConfig) (*SandboxStatus, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	name, err := c.generateSandboxName(cfg.GroupFolder)
	if err != nil {
		return nil, fmt.Errorf("sandbox: generate name: %w", err)
	}

	var lastErr error
	backoff := sandboxCreateBaseBackoff

	for attempt := 1; attempt <= sandboxCreateMaxRetries; attempt++ {
		if attempt > 1 {
			c.log.Warn("retrying sandbox creation",
				"attempt", attempt, "group", cfg.GroupFolder, "error", lastErr)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			backoff *= 2
		}

		sb := c.buildSandbox(name, cfg)
		if err := c.ctrlClient.Create(ctx, sb); err != nil {
			lastErr = err
			if isTransientError(err) {
				continue
			}
			// Non-transient: fail immediately.
			return nil, fmt.Errorf("sandbox: create sandbox: %w", err)
		}

		c.log.Info("sandbox created", "name", name, "group", cfg.GroupFolder)
		return sandboxToStatus(sb), nil
	}

	return nil, fmt.Errorf("sandbox: create sandbox failed after %d retries: %w", sandboxCreateMaxRetries, lastErr)
}

// StopSandbox deletes a Sandbox resource.
func (c *Controller) StopSandbox(ctx context.Context, name string) error {
	sandbox := &agentsandboxv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: c.namespace,
		},
	}
	if err := c.ctrlClient.Delete(ctx, sandbox); err != nil {
		return fmt.Errorf("sandbox: delete sandbox %s: %w", name, err)
	}
	c.log.Info("sandbox stopped", "name", name)
	return nil
}

// GetSandbox returns the status of a single Sandbox resource.
func (c *Controller) GetSandbox(ctx context.Context, name string) (*SandboxStatus, error) {
	sandbox := &agentsandboxv1alpha1.Sandbox{}
	err := c.ctrlClient.Get(ctx, client.ObjectKey{Namespace: c.namespace, Name: name}, sandbox)
	if err != nil {
		return nil, fmt.Errorf("sandbox: get sandbox %s: %w", name, err)
	}
	return sandboxToStatus(sandbox), nil
}

// ListSandboxes returns the status of all kraclaw-managed Sandbox resources.
func (c *Controller) ListSandboxes(ctx context.Context) ([]SandboxStatus, error) {
	var sandboxes agentsandboxv1alpha1.SandboxList
	err := c.ctrlClient.List(ctx, &sandboxes, client.InNamespace(c.namespace), client.MatchingLabels{
		labelManagedBy: managedByValue,
	})
	if err != nil {
		return nil, fmt.Errorf("sandbox: list sandboxes: %w", err)
	}

	statuses := make([]SandboxStatus, 0, len(sandboxes.Items))
	for i := range sandboxes.Items {
		statuses = append(statuses, *sandboxToStatus(&sandboxes.Items[i]))
	}
	return statuses, nil
}

// HasActiveSandbox checks whether a running or pending Sandbox exists for the given group.
func (c *Controller) HasActiveSandbox(ctx context.Context, groupFolder string) (bool, error) {
	var sandboxes agentsandboxv1alpha1.SandboxList
	err := c.ctrlClient.List(ctx, &sandboxes, client.InNamespace(c.namespace), client.MatchingLabels{
		labelManagedBy: managedByValue,
		labelGroup:     groupFolder,
	})
	if err != nil {
		return false, fmt.Errorf("sandbox: list sandboxes for group %s: %w", groupFolder, err)
	}
	for i := range sandboxes.Items {
		s := sandboxToStatus(&sandboxes.Items[i])
		if s.State == StatePending || s.State == StateRunning {
			return true, nil
		}
	}
	return false, nil
}

func (c *Controller) generateSandboxName(folder string) (string, error) {
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + 32 // lowercase
		}
		return '-'
	}, folder)
	if len(safe) > 40 {
		safe = safe[:40]
	}

	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("kraclaw-agent-%s-%s", safe, hex.EncodeToString(b)), nil
}

// pvcName returns cfg value if non-empty, or the provided default.
func pvcName(configured, defaultName string) string {
	if configured != "" {
		return configured
	}
	return defaultName
}

// buildSandbox constructs a Sandbox resource with the full pod spec and GROUP_FOLDER injected.
// The pod spec mirrors the kraclaw-agent-template SandboxTemplate but with per-group env vars
// resolved at creation time, which is required because SandboxClaimSpec has no injection mechanism.
func (c *Controller) buildSandbox(name string, cfg SandboxConfig) *agentsandboxv1alpha1.Sandbox {
	labels := map[string]string{
		labelManagedBy: managedByValue,
		labelRole:      roleAgent,
		labelGroup:     cfg.GroupFolder,
	}

	sessionsPVC := pvcName(cfg.SessionsPVC, "kraclaw-sessions")
	groupsPVC := pvcName(cfg.GroupsPVC, "kraclaw-groups")
	dataPVC := pvcName(cfg.DataPVC, "kraclaw-data")

	groupFolderEnv := corev1.EnvVar{Name: "GROUP_FOLDER", Value: cfg.GroupFolder}

	provider := ""
	if cfg.ContainerConfig != nil {
		provider = cfg.ContainerConfig.Provider
	}
	image := c.agentImageForProvider(provider)

	// Build env vars based on provider.
	envVars := []corev1.EnvVar{
		groupFolderEnv,
		{Name: "REDIS_URL", Value: c.redisURL},
		{Name: "KRACLAW_PROXY_URL", Value: c.proxyURL},
		{Name: "KRACLAW_PROVIDER", Value: provider},
		{Name: "KRACLAW_GROUP", Value: cfg.GroupJID},
	}

	// Determine HOME path for session mount.
	homePath := "/home/node"

	switch provider {
	case "openai":
		model := ""
		if cfg.ContainerConfig != nil {
			model = cfg.ContainerConfig.Model
		}
		envVars = append(envVars, corev1.EnvVar{Name: "OPENAI_MODEL", Value: model})
		envVars = append(envVars, corev1.EnvVar{Name: "HOME", Value: "/home/nonroot"})
		homePath = "/home/nonroot"
	default:
		// Anthropic (legacy + explicit).
		envVars = append(envVars,
			corev1.EnvVar{Name: "ANTHROPIC_BASE_URL", Value: c.proxyURL},
			corev1.EnvVar{Name: "CLAUDE_CODE_OAUTH_TOKEN", Value: "placeholder"},
			corev1.EnvVar{Name: "HOME", Value: "/home/node"},
		)
	}

	nonRoot := true
	allowPrivEsc := false
	runAs := runAsUser
	replicas := int32(1)

	// Build the agent container.
	container := corev1.Container{
		Name:       "agent",
		Image:      image,
		WorkingDir: "/workspace",
		Env:        envVars,
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:        "sessions",
				MountPath:   homePath + "/.claude",
				SubPathExpr: "$(GROUP_FOLDER)/.claude",
			},
			{
				Name:        "groups",
				MountPath:   "/workspace",
				SubPathExpr: "$(GROUP_FOLDER)",
			},
			{
				Name:      "data",
				MountPath: "/config",
				ReadOnly:  true,
			},
		},
	}

	// Legacy Node.js agent needs explicit command.
	if (provider == "" || provider == "anthropic") && image == c.agentImage {
		container.Command = []string{"node", "/app/dist/index.js", "--group", "$(GROUP_FOLDER)"}
	}

	sb := &agentsandboxv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: c.namespace,
			Labels:    labels,
		},
		Spec: agentsandboxv1alpha1.SandboxSpec{
			Replicas: &replicas,
			PodTemplate: agentsandboxv1alpha1.PodTemplate{
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: boolPtr(false),
					ImagePullSecrets: []corev1.LocalObjectReference{
						{Name: "ghcr-pull-secret"},
					},
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: &nonRoot,
						RunAsUser:    &runAs,
					},
					InitContainers: []corev1.Container{
						{
							Name:  "init-dirs",
							Image: "busybox",
							Command: []string{
								"sh", "-c",
								"mkdir -p /sessions/$(GROUP_FOLDER)/.claude && mkdir -p /groups/$(GROUP_FOLDER)/archives",
							},
							Env: []corev1.EnvVar{groupFolderEnv},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "sessions", MountPath: "/sessions"},
								{Name: "groups", MountPath: "/groups"},
							},
							SecurityContext: &corev1.SecurityContext{
								RunAsUser:                &runAs,
								AllowPrivilegeEscalation: &allowPrivEsc,
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("10m"),
									corev1.ResourceMemory: resource.MustParse("32Mi"),
								},
							},
						},
					},
					Containers: []corev1.Container{container},
					Volumes: []corev1.Volume{
						{
							Name: "sessions",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: sessionsPVC,
								},
							},
						},
						{
							Name: "groups",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: groupsPVC,
								},
							},
						},
						{
							Name: "data",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: dataPVC,
									ReadOnly:  true,
								},
							},
						},
					},
				},
			},
		},
	}

	// Apply additional mounts from group ContainerConfig.
	if cfg.ContainerConfig != nil {
		podSpec := &sb.Spec.PodTemplate.Spec
		for i, mount := range cfg.ContainerConfig.AdditionalMounts {
			volName := fmt.Sprintf("extra-%d", i)
			mountPath := mount.ContainerPath
			if mountPath == "" {
				mountPath = mount.HostPath
			}
			hostPathType := corev1.HostPathDirectory
			podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
				Name: volName,
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: mount.HostPath,
						Type: &hostPathType,
					},
				},
			})
			podSpec.Containers[0].VolumeMounts = append(podSpec.Containers[0].VolumeMounts, corev1.VolumeMount{
				Name:      volName,
				MountPath: mountPath,
				ReadOnly:  mount.ReadOnly,
			})
		}
	}

	return sb
}

func sandboxToStatus(sandbox *agentsandboxv1alpha1.Sandbox) *SandboxStatus {
	s := &SandboxStatus{
		Name:  sandbox.Name,
		Group: sandbox.Labels[labelGroup],
		State: StatePending,
	}

	for _, cond := range sandbox.Status.Conditions {
		if cond.Type == string(agentsandboxv1alpha1.SandboxConditionReady) {
			if cond.Status == metav1.ConditionTrue {
				s.State = StateRunning
				t := cond.LastTransitionTime.Time
				s.StartTime = &t
			} else if cond.Reason == "Succeeded" {
				s.State = StateCompleted
				t := cond.LastTransitionTime.Time
				s.EndTime = &t
			} else if cond.Reason == "Failed" {
				s.State = StateFailed
				t := cond.LastTransitionTime.Time
				s.EndTime = &t
			}
		}
	}

	return s
}

func boolPtr(b bool) *bool {
	return &b
}
