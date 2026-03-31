# Multi-Provider Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add OpenAI as a second AI provider with per-group provider selection, per-group credential storage, and a lightweight Go agent binary.

**Architecture:** Provider field on ContainerConfig selects agent image and credential routing. Credential proxy resolves per-group API keys from MySQL (with platform-level fallback). A shared `pkg/agent/` Go library handles IPC; `cmd/kraclaw-agent-openai/` implements the OpenAI-specific conversation loop.

**Tech Stack:** Go 1.26, OpenAI Go SDK (`github.com/openai/openai-go`), Redis Streams IPC, MySQL credential storage, AES-GCM encryption, K8s Sandbox resources, GitHub Actions CI.

---

## File Structure

### New Files

| File | Purpose |
|------|---------|
| `internal/provider/registry.go` | Provider registry with per-provider model validation |
| `internal/provider/registry_test.go` | Tests for provider registry |
| `internal/credproxy/credential_store.go` | CredentialStore interface + MySQL implementation |
| `internal/credproxy/credential_store_test.go` | Tests for credential store |
| `internal/credproxy/encrypt.go` | AES-GCM encryption helpers for API keys |
| `internal/credproxy/encrypt_test.go` | Tests for encryption |
| `migrations/20260331000001_credentials.up.sql` | Create credentials table |
| `migrations/20260331000001_credentials.down.sql` | Drop credentials table |
| `pkg/agent/ipc.go` | Shared IPC client for Go agents |
| `pkg/agent/ipc_test.go` | Tests for IPC client |
| `pkg/agent/agent.go` | Agent lifecycle scaffold (connect, run, shutdown) |
| `cmd/kraclaw-agent-openai/main.go` | OpenAI agent binary entry point |
| `cmd/kraclaw-agent-openai/Dockerfile` | Multi-stage distroless Dockerfile |
| `.github/workflows/kraclaw-agent-openai.yaml` | CI workflow for OpenAI agent image |

### Modified Files

| File | Change |
|------|--------|
| `internal/store/store.go` | Add `Provider` field to `ContainerConfig` |
| `internal/config/config.go` | Multi-provider proxy config, per-provider agent images, encryption key |
| `internal/credproxy/proxy.go` | Multi-provider routing with per-group credential lookup |
| `internal/orchestrator/oauth_models.go` | Replace with provider registry integration |
| `internal/orchestrator/commands.go` | Update `/models` and `/model` to use provider registry |
| `internal/orchestrator/orchestrator.go` | Pass provider to SandboxConfig |
| `internal/sandbox/controller.go` | Provider-aware image selection + env vars |
| `internal/server/api_services.go` | Validate provider+model on RegisterGroup |
| `Makefile` | Add `build-agent-openai` target |
| `go.mod` | Add `github.com/openai/openai-go` dependency |

---

### Task 1: Provider Registry

**Files:**
- Create: `internal/provider/registry.go`
- Create: `internal/provider/registry_test.go`

- [ ] **Step 1: Write failing tests for provider registry**

```go
// internal/provider/registry_test.go
package provider

import "testing"

func TestNewRegistry_ContainsAnthropic(t *testing.T) {
	r := NewRegistry()
	p, ok := r.Get("anthropic")
	if !ok {
		t.Fatal("expected anthropic provider")
	}
	if p.DefaultModel == "" {
		t.Fatal("expected default model")
	}
}

func TestNewRegistry_ContainsOpenAI(t *testing.T) {
	r := NewRegistry()
	p, ok := r.Get("openai")
	if !ok {
		t.Fatal("expected openai provider")
	}
	if p.DefaultModel == "" {
		t.Fatal("expected default model")
	}
}

func TestRegistry_ValidateModel_Valid(t *testing.T) {
	r := NewRegistry()
	if err := r.ValidateModel("anthropic", "claude-sonnet-4-6"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRegistry_ValidateModel_InvalidModel(t *testing.T) {
	r := NewRegistry()
	if err := r.ValidateModel("anthropic", "gpt-5.4"); err == nil {
		t.Fatal("expected error for wrong provider model")
	}
}

func TestRegistry_ValidateModel_UnknownProvider(t *testing.T) {
	r := NewRegistry()
	if err := r.ValidateModel("gemini", "gemini-pro"); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestRegistry_ValidateModel_EmptyModelAllowed(t *testing.T) {
	r := NewRegistry()
	if err := r.ValidateModel("openai", ""); err != nil {
		t.Fatalf("empty model should be allowed (uses default): %v", err)
	}
}

func TestRegistry_DefaultProvider(t *testing.T) {
	r := NewRegistry()
	if r.DefaultProvider() != "anthropic" {
		t.Fatalf("expected default provider anthropic, got %s", r.DefaultProvider())
	}
}

func TestRegistry_Models_ReturnsCorrectProvider(t *testing.T) {
	r := NewRegistry()
	models := r.Models("openai")
	if len(models) == 0 {
		t.Fatal("expected openai models")
	}
	for _, m := range models {
		if m.ID == "claude-sonnet-4-6" {
			t.Fatal("openai models should not contain claude models")
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/provider/ -v`
Expected: FAIL — package does not exist

- [ ] **Step 3: Implement provider registry**

```go
// internal/provider/registry.go
package provider

import "fmt"

// ModelInfo describes a single model offered by a provider.
type ModelInfo struct {
	ID          string
	DisplayName string
}

// ProviderInfo describes a supported AI provider.
type ProviderInfo struct {
	ID           string
	DisplayName  string
	Models       []ModelInfo
	DefaultModel string
}

// Registry holds all known providers and their models.
type Registry struct {
	providers map[string]ProviderInfo
}

// NewRegistry creates a registry pre-populated with Anthropic and OpenAI.
func NewRegistry() *Registry {
	r := &Registry{providers: make(map[string]ProviderInfo)}

	r.providers["anthropic"] = ProviderInfo{
		ID:          "anthropic",
		DisplayName: "Anthropic",
		DefaultModel: "claude-sonnet-4-6",
		Models: []ModelInfo{
			{ID: "claude-opus-4-6", DisplayName: "Claude Opus 4.6"},
			{ID: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6"},
			{ID: "claude-haiku-4-5", DisplayName: "Claude Haiku 4.5"},
			{ID: "claude-sonnet-4-5", DisplayName: "Claude Sonnet 4.5"},
			{ID: "claude-opus-4-5", DisplayName: "Claude Opus 4.5"},
			{ID: "claude-opus-4-1", DisplayName: "Claude Opus 4.1"},
			{ID: "claude-sonnet-4-0", DisplayName: "Claude Sonnet 4"},
			{ID: "claude-opus-4-0", DisplayName: "Claude Opus 4"},
		},
	}

	r.providers["openai"] = ProviderInfo{
		ID:          "openai",
		DisplayName: "OpenAI",
		DefaultModel: "gpt-5.4",
		Models: []ModelInfo{
			{ID: "gpt-5.4", DisplayName: "GPT-5.4"},
			{ID: "gpt-5.4-mini", DisplayName: "GPT-5.4 Mini"},
			{ID: "gpt-5.4-nano", DisplayName: "GPT-5.4 Nano"},
			{ID: "gpt-5.4-pro", DisplayName: "GPT-5.4 Pro"},
			{ID: "gpt-5.3-codex", DisplayName: "GPT-5.3 Codex"},
			{ID: "o3-mini", DisplayName: "o3-mini"},
		},
	}

	return r
}

// Get returns a provider by ID.
func (r *Registry) Get(id string) (ProviderInfo, bool) {
	p, ok := r.providers[id]
	return p, ok
}

// DefaultProvider returns "anthropic" for backwards compatibility.
func (r *Registry) DefaultProvider() string {
	return "anthropic"
}

// ValidateModel checks that model belongs to provider. Empty model is valid (uses default).
func (r *Registry) ValidateModel(providerID, model string) error {
	p, ok := r.providers[providerID]
	if !ok {
		return fmt.Errorf("unknown provider %q", providerID)
	}
	if model == "" {
		return nil
	}
	for _, m := range p.Models {
		if m.ID == model {
			return nil
		}
	}
	return fmt.Errorf("model %q is not valid for provider %q", model, providerID)
}

// Models returns the model list for a provider. Returns nil for unknown providers.
func (r *Registry) Models(providerID string) []ModelInfo {
	p, ok := r.providers[providerID]
	if !ok {
		return nil
	}
	out := make([]ModelInfo, len(p.Models))
	copy(out, p.Models)
	return out
}

// Providers returns all registered provider IDs.
func (r *Registry) Providers() []string {
	ids := make([]string, 0, len(r.providers))
	for id := range r.providers {
		ids = append(ids, id)
	}
	return ids
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/provider/ -v`
Expected: PASS — all 7 tests

- [ ] **Step 5: Commit**

```bash
git add internal/provider/registry.go internal/provider/registry_test.go
git commit -m "feat: add provider registry with Anthropic and OpenAI models"
```

---

### Task 2: Add Provider Field to ContainerConfig

**Files:**
- Modify: `internal/store/store.go:13-17`

- [ ] **Step 1: Write failing test**

Create a test that verifies Provider survives JSON round-trip:

```go
// Add to internal/store/store_test.go (or create it if it doesn't exist)
// If the file doesn't exist, create internal/store/container_config_test.go
package store

import "testing"

func TestContainerConfig_ProviderJSONRoundTrip(t *testing.T) {
	cc := &ContainerConfig{
		Model:    "gpt-5.4",
		Provider: "openai",
		Timeout:  30000,
	}
	data, err := ContainerConfigJSON(cc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err := ParseContainerConfig(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Provider != "openai" {
		t.Fatalf("expected provider openai, got %q", parsed.Provider)
	}
}

func TestContainerConfig_ProviderDefaultsEmpty(t *testing.T) {
	data := []byte(`{"model":"claude-sonnet-4-6"}`)
	parsed, err := ParseContainerConfig(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Provider != "" {
		t.Fatalf("expected empty provider for legacy config, got %q", parsed.Provider)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestContainerConfig_Provider -v -short`
Expected: FAIL — `cc.Provider` field does not exist

- [ ] **Step 3: Add Provider field to ContainerConfig**

In `internal/store/store.go`, change the struct:

```go
// ContainerConfig holds per-group container settings.
type ContainerConfig struct {
	AdditionalMounts []AdditionalMount `json:"additionalMounts,omitempty"`
	Timeout          int               `json:"timeout,omitempty"`  // milliseconds, default 300000
	Model            string            `json:"model,omitempty"`
	Provider         string            `json:"provider,omitempty"` // "openai", "anthropic" — empty defaults to "anthropic"
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestContainerConfig_Provider -v -short`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/store/store.go internal/store/container_config_test.go
git commit -m "feat: add Provider field to ContainerConfig"
```

---

### Task 3: AES-GCM Encryption Helpers

**Files:**
- Create: `internal/credproxy/encrypt.go`
- Create: `internal/credproxy/encrypt_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/credproxy/encrypt_test.go
package credproxy

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
)

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keyHex := hex.EncodeToString(key)

	enc, err := NewEncryptor(keyHex)
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}

	plaintext := "sk-proj-abc123secretkey"
	ciphertext, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if ciphertext == plaintext {
		t.Fatal("ciphertext should differ from plaintext")
	}

	decrypted, err := enc.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if decrypted != plaintext {
		t.Fatalf("expected %q, got %q", plaintext, decrypted)
	}
}

func TestEncryptDecrypt_DifferentCiphertextEachTime(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	enc, err := NewEncryptor(hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}

	c1, _ := enc.Encrypt("same-input")
	c2, _ := enc.Encrypt("same-input")
	if c1 == c2 {
		t.Fatal("encrypting same input should produce different ciphertext (random nonce)")
	}
}

func TestNewEncryptor_InvalidKeyLength(t *testing.T) {
	_, err := NewEncryptor("tooshort")
	if err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestDecrypt_TamperedCiphertext(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	enc, err := NewEncryptor(hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}

	ciphertext, _ := enc.Encrypt("secret")
	// Tamper with the ciphertext.
	tampered := ciphertext[:len(ciphertext)-2] + "xx"
	_, err = enc.Decrypt(tampered)
	if err == nil {
		t.Fatal("expected error for tampered ciphertext")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/credproxy/ -run TestEncrypt -v -short`
Expected: FAIL — `NewEncryptor` undefined

- [ ] **Step 3: Implement encryption helpers**

```go
// internal/credproxy/encrypt.go
package credproxy

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
)

// Encryptor provides AES-256-GCM encryption for API keys at rest.
type Encryptor struct {
	gcm cipher.AEAD
}

// NewEncryptor creates an encryptor from a hex-encoded 32-byte key.
func NewEncryptor(keyHex string) (*Encryptor, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, fmt.Errorf("decode encryption key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("encryption key must be 32 bytes (64 hex chars), got %d bytes", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	return &Encryptor{gcm: gcm}, nil
}

// Encrypt encrypts plaintext and returns a base64-encoded string (nonce + ciphertext).
func (e *Encryptor) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, e.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	sealed := e.gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt decrypts a base64-encoded string produced by Encrypt.
func (e *Encryptor) Decrypt(ciphertext string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}
	nonceSize := e.gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, sealed := data[:nonceSize], data[nonceSize:]
	plaintext, err := e.gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(plaintext), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/credproxy/ -run TestEncrypt -v -short`
Expected: PASS — all 4 tests

- [ ] **Step 5: Commit**

```bash
git add internal/credproxy/encrypt.go internal/credproxy/encrypt_test.go
git commit -m "feat: add AES-GCM encryption helpers for credential storage"
```

---

### Task 4: Credential Store (Migration + Interface + MySQL Implementation)

**Files:**
- Create: `migrations/20260331000001_credentials.up.sql`
- Create: `migrations/20260331000001_credentials.down.sql`
- Create: `internal/credproxy/credential_store.go`
- Create: `internal/credproxy/credential_store_test.go`

- [ ] **Step 1: Create the database migration**

```sql
-- migrations/20260331000001_credentials.up.sql
CREATE TABLE IF NOT EXISTS credentials (
    group_jid VARCHAR(255) PRIMARY KEY,
    provider VARCHAR(64) NOT NULL,
    api_key_encrypted TEXT NOT NULL,
    oauth_token_encrypted TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
);
```

```sql
-- migrations/20260331000001_credentials.down.sql
DROP TABLE IF EXISTS credentials;
```

- [ ] **Step 2: Write failing tests for credential store**

```go
// internal/credproxy/credential_store_test.go
package credproxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func testEncryptor(t *testing.T) *Encryptor {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	enc, err := NewEncryptor(hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	return enc
}

func TestCredentialStore_UpsertAndGet(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	enc := testEncryptor(t)
	store := NewCredentialStore(db, enc)

	cred := &Credential{
		GroupJID: "discord:123",
		Provider: "openai",
		APIKey:   "sk-test-key",
	}

	mock.ExpectExec("REPLACE INTO credentials").
		WithArgs("discord:123", "openai", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.UpsertCredential(context.Background(), cred); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestCredentialStore_Delete(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	enc := testEncryptor(t)
	store := NewCredentialStore(db, enc)

	mock.ExpectExec("DELETE FROM credentials WHERE group_jid").
		WithArgs("discord:123").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.DeleteCredential(context.Background(), "discord:123"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/credproxy/ -run TestCredentialStore -v -short`
Expected: FAIL — `Credential`, `NewCredentialStore` undefined

- [ ] **Step 4: Implement credential store**

```go
// internal/credproxy/credential_store.go
package credproxy

import (
	"context"
	"database/sql"
	"fmt"
)

// Credential represents per-group API credentials.
type Credential struct {
	GroupJID   string
	Provider   string
	APIKey     string
	OAuthToken string
}

// CredentialStore manages per-group credentials in MySQL with at-rest encryption.
type CredentialStore struct {
	db  *sql.DB
	enc *Encryptor
}

// NewCredentialStore creates a credential store backed by MySQL.
func NewCredentialStore(db *sql.DB, enc *Encryptor) *CredentialStore {
	return &CredentialStore{db: db, enc: enc}
}

// GetCredential retrieves and decrypts a credential for a group.
func (s *CredentialStore) GetCredential(ctx context.Context, groupJID string) (*Credential, error) {
	var provider, apiKeyEnc string
	var oauthTokenEnc sql.NullString

	err := s.db.QueryRowContext(ctx,
		"SELECT provider, api_key_encrypted, oauth_token_encrypted FROM credentials WHERE group_jid = ?",
		groupJID,
	).Scan(&provider, &apiKeyEnc, &oauthTokenEnc)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get credential: %w", err)
	}

	apiKey, err := s.enc.Decrypt(apiKeyEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt api key: %w", err)
	}

	cred := &Credential{
		GroupJID: groupJID,
		Provider: provider,
		APIKey:   apiKey,
	}

	if oauthTokenEnc.Valid && oauthTokenEnc.String != "" {
		token, err := s.enc.Decrypt(oauthTokenEnc.String)
		if err != nil {
			return nil, fmt.Errorf("decrypt oauth token: %w", err)
		}
		cred.OAuthToken = token
	}

	return cred, nil
}

// UpsertCredential encrypts and stores a credential for a group.
func (s *CredentialStore) UpsertCredential(ctx context.Context, cred *Credential) error {
	apiKeyEnc, err := s.enc.Encrypt(cred.APIKey)
	if err != nil {
		return fmt.Errorf("encrypt api key: %w", err)
	}

	var oauthTokenEnc sql.NullString
	if cred.OAuthToken != "" {
		enc, err := s.enc.Encrypt(cred.OAuthToken)
		if err != nil {
			return fmt.Errorf("encrypt oauth token: %w", err)
		}
		oauthTokenEnc = sql.NullString{String: enc, Valid: true}
	}

	_, err = s.db.ExecContext(ctx,
		"REPLACE INTO credentials (group_jid, provider, api_key_encrypted, oauth_token_encrypted) VALUES (?, ?, ?, ?)",
		cred.GroupJID, cred.Provider, apiKeyEnc, oauthTokenEnc,
	)
	if err != nil {
		return fmt.Errorf("upsert credential: %w", err)
	}
	return nil
}

// DeleteCredential removes a credential for a group.
func (s *CredentialStore) DeleteCredential(ctx context.Context, groupJID string) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM credentials WHERE group_jid = ?",
		groupJID,
	)
	if err != nil {
		return fmt.Errorf("delete credential: %w", err)
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/credproxy/ -run TestCredentialStore -v -short`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add migrations/20260331000001_credentials.up.sql migrations/20260331000001_credentials.down.sql internal/credproxy/credential_store.go internal/credproxy/credential_store_test.go
git commit -m "feat: add credential store with encrypted per-group API keys"
```

---

### Task 5: Update Config for Multi-Provider

**Files:**
- Modify: `internal/config/config.go:46-63`

- [ ] **Step 1: Write failing test**

```go
// Add to internal/config/config_test.go (create if needed)
package config

import "testing"

func TestValidate_AllowsOpenAIOnlyCredentials(t *testing.T) {
	cfg := validBaseConfig()
	cfg.Proxy.AnthropicAPIKey = ""
	cfg.Proxy.AnthropicOAuthToken = ""
	cfg.Proxy.OpenAIAPIKey = "sk-test"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("should allow OpenAI-only config: %v", err)
	}
}

func TestValidate_RejectsNoCredentials(t *testing.T) {
	cfg := validBaseConfig()
	cfg.Proxy.AnthropicAPIKey = ""
	cfg.Proxy.AnthropicOAuthToken = ""
	cfg.Proxy.OpenAIAPIKey = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("should reject config with no provider credentials")
	}
}

// validBaseConfig returns a minimal valid Config for testing.
func validBaseConfig() Config {
	return Config{
		Server: ServerConfig{
			GRPCInsecure:     true,
			GRPCAllowedCIDRs: "10.0.0.0/8",
		},
		Proxy: ProxyConfig{
			AnthropicAPIKey: "test-key",
		},
		Queue: QueueConfig{
			MaxConcurrent: 5,
		},
		Channels: ChannelsConfig{
			Timezone: "UTC",
		},
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestValidate_Allows -v`
Expected: FAIL — `AnthropicAPIKey` field does not exist on ProxyConfig

- [ ] **Step 3: Update ProxyConfig and K8sConfig**

In `internal/config/config.go`, replace `ProxyConfig` and update `K8sConfig`:

```go
type K8sConfig struct {
	Namespace              string        `envconfig:"K8S_NAMESPACE" default:"kraclaw"`
	AgentImage             string        `envconfig:"AGENT_IMAGE"`                      // Legacy: default agent image
	AgentImageAnthropic    string        `envconfig:"AGENT_IMAGE_ANTHROPIC"`            // Anthropic agent image
	AgentImageOpenAI       string        `envconfig:"AGENT_IMAGE_OPENAI"`               // OpenAI agent image
	InCluster              bool          `envconfig:"K8S_IN_CLUSTER" default:"true"`
	SessionsPVC            string        `envconfig:"K8S_SESSIONS_PVC" default:"kraclaw-sessions"`
	GroupsPVC              string        `envconfig:"K8S_GROUPS_PVC" default:"kraclaw-groups"`
	DataPVC                string        `envconfig:"K8S_DATA_PVC" default:"kraclaw-data"`
	SandboxStartupTimeout  time.Duration `envconfig:"SANDBOX_STARTUP_TIMEOUT" default:"5m"`
	SandboxProxyURL        string        `envconfig:"SANDBOX_PROXY_URL" default:"http://kraclaw-credproxy:3001"`
}

type ProxyConfig struct {
	Addr string `envconfig:"PROXY_ADDR" default:":3001"`

	// Anthropic (platform-level fallback)
	AnthropicUpstreamURL string `envconfig:"ANTHROPIC_UPSTREAM_URL" default:"https://api.anthropic.com"`
	AnthropicAPIKey      string `envconfig:"ANTHROPIC_API_KEY"`
	AnthropicOAuthToken  string `envconfig:"ANTHROPIC_OAUTH_TOKEN"`
	AnthropicAPIVersion  string `envconfig:"ANTHROPIC_VERSION" default:"2023-06-01"`

	// OpenAI (platform-level fallback)
	OpenAIUpstreamURL string `envconfig:"OPENAI_UPSTREAM_URL" default:"https://api.openai.com"`
	OpenAIAPIKey      string `envconfig:"OPENAI_API_KEY"`

	// Encryption key for credential store (hex-encoded 32 bytes)
	CredentialEncryptionKey string `envconfig:"CREDENTIAL_ENCRYPTION_KEY"`
}
```

Update `Validate()` — replace the Anthropic-only check:

```go
func (c *Config) Validate() error {
	if c.Proxy.AnthropicAPIKey == "" && c.Proxy.AnthropicOAuthToken == "" && c.Proxy.OpenAIAPIKey == "" {
		return fmt.Errorf("at least one provider credential must be set (ANTHROPIC_API_KEY, ANTHROPIC_OAUTH_TOKEN, or OPENAI_API_KEY)")
	}
	// ... rest unchanged ...
}
```

- [ ] **Step 4: Fix all references to old field names**

Search for all references to the old `ProxyConfig` field names and update them:

- `cfg.Proxy.APIKey` → `cfg.Proxy.AnthropicAPIKey` (in orchestrator.go line 118)
- `cfg.Proxy.OAuthToken` → `cfg.Proxy.AnthropicOAuthToken` (in orchestrator.go line 118)
- `cfg.Proxy.UpstreamURL` → `cfg.Proxy.AnthropicUpstreamURL` (in credproxy/proxy.go)
- `cfg.APIKey` → `cfg.AnthropicAPIKey` (in credproxy/proxy.go)
- `cfg.OAuthToken` → `cfg.AnthropicOAuthToken` (in credproxy/proxy.go)
- `cfg.APIVersion` → `cfg.AnthropicAPIVersion` (in credproxy/proxy.go)

Run `grep -rn "Proxy\.APIKey\|Proxy\.OAuthToken\|Proxy\.UpstreamURL\|Proxy\.APIVersion\|cfg\.APIKey\|cfg\.OAuthToken\|cfg\.UpstreamURL\|cfg\.APIVersion" internal/` to find all occurrences.

Update each one. The credproxy `New()` function in `proxy.go` should be updated to accept the Anthropic-specific fields:

```go
func New(cfg config.ProxyConfig) (*Proxy, error) {
	upstreamURL := cfg.AnthropicUpstreamURL
	if upstreamURL == "" {
		upstreamURL = "https://api.anthropic.com"
	}
	upstream, err := url.Parse(upstreamURL)
	if err != nil {
		return nil, fmt.Errorf("credproxy: invalid upstream URL: %w", err)
	}
	if cfg.AnthropicAPIKey == "" && cfg.AnthropicOAuthToken == "" {
		// Not an error anymore — proxy may only serve OpenAI groups
		slog.Info("credproxy: no Anthropic credentials configured, Anthropic requests will use per-group credentials")
	}
	if cfg.AnthropicAPIKey != "" && cfg.AnthropicOAuthToken != "" {
		slog.Warn("both ANTHROPIC_API_KEY and ANTHROPIC_OAUTH_TOKEN set, API key takes precedence")
	}
	return &Proxy{
		upstream:        upstream,
		allowedHost:     upstream.Host,
		apiKey:          cfg.AnthropicAPIKey,
		oauthToken:      cfg.AnthropicOAuthToken,
		addr:            cfg.Addr,
		log:             slog.Default().With("component", "credproxy"),
		cachedAPIKeyTTL: 15 * time.Minute,
	}, nil
}
```

- [ ] **Step 5: Run all tests to verify nothing is broken**

Run: `go test -short ./internal/config/ ./internal/credproxy/ ./internal/orchestrator/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go internal/credproxy/proxy.go internal/orchestrator/orchestrator.go
git commit -m "feat: update config for multi-provider credentials and per-provider agent images"
```

---

### Task 6: Multi-Provider Credential Proxy

**Files:**
- Modify: `internal/credproxy/proxy.go`

This is the biggest change. The proxy needs to route requests to different upstreams based on the `X-Kraclaw-Group` header, looking up per-group credentials.

- [ ] **Step 1: Write failing tests for multi-provider routing**

```go
// Add to internal/credproxy/proxy_test.go (create if needed)
package credproxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/johanssonvincent/kraclaw/internal/config"
)

func TestProxy_InjectsOpenAIBearerToken(t *testing.T) {
	// Upstream mock that verifies the Authorization header.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer sk-openai-test" {
			t.Errorf("expected Bearer sk-openai-test, got %q", auth)
		}
		// Should NOT have X-Api-Key.
		if r.Header.Get("X-Api-Key") != "" {
			t.Error("X-Api-Key should not be set for OpenAI requests")
		}
		// Kraclaw headers should be stripped.
		if r.Header.Get("X-Kraclaw-Group") != "" {
			t.Error("X-Kraclaw-Group should be stripped before forwarding")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	enc, err := NewEncryptor(hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}

	resolver := &staticCredentialResolver{
		cred: &resolvedCredential{
			Provider:    "openai",
			APIKey:      "sk-openai-test",
			UpstreamURL: upstream.URL,
		},
	}

	proxy, err := NewMultiProviderProxy(config.ProxyConfig{
		Addr: ":0",
	}, resolver)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("X-Kraclaw-Group", "discord:123")
	w := httptest.NewRecorder()
	proxy.handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// staticCredentialResolver is a test helper that returns a fixed credential.
type staticCredentialResolver struct {
	cred *resolvedCredential
}

func (r *staticCredentialResolver) Resolve(ctx context.Context, groupJID string) (*resolvedCredential, error) {
	return r.cred, nil
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/credproxy/ -run TestProxy_InjectsOpenAI -v -short`
Expected: FAIL — `NewMultiProviderProxy`, `resolvedCredential`, `CredentialResolver` undefined

- [ ] **Step 3: Implement multi-provider proxy**

Refactor `internal/credproxy/proxy.go` to add multi-provider support. The key changes:

1. Add a `CredentialResolver` interface that the proxy uses to look up credentials per request.
2. Add a `resolvedCredential` struct containing provider, API key, and upstream URL.
3. In the `Director` function, read `X-Kraclaw-Group` header, resolve credentials, rewrite upstream + auth headers.
4. Fall back to platform-level Anthropic credentials when no `X-Kraclaw-Group` header is present (backwards compatibility with existing Node.js agent).

Add to `proxy.go`:

```go
// resolvedCredential contains the provider-specific routing info for a single request.
type resolvedCredential struct {
	Provider    string
	APIKey      string
	OAuthToken  string
	UpstreamURL string
}

// CredentialResolver looks up credentials for a group.
type CredentialResolver interface {
	Resolve(ctx context.Context, groupJID string) (*resolvedCredential, error)
}
```

Create a `defaultCredentialResolver` that wraps the `CredentialStore` + platform fallback config:

```go
type defaultCredentialResolver struct {
	credStore *CredentialStore
	cfg       config.ProxyConfig
}

func (r *defaultCredentialResolver) Resolve(ctx context.Context, groupJID string) (*resolvedCredential, error) {
	// Try per-group credential first.
	if r.credStore != nil && groupJID != "" {
		cred, err := r.credStore.GetCredential(ctx, groupJID)
		if err != nil {
			return nil, fmt.Errorf("resolve credential: %w", err)
		}
		if cred != nil {
			rc := &resolvedCredential{
				Provider: cred.Provider,
				APIKey:   cred.APIKey,
				OAuthToken: cred.OAuthToken,
			}
			switch cred.Provider {
			case "openai":
				rc.UpstreamURL = r.cfg.OpenAIUpstreamURL
			default:
				rc.UpstreamURL = r.cfg.AnthropicUpstreamURL
			}
			return rc, nil
		}
	}

	// Platform-level fallback: Anthropic.
	if r.cfg.AnthropicAPIKey != "" || r.cfg.AnthropicOAuthToken != "" {
		return &resolvedCredential{
			Provider:    "anthropic",
			APIKey:      r.cfg.AnthropicAPIKey,
			OAuthToken:  r.cfg.AnthropicOAuthToken,
			UpstreamURL: r.cfg.AnthropicUpstreamURL,
		}, nil
	}

	return nil, fmt.Errorf("no credentials found for group %q and no platform fallback configured", groupJID)
}
```

Add `NewMultiProviderProxy` constructor and update the `Director` to use the resolver. The `Director` reads `X-Kraclaw-Group`, resolves credentials, rewrites the upstream URL, and injects the correct auth header based on provider:

- `openai`: `Authorization: Bearer <key>`, strip `X-Api-Key`
- `anthropic`: `X-Api-Key: <key>` or `Authorization: Bearer <oauth_token>` (existing behavior)

Strip `X-Kraclaw-Group` and `X-Kraclaw-Provider` before forwarding.

Keep the existing `New()` constructor working for backwards compatibility — it creates a proxy with a nil resolver that uses only platform Anthropic credentials (existing behavior).

- [ ] **Step 4: Run all credproxy tests**

Run: `go test ./internal/credproxy/ -v -short`
Expected: PASS — both old and new tests

- [ ] **Step 5: Commit**

```bash
git add internal/credproxy/proxy.go internal/credproxy/proxy_test.go
git commit -m "feat: multi-provider credential proxy with per-group routing"
```

---

### Task 7: Provider-Aware Sandbox Controller

**Files:**
- Modify: `internal/sandbox/controller.go`

- [ ] **Step 1: Write failing test**

```go
// Add to internal/sandbox/controller_test.go
package sandbox

import (
	"testing"

	"github.com/johanssonvincent/kraclaw/internal/store"
)

func TestAgentImageForProvider_Anthropic(t *testing.T) {
	c := &Controller{
		agentImage: "legacy-image:latest",
		agentImages: map[string]string{
			"anthropic": "ghcr.io/johanssonvincent/kraclaw-agent-anthropic:latest",
			"openai":    "ghcr.io/johanssonvincent/kraclaw-agent-openai:latest",
		},
	}
	img := c.agentImageForProvider("anthropic")
	if img != "ghcr.io/johanssonvincent/kraclaw-agent-anthropic:latest" {
		t.Fatalf("expected anthropic image, got %q", img)
	}
}

func TestAgentImageForProvider_OpenAI(t *testing.T) {
	c := &Controller{
		agentImages: map[string]string{
			"openai": "ghcr.io/johanssonvincent/kraclaw-agent-openai:latest",
		},
	}
	img := c.agentImageForProvider("openai")
	if img != "ghcr.io/johanssonvincent/kraclaw-agent-openai:latest" {
		t.Fatalf("expected openai image, got %q", img)
	}
}

func TestAgentImageForProvider_FallbackToLegacy(t *testing.T) {
	c := &Controller{
		agentImage:  "legacy-image:latest",
		agentImages: map[string]string{},
	}
	img := c.agentImageForProvider("anthropic")
	if img != "legacy-image:latest" {
		t.Fatalf("expected legacy fallback, got %q", img)
	}
}

func TestAgentImageForProvider_EmptyProviderUsesLegacy(t *testing.T) {
	c := &Controller{
		agentImage: "legacy-image:latest",
		agentImages: map[string]string{
			"anthropic": "new-image:latest",
		},
	}
	img := c.agentImageForProvider("")
	if img != "legacy-image:latest" {
		t.Fatalf("expected legacy fallback for empty provider, got %q", img)
	}
}

func TestBuildSandbox_OpenAIEnvVars(t *testing.T) {
	c := &Controller{
		namespace: "test",
		redisURL:  "redis://localhost:6379",
		proxyURL:  "http://proxy:3001",
		agentImages: map[string]string{
			"openai": "ghcr.io/johanssonvincent/kraclaw-agent-openai:latest",
		},
	}
	cfg := SandboxConfig{
		GroupFolder: "testgroup",
		GroupJID:    "discord:123",
		ContainerConfig: &store.ContainerConfig{
			Provider: "openai",
			Model:    "gpt-5.4",
		},
	}
	sb := c.buildSandbox("test-sandbox", cfg)
	envs := sb.Spec.PodTemplate.Spec.Containers[0].Env

	foundProvider := false
	foundModel := false
	for _, e := range envs {
		if e.Name == "KRACLAW_PROVIDER" && e.Value == "openai" {
			foundProvider = true
		}
		if e.Name == "OPENAI_MODEL" && e.Value == "gpt-5.4" {
			foundModel = true
		}
	}
	if !foundProvider {
		t.Error("missing KRACLAW_PROVIDER=openai env var")
	}
	if !foundModel {
		t.Error("missing OPENAI_MODEL=gpt-5.4 env var")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sandbox/ -run TestAgentImage -v -short`
Expected: FAIL — `agentImages` field and `agentImageForProvider` method not found

- [ ] **Step 3: Update sandbox controller**

Add `agentImages` field to `Controller`:

```go
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
```

Update `New()` to accept `agentImages`:

```go
func New(clientset kubernetes.Interface, ctrlClient client.WithWatch, config *rest.Config, namespace, agentImage string, agentImages map[string]string, redisURL, proxyURL string) (*Controller, error) {
	if clientset == nil {
		return nil, fmt.Errorf("sandbox: kubernetes clientset is required")
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
```

Add `agentImageForProvider`:

```go
func (c *Controller) agentImageForProvider(provider string) string {
	if provider != "" {
		if img, ok := c.agentImages[provider]; ok && img != "" {
			return img
		}
	}
	return c.agentImage
}
```

Update `buildSandbox` to use provider-aware image and env vars:

```go
// In buildSandbox, replace the hardcoded image and env vars:
provider := ""
if cfg.ContainerConfig != nil {
	provider = cfg.ContainerConfig.Provider
}
image := c.agentImageForProvider(provider)

// Build env vars based on provider
envVars := []corev1.EnvVar{
	groupFolderEnv,
	{Name: "REDIS_URL", Value: c.redisURL},
	{Name: "KRACLAW_PROXY_URL", Value: c.proxyURL},
	{Name: "KRACLAW_PROVIDER", Value: provider},
	{Name: "KRACLAW_GROUP", Value: cfg.GroupJID},
	{Name: "HOME", Value: "/home/nonroot"},
}

switch provider {
case "openai":
	model := ""
	if cfg.ContainerConfig != nil {
		model = cfg.ContainerConfig.Model
	}
	envVars = append(envVars, corev1.EnvVar{Name: "OPENAI_MODEL", Value: model})
default:
	// Anthropic (legacy + explicit)
	envVars = append(envVars,
		corev1.EnvVar{Name: "ANTHROPIC_BASE_URL", Value: c.proxyURL},
		corev1.EnvVar{Name: "CLAUDE_CODE_OAUTH_TOKEN", Value: "placeholder"},
		corev1.EnvVar{Name: "HOME", Value: "/home/node"},
	)
}
```

Remove the hardcoded `Command` for the agent container — let each image use its own `ENTRYPOINT`. For backwards compatibility, keep `Command` only when provider is empty or anthropic AND the legacy image is used:

```go
container := corev1.Container{
	Name:       "agent",
	Image:      image,
	WorkingDir: "/workspace",
	Env:        envVars,
	// ... volume mounts ...
}

// Legacy Node.js agent needs explicit command.
if (provider == "" || provider == "anthropic") && image == c.agentImage {
	container.Command = []string{"node", "/app/dist/index.js", "--group", "$(GROUP_FOLDER)"}
	// Override HOME back to /home/node for Node.js agent.
	for i, e := range container.Env {
		if e.Name == "HOME" {
			container.Env[i].Value = "/home/node"
		}
	}
}
```

- [ ] **Step 4: Update callers of sandbox.New()**

Find all callers (likely `cmd/kraclaw/main.go` or orchestrator wiring) and pass `agentImages`:

```go
agentImages := map[string]string{
	"anthropic": cfg.K8s.AgentImageAnthropic,
	"openai":    cfg.K8s.AgentImageOpenAI,
}
ctrl, err := sandbox.New(clientset, ctrlClient, restCfg, cfg.K8s.Namespace, cfg.K8s.AgentImage, agentImages, cfg.Redis.URL, cfg.K8s.SandboxProxyURL)
```

- [ ] **Step 5: Run all sandbox tests**

Run: `go test ./internal/sandbox/ -v -short`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/sandbox/controller.go internal/sandbox/controller_test.go cmd/kraclaw/main.go
git commit -m "feat: provider-aware sandbox controller with per-provider agent images"
```

---

### Task 8: Update Orchestrator — Provider Validation + Model Commands

**Files:**
- Modify: `internal/orchestrator/orchestrator.go`
- Modify: `internal/orchestrator/commands.go`
- Delete: `internal/orchestrator/oauth_models.go`

- [ ] **Step 1: Add provider registry to Orchestrator**

Add `providers *provider.Registry` field to the `Orchestrator` struct. Initialize it in `New()`:

```go
import "github.com/johanssonvincent/kraclaw/internal/provider"

// In Orchestrator struct:
providers *provider.Registry

// In New():
providers: provider.NewRegistry(),
```

- [ ] **Step 2: Update /models command to be provider-aware**

In `commands.go`, update `handleModelsCommand` to show models for the group's provider:

```go
func (o *Orchestrator) handleModelsCommand(ctx context.Context, chatJID string) {
	group, err := o.store.GetGroup(ctx, chatJID)
	if err != nil || group == nil {
		o.sendSystemMessage(ctx, chatJID, "Unable to fetch group for this chat.")
		return
	}

	providerID := "anthropic"
	if group.ContainerConfig != nil && group.ContainerConfig.Provider != "" {
		providerID = group.ContainerConfig.Provider
	}

	models := o.providers.Models(providerID)
	currentModel := ""
	if group.ContainerConfig != nil {
		currentModel = group.ContainerConfig.Model
	}

	var b strings.Builder
	p, _ := o.providers.Get(providerID)
	b.WriteString(fmt.Sprintf("Models (%s):\n", p.DisplayName))

	for _, m := range models {
		name := m.ID
		if m.DisplayName != "" {
			name = fmt.Sprintf("%s (%s)", m.ID, m.DisplayName)
		}
		marker := ""
		if m.ID == currentModel {
			marker = " (current)"
		}
		b.WriteString("- ")
		b.WriteString(name)
		b.WriteString(marker)
		b.WriteString("\n")
	}

	if currentModel == "" {
		b.WriteString(fmt.Sprintf("Current: %s (default)", p.DefaultModel))
	}

	o.sendSystemMessage(ctx, chatJID, strings.TrimRight(b.String(), "\n"))
}
```

- [ ] **Step 3: Update /model command to validate against provider**

In `commands.go`, update `handleModelCommand`:

```go
func (o *Orchestrator) handleModelCommand(ctx context.Context, chatJID string, requested string) {
	currentModel, currentLabel, err := o.currentModelForChat(ctx, chatJID)
	if err != nil {
		o.sendSystemMessage(ctx, chatJID, "Unable to fetch current model for this chat.")
		return
	}

	if requested == "" {
		if currentLabel == "" {
			currentLabel = "default"
		}
		o.sendSystemMessage(ctx, chatJID, fmt.Sprintf("Current model: %s", currentLabel))
		return
	}

	// Get group's provider.
	group, err := o.store.GetGroup(ctx, chatJID)
	if err != nil || group == nil {
		o.sendSystemMessage(ctx, chatJID, "Unable to fetch group.")
		return
	}
	providerID := "anthropic"
	if group.ContainerConfig != nil && group.ContainerConfig.Provider != "" {
		providerID = group.ContainerConfig.Provider
	}

	if err := o.providers.ValidateModel(providerID, requested); err != nil {
		o.sendSystemMessage(ctx, chatJID, fmt.Sprintf("Unknown model %q for provider %s. Use /models to list available models.", requested, providerID))
		return
	}

	if requested == currentModel {
		o.sendSystemMessage(ctx, chatJID, fmt.Sprintf("Model already set to %s.", requested))
		return
	}

	if err := o.updateGroupModel(ctx, chatJID, requested); err != nil {
		o.sendSystemMessage(ctx, chatJID, "Failed to update the model.")
		return
	}

	if err := o.sendModelUpdateToActive(ctx, chatJID, requested); err != nil {
		o.log.Error("failed to notify active sandbox", "chat_jid", chatJID, "error", err)
	}

	o.sendSystemMessage(ctx, chatJID, fmt.Sprintf("Model set to %s.", requested))
}
```

- [ ] **Step 4: Remove oauth_models.go and model_cache dependency for model listing**

Delete `internal/orchestrator/oauth_models.go`. Remove the `models *modelCache` field from `Orchestrator` and all references to it. The provider registry replaces this functionality.

Note: The model cache (`newModelCache`) in `orchestrator.go` line 118 was used for listing models from the Anthropic API. Remove this and replace with the registry-based approach.

- [ ] **Step 5: Update RegisterGroup to validate provider+model**

In `internal/server/api_services.go`, after parsing `ContainerConfig`, validate provider+model:

```go
// After parsing ContainerConfig in RegisterGroup:
if group.ContainerConfig != nil && group.ContainerConfig.Provider != "" {
	reg := provider.NewRegistry()
	if err := reg.ValidateModel(group.ContainerConfig.Provider, group.ContainerConfig.Model); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid provider/model: %v", err)
	}
}
```

Add import for `"github.com/johanssonvincent/kraclaw/internal/provider"`.

- [ ] **Step 6: Run all tests**

Run: `go test -short ./internal/orchestrator/ ./internal/server/ -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/orchestrator/orchestrator.go internal/orchestrator/commands.go internal/server/api_services.go
git rm internal/orchestrator/oauth_models.go
git commit -m "feat: integrate provider registry into orchestrator and model commands"
```

---

### Task 9: Shared Agent IPC Library

**Files:**
- Create: `pkg/agent/ipc.go`
- Create: `pkg/agent/ipc_test.go`
- Create: `pkg/agent/agent.go`

- [ ] **Step 1: Write failing tests for agent IPC client**

```go
// pkg/agent/ipc_test.go
package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestIPCClient_SendOutput(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	client := NewIPCClient(rdb, "testgroup")

	msg := &OutboundMessage{
		Type: "message",
		Text: "Hello from agent",
	}

	if err := client.SendOutput(context.Background(), msg); err != nil {
		t.Fatalf("send output: %v", err)
	}

	// Verify message was written to the output stream.
	entries, err := rdb.XRange(context.Background(), "kraclaw:ipc:testgroup:output", "-", "+").Result()
	if err != nil {
		t.Fatalf("xrange: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
}

func TestIPCClient_ReadInput(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	client := NewIPCClient(rdb, "testgroup")

	// Write a message to the input stream.
	payload, _ := json.Marshal(map[string]string{"messages": "Hello"})
	data, _ := json.Marshal(map[string]interface{}{
		"group":   "testgroup",
		"type":    "message",
		"payload": json.RawMessage(payload),
	})
	rdb.XAdd(context.Background(), &redis.XAddArgs{
		Stream: "kraclaw:ipc:testgroup:input",
		Values: map[string]interface{}{"data": string(data)},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch, err := client.ReadInput(ctx)
	if err != nil {
		t.Fatalf("read input: %v", err)
	}

	select {
	case msg := <-ch:
		if msg.Type != "message" {
			t.Fatalf("expected type message, got %s", msg.Type)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for message")
	}
}

func TestIPCClient_CheckCloseSignal(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	client := NewIPCClient(rdb, "testgroup")

	closed, err := client.CheckCloseSignal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if closed {
		t.Fatal("expected not closed")
	}

	rdb.Set(context.Background(), "kraclaw:ipc:testgroup:close", "1", 60*time.Second)

	closed, err = client.CheckCloseSignal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Fatal("expected closed")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/agent/ -v -short`
Expected: FAIL — package does not exist

- [ ] **Step 3: Implement agent IPC client**

```go
// pkg/agent/ipc.go
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// InboundMessage is a message received from the server.
type InboundMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// OutboundMessage is a message sent from the agent to the server.
type OutboundMessage struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// IPCClient handles Redis Streams communication for a Go agent.
type IPCClient struct {
	rdb   redis.Cmdable
	group string
}

// NewIPCClient creates an IPC client for a specific group.
func NewIPCClient(rdb redis.Cmdable, group string) *IPCClient {
	return &IPCClient{rdb: rdb, group: group}
}

func (c *IPCClient) outputKey() string { return fmt.Sprintf("kraclaw:ipc:%s:output", c.group) }
func (c *IPCClient) inputKey() string  { return fmt.Sprintf("kraclaw:ipc:%s:input", c.group) }
func (c *IPCClient) closeKey() string  { return fmt.Sprintf("kraclaw:ipc:%s:close", c.group) }

// SendOutput publishes a message to the output stream (agent -> server).
func (c *IPCClient) SendOutput(ctx context.Context, msg *OutboundMessage) error {
	ipcMsg := map[string]interface{}{
		"group": c.group,
		"type":  msg.Type,
	}

	if msg.Text != "" {
		payload, err := json.Marshal(map[string]string{"text": msg.Text})
		if err != nil {
			return fmt.Errorf("marshal payload: %w", err)
		}
		ipcMsg["payload"] = json.RawMessage(payload)
	}

	data, err := json.Marshal(ipcMsg)
	if err != nil {
		return fmt.Errorf("marshal ipc message: %w", err)
	}

	if err := c.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: c.outputKey(),
		Values: map[string]interface{}{"data": string(data)},
	}).Err(); err != nil {
		return fmt.Errorf("xadd output: %w", err)
	}

	// Publish notification.
	_ = c.rdb.Publish(ctx, "kraclaw:ipc:notify", c.group).Err()
	return nil
}

// ReadInput returns a channel that receives messages from the input stream.
func (c *IPCClient) ReadInput(ctx context.Context) (<-chan *InboundMessage, error) {
	stream := c.inputKey()
	consumerGroup := "agent"
	consumer := "agent-0"

	// Create consumer group.
	err := c.rdb.XGroupCreateMkStream(ctx, stream, consumerGroup, "0").Err()
	if err != nil && !isGroupExistsErr(err) {
		return nil, fmt.Errorf("create consumer group: %w", err)
	}

	ch := make(chan *InboundMessage, 64)
	go func() {
		defer close(ch)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			results, err := c.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
				Group:    consumerGroup,
				Consumer: consumer,
				Streams:  []string{stream, ">"},
				Count:    10,
				Block:    500 * time.Millisecond,
			}).Result()

			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return
				}
				if errors.Is(err, redis.Nil) {
					continue
				}
				continue
			}

			for _, s := range results {
				for _, entry := range s.Messages {
					raw, ok := entry.Values["data"].(string)
					if !ok {
						continue
					}
					var ipcMsg struct {
						Type    string          `json:"type"`
						Payload json.RawMessage `json:"payload"`
					}
					if err := json.Unmarshal([]byte(raw), &ipcMsg); err != nil {
						continue
					}

					msg := &InboundMessage{
						Type:    ipcMsg.Type,
						Payload: ipcMsg.Payload,
					}

					select {
					case ch <- msg:
					case <-ctx.Done():
						return
					}

					_ = c.rdb.XAck(ctx, stream, consumerGroup, entry.ID).Err()
				}
			}
		}
	}()

	return ch, nil
}

// CheckCloseSignal checks whether the server has set the close signal.
func (c *IPCClient) CheckCloseSignal(ctx context.Context) (bool, error) {
	n, err := c.rdb.Exists(ctx, c.closeKey()).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func isGroupExistsErr(err error) bool {
	return err != nil && err.Error() == "BUSYGROUP Consumer Group name already exists"
}
```

```go
// pkg/agent/agent.go
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/redis/go-redis/v9"
)

// Config holds common agent configuration from environment.
type Config struct {
	RedisURL  string
	Group     string
	GroupJID  string
	ProxyURL  string
	Provider  string
}

// LoadConfig reads agent config from environment variables.
func LoadConfig() (*Config, error) {
	cfg := &Config{
		RedisURL: os.Getenv("REDIS_URL"),
		Group:    os.Getenv("KRACLAW_GROUP_FOLDER"),
		GroupJID: os.Getenv("KRACLAW_GROUP"),
		ProxyURL: os.Getenv("KRACLAW_PROXY_URL"),
		Provider: os.Getenv("KRACLAW_PROVIDER"),
	}
	if cfg.RedisURL == "" {
		cfg.RedisURL = "redis://localhost:6379"
	}
	if cfg.Group == "" {
		// Fallback to GROUP_FOLDER for backwards compat.
		cfg.Group = os.Getenv("GROUP_FOLDER")
	}
	if cfg.Group == "" {
		return nil, fmt.Errorf("KRACLAW_GROUP_FOLDER or GROUP_FOLDER is required")
	}
	return cfg, nil
}

// ConnectRedis creates a Redis client from a URL.
func ConnectRedis(ctx context.Context, redisURL string) (redis.Cmdable, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	rdb := redis.NewClient(opts)
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return rdb, nil
}

// WaitForShutdown blocks until SIGTERM or SIGINT.
func WaitForShutdown(ctx context.Context) context.Context {
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	_ = cancel // caller uses ctx.Done()
	return ctx
}

// Run is the main agent lifecycle: connect, process, shutdown.
// The handler function is called with the IPC client and should block until done.
func Run(handler func(ctx context.Context, ipc *IPCClient, log *slog.Logger) error) error {
	log := slog.Default()

	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log.Info("agent starting", "group", cfg.Group, "provider", cfg.Provider)

	ctx := WaitForShutdown(context.Background())

	rdb, err := ConnectRedis(ctx, cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("connect redis: %w", err)
	}

	ipcClient := NewIPCClient(rdb, cfg.Group)

	if err := handler(ctx, ipcClient, log); err != nil {
		return fmt.Errorf("agent handler: %w", err)
	}

	log.Info("agent stopped")
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/agent/ -v -short`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/agent/ipc.go pkg/agent/ipc_test.go pkg/agent/agent.go
git commit -m "feat: add shared Go agent IPC library"
```

---

### Task 10: OpenAI Agent Binary

**Files:**
- Create: `cmd/kraclaw-agent-openai/main.go`
- Modify: `go.mod` (add openai-go dependency)

- [ ] **Step 1: Add OpenAI Go SDK dependency**

Run: `go get github.com/openai/openai-go`

- [ ] **Step 2: Implement OpenAI agent**

```go
// cmd/kraclaw-agent-openai/main.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/johanssonvincent/kraclaw/pkg/agent"
)

func main() {
	if err := agent.Run(runOpenAI); err != nil {
		slog.Error("agent failed", "error", err)
		os.Exit(1)
	}
}

func runOpenAI(ctx context.Context, ipc *agent.IPCClient, log *slog.Logger) error {
	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		model = "gpt-5.4"
	}
	proxyURL := os.Getenv("KRACLAW_PROXY_URL")
	groupJID := os.Getenv("KRACLAW_GROUP")

	// Create OpenAI client pointing at the credential proxy.
	opts := []option.RequestOption{
		option.WithAPIKey("placeholder"), // Proxy injects real key.
	}
	if proxyURL != "" {
		opts = append(opts, option.WithBaseURL(proxyURL+"/v1"))
	}
	// Add group header so proxy knows which credentials to use.
	opts = append(opts, option.WithHeader("X-Kraclaw-Group", groupJID))

	client := openai.NewClient(opts...)

	log.Info("openai agent ready", "model", model, "proxy", proxyURL)

	// Conversation history for the session.
	var history []openai.ChatCompletionMessageParamUnion

	// Read inbound messages.
	inputCh, err := ipc.ReadInput(ctx)
	if err != nil {
		return fmt.Errorf("read input: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-inputCh:
			if !ok {
				return nil
			}

			switch msg.Type {
			case "message":
				text, err := extractMessageText(msg.Payload)
				if err != nil {
					log.Warn("failed to extract message text", "error", err)
					continue
				}

				history = append(history, openai.UserMessage(text))

				// Call OpenAI with streaming.
				stream := client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
					Model:    model,
					Messages: history,
				})

				var fullResponse string
				for stream.Next() {
					chunk := stream.Current()
					for _, choice := range chunk.Choices {
						if choice.Delta.Content != "" {
							fullResponse += choice.Delta.Content
						}
					}
				}
				if err := stream.Err(); err != nil {
					log.Error("openai stream error", "error", err)
					if sendErr := ipc.SendOutput(ctx, &agent.OutboundMessage{
						Type: "message",
						Text: fmt.Sprintf("Error: %v", err),
					}); sendErr != nil {
						log.Error("failed to send error message", "error", sendErr)
					}
					continue
				}

				history = append(history, openai.AssistantMessage(fullResponse))

				if err := ipc.SendOutput(ctx, &agent.OutboundMessage{
					Type: "message",
					Text: fullResponse,
				}); err != nil {
					log.Error("failed to send response", "error", err)
				}

			case "set_model":
				var payload struct {
					Model string `json:"model"`
				}
				if err := json.Unmarshal(msg.Payload, &payload); err == nil && payload.Model != "" {
					model = payload.Model
					log.Info("model updated", "model", model)
				}

			case "shutdown":
				log.Info("shutdown signal received")
				return nil

			default:
				log.Debug("unknown message type", "type", msg.Type)
			}
		}

		// Check close signal periodically.
		closed, err := ipc.CheckCloseSignal(ctx)
		if err != nil {
			log.Warn("close signal check failed", "error", err)
		}
		if closed {
			log.Info("close signal detected")
			return nil
		}
	}
}

func extractMessageText(payload json.RawMessage) (string, error) {
	var p struct {
		Messages string `json:"messages"`
		Text     string `json:"text"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", err
	}
	if p.Messages != "" {
		return p.Messages, nil
	}
	if p.Text != "" {
		return p.Text, nil
	}
	return "", fmt.Errorf("no text content in payload")
}
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./cmd/kraclaw-agent-openai/`
Expected: Successful build

- [ ] **Step 4: Add build target to Makefile**

Add to `Makefile`:

```makefile
build-agent-openai: ## Build the OpenAI agent binary
	@echo "Building kraclaw-agent-openai..."
	@go build -ldflags="-w -s" -o kraclaw-agent-openai ./cmd/kraclaw-agent-openai
```

Update the `clean` target:

```makefile
clean: ## Clean build artifacts
	@echo "Cleaning..."
	@rm -f $(APP_NAME) $(TUI_NAME) kraclaw-agent-openai
```

- [ ] **Step 5: Commit**

```bash
git add cmd/kraclaw-agent-openai/main.go go.mod go.sum Makefile
git commit -m "feat: add OpenAI agent binary with streaming conversation support"
```

---

### Task 11: OpenAI Agent Dockerfile

**Files:**
- Create: `cmd/kraclaw-agent-openai/Dockerfile`

- [ ] **Step 1: Create Dockerfile**

```dockerfile
# cmd/kraclaw-agent-openai/Dockerfile
FROM golang:1.26-alpine AS builder

WORKDIR /build

RUN apk add --no-cache git

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o kraclaw-agent-openai \
    ./cmd/kraclaw-agent-openai

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /build/kraclaw-agent-openai /agent

LABEL org.opencontainers.image.source="https://github.com/johanssonvincent/kraclaw"
LABEL org.opencontainers.image.description="Kraclaw OpenAI Agent"
LABEL org.opencontainers.image.licenses="Apache-2.0"

ENTRYPOINT ["/agent"]
```

- [ ] **Step 2: Test Docker build locally**

Run: `docker build -f cmd/kraclaw-agent-openai/Dockerfile -t kraclaw-agent-openai:dev .`
Expected: Successful build. Verify small image size with `docker images kraclaw-agent-openai:dev`

- [ ] **Step 3: Commit**

```bash
git add cmd/kraclaw-agent-openai/Dockerfile
git commit -m "feat: add Dockerfile for OpenAI agent image"
```

---

### Task 12: CI Workflow for OpenAI Agent

**Files:**
- Create: `.github/workflows/kraclaw-agent-openai.yaml`

- [ ] **Step 1: Create CI workflow**

Model after existing `kraclaw.yaml` with same hardening (pinned actions, minimal permissions, concurrency):

```yaml
# .github/workflows/kraclaw-agent-openai.yaml
name: Build and Push OpenAI Agent

on:
  push:
    branches: [ main ]
    tags: [ 'v*' ]
    paths:
      - 'cmd/kraclaw-agent-openai/**'
      - 'pkg/agent/**'
      - 'go.mod'
      - 'go.sum'
      - '.github/workflows/kraclaw-agent-openai.yaml'
  pull_request:
    branches: [ main ]
    paths:
      - 'cmd/kraclaw-agent-openai/**'
      - 'pkg/agent/**'
      - 'go.mod'
      - 'go.sum'
      - '.github/workflows/kraclaw-agent-openai.yaml'

concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true

permissions: {}

env:
  REGISTRY: ghcr.io
  IMAGE_NAME: johanssonvincent/kraclaw-agent-openai

jobs:
  test-unit:
    uses: ./.github/workflows/test-unit.yaml

  lint:
    uses: ./.github/workflows/lint.yaml

  build:
    needs: [test-unit, lint]
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write

    steps:
      - name: Checkout repository
        uses: actions/checkout@de0fac2e4500dabe0009e67214ff5f5447ce83dd # v6
        with:
          fetch-depth: 0
          fetch-tags: true

      - name: Resolve version tag
        id: version
        env:
          EVENT_NAME: ${{ github.event_name }}
          PR_NUMBER: ${{ github.event.pull_request.number }}
        run: |
          BASE_VERSION=$(git describe --tags --abbrev=0 2>/dev/null || echo "0.0.0")
          BASE_VERSION=${BASE_VERSION#v}
          echo "base=${BASE_VERSION}" >> "$GITHUB_OUTPUT"

          if [ "$EVENT_NAME" = "pull_request" ]; then
            echo "pr_tag=${BASE_VERSION}-pr${PR_NUMBER}" >> "$GITHUB_OUTPUT"
          fi

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@4d04d5d9486b7bd6fa91e7baf45bbb4f8b9deedd # v4

      - name: Log in to Container registry
        if: github.event_name != 'pull_request'
        uses: docker/login-action@b45d80f862d83dbcd57f89517bcf500b2ab88fb2 # v4
        with:
          registry: ${{ env.REGISTRY }}
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Extract metadata
        id: meta
        uses: docker/metadata-action@030e881283bb7a6894de51c315a6bfe6a94e05cf # v6
        with:
          images: ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}
          tags: |
            type=ref,event=branch
            type=semver,pattern={{version}}
            type=semver,pattern={{major}}.{{minor}}
            type=semver,pattern={{major}}
            type=sha,prefix=main-,enable=${{ github.ref == 'refs/heads/main' }}
            type=raw,value=${{ steps.version.outputs.pr_tag }},enable=${{ github.event_name == 'pull_request' }}
            type=raw,value=latest,enable={{is_default_branch}}

      - name: Build and push Docker image
        uses: docker/build-push-action@d08e5c354a6adb9ed34480a06d141179aa583294 # v7
        with:
          context: .
          file: cmd/kraclaw-agent-openai/Dockerfile
          platforms: linux/amd64
          push: ${{ github.event_name != 'pull_request' }}
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
          cache-from: type=gha
          cache-to: type=gha,mode=max
```

- [ ] **Step 2: Commit**

```bash
git add .github/workflows/kraclaw-agent-openai.yaml
git commit -m "ci: add workflow for OpenAI agent container image"
```

---

### Deferred: CredentialService gRPC RPCs

The design spec calls for `SetCredential`, `DeleteCredential`, and `TestCredential` gRPC RPCs. These are deferred from this plan because:
- The credential store is fully functional via direct MySQL access.
- The MVP uses platform-level credentials (env vars), not per-group credentials managed via API.
- When per-group credential management is needed (BYOK feature), add a `credential.proto` service definition, generate code, and implement handlers that wrap the existing `CredentialStore`.

---

### Task 13: Integration Wiring + Final Verification

**Files:**
- Modify: `cmd/kraclaw/main.go` (wire credential store + multi-provider proxy)

- [ ] **Step 1: Wire credential store and multi-provider proxy in main.go**

In the server startup code where the credential proxy is created, update it to:

1. Create an `Encryptor` from `cfg.Proxy.CredentialEncryptionKey` (if set).
2. Create a `CredentialStore` with the MySQL DB and encryptor.
3. Create a `defaultCredentialResolver` wrapping the store + config.
4. Pass the resolver to the proxy.

```go
// In main.go, where the proxy is created:
var credResolver credproxy.CredentialResolver
if cfg.Proxy.CredentialEncryptionKey != "" {
	enc, err := credproxy.NewEncryptor(cfg.Proxy.CredentialEncryptionKey)
	if err != nil {
		return fmt.Errorf("create encryptor: %w", err)
	}
	credStore := credproxy.NewCredentialStore(mysqlDB, enc)
	credResolver = credproxy.NewDefaultResolver(credStore, cfg.Proxy)
}
proxy, err := credproxy.NewMultiProviderProxy(cfg.Proxy, credResolver)
```

- [ ] **Step 2: Verify full build**

Run: `make build && make build-agent-openai`
Expected: Both binaries compile successfully.

- [ ] **Step 3: Run full test suite**

Run: `make test-short`
Expected: All tests pass.

- [ ] **Step 4: Run linter**

Run: `make lint`
Expected: No errors.

- [ ] **Step 5: Commit**

```bash
git add cmd/kraclaw/main.go
git commit -m "feat: wire credential store and multi-provider proxy in server startup"
```
