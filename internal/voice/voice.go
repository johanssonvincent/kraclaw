package voice

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Provider is a voice provider type.
type Provider string

const (
	// ProviderOpenAI is the OpenAI TTS/STT provider.
	ProviderOpenAI Provider = "openai"

	// ProviderElevenLabs is the ElevenLabs TTS provider.
	ProviderElevenLabs Provider = "elevenlabs"

	// ProviderEdge is the Edge TTS provider (free, Microsoft).
	ProviderEdge Provider = "edge"
)

// Config holds voice configuration.
type Config struct {
	// Enabled controls whether voice mode is active.
	Enabled bool `envconfig:"VOICE_ENABLED" default:"false"`

	// TTSProvider is the text-to-speech provider.
	TTSProvider Provider `envconfig:"VOICE_TTS_PROVIDER" default:"openai"`

	// STTProvider is the speech-to-text provider.
	STTProvider Provider `envconfig:"VOICE_STT_PROVIDER" default:"openai"`

	// OpenAIAPIKey is the OpenAI API key.
	OpenAIAPIKey string `envconfig:"VOICE_OPENAI_API_KEY"`

	// ElevenLabsAPIKey is the ElevenLabs API key.
	ElevenLabsAPIKey string `envconfig:"VOICE_ELEVENLABS_API_KEY"`

	// VoiceID is the voice ID to use for TTS.
	VoiceID string `envconfig:"VOICE_ID" default:"alloy"`

	// STTLanguage is the language for speech-to-text.
	STTLanguage string `envconfig:"VOICE_STT_LANGUAGE" default:"en"`

	// TTSSpeed is the TTS speed multiplier.
	TTSSpeed float64 `envconfig:"VOICE_TTS_SPEED" default:"1.0"`
}

// Client manages voice operations (TTS and STT).
type Client struct {
	cfg    Config
	log    *slog.Logger
	client *http.Client
}

// New creates a new voice client.
func New(cfg Config) *Client {
	return &Client{
		cfg: cfg,
		log: slog.With("component", "voice"),
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Enabled returns whether voice mode is active.
func (c *Client) Enabled() bool {
	return c.cfg.Enabled
}

// Synthesize converts text to speech audio.
func (c *Client) Synthesize(ctx context.Context, text string) ([]byte, error) {
	if !c.cfg.Enabled {
		return nil, fmt.Errorf("voice mode is not enabled")
	}

	if text == "" {
		return nil, fmt.Errorf("text is required")
	}

	// Truncate very long text.
	if len(text) > 4000 {
		text = text[:4000] + "..."
	}

	switch c.cfg.TTSProvider {
	case ProviderOpenAI:
		return c.synthesizeOpenAI(ctx, text)
	case ProviderElevenLabs:
		return c.synthesizeElevenLabs(ctx, text)
	case ProviderEdge:
		return c.synthesizeEdge(ctx, text)
	default:
		return nil, fmt.Errorf("unsupported TTS provider: %s", c.cfg.TTSProvider)
	}
}

// synthesizeOpenAI uses OpenAI's TTS API.
func (c *Client) synthesizeOpenAI(ctx context.Context, text string) ([]byte, error) {
	if c.cfg.OpenAIAPIKey == "" {
		return nil, fmt.Errorf("OpenAI API key is not configured")
	}

	reqBody := map[string]any{
		"model":  "gpt-4o-mini-tts",
		"input":  text,
		"voice":  c.cfg.VoiceID,
		"speed":  c.cfg.TTSSpeed,
		"format": "mp3",
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal tts request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/audio/speech", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create tts request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.OpenAIAPIKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute tts request: %w", err)
	}

	if err := resp.Body.Close(); err != nil {
		return nil, fmt.Errorf("close tts response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)

		return nil, fmt.Errorf("tts API returned %d: %s", resp.StatusCode, string(body))
	}

	return io.ReadAll(resp.Body)
}

// synthesizeElevenLabs uses ElevenLabs TTS API.
func (c *Client) synthesizeElevenLabs(ctx context.Context, text string) ([]byte, error) {
	if c.cfg.ElevenLabsAPIKey == "" {
		return nil, fmt.Errorf("ElevenLabs API key is not configured")
	}

	reqBody := map[string]any{
		"text":     text,
		"model_id": "eleven_turbo_v2",
		"voice_settings": map[string]any{
			"stability":  0.5,
			"similarity": 0.8,
			"speed":      c.cfg.TTSSpeed,
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal tts request: %w", err)
	}

	url := fmt.Sprintf("https://api.elevenlabs.io/v1/text-to-speech/%s/stream", c.cfg.VoiceID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create tts request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("xi-api-key", c.cfg.ElevenLabsAPIKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute tts request: %w", err)
	}

	if err := resp.Body.Close(); err != nil {
		return nil, fmt.Errorf("close tts response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)

		return nil, fmt.Errorf("tts API returned %d: %s", resp.StatusCode, string(body))
	}

	return io.ReadAll(resp.Body)
}

// synthesizeEdge uses Microsoft Edge TTS (free).
func (c *Client) synthesizeEdge(ctx context.Context, text string) ([]byte, error) {
	// Edge TTS uses a different protocol — we'll use a simple HTTP approach.
	// This is a simplified implementation; production use should use the official SDK.
	voice := c.cfg.VoiceID
	if voice == "" || voice == "alloy" {
		voice = "en-US-GuyNeural"
	}

	lang := "en-US"
	if strings.HasPrefix(c.cfg.VoiceID, "en-GB") {
		lang = "en-GB"
	}

	reqBody := map[string]any{
		"input": text,
		"voice": voice,
		"lang":  lang,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal tts request: %w", err)
	}

	// Use a public Edge TTS endpoint.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.edge-tts.com/v1/synthesize", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create tts request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute tts request: %w", err)
	}

	if err := resp.Body.Close(); err != nil {
		return nil, fmt.Errorf("close tts response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)

		return nil, fmt.Errorf("tts API returned %d: %s", resp.StatusCode, string(body))
	}

	return io.ReadAll(resp.Body)
}

// Recognize converts speech audio to text.
func (c *Client) Recognize(ctx context.Context, audio []byte) (string, error) {
	if !c.cfg.Enabled {
		return "", fmt.Errorf("voice mode is not enabled")
	}

	if len(audio) == 0 {
		return "", fmt.Errorf("audio is required")
	}

	switch c.cfg.STTProvider {
	case ProviderOpenAI:
		return c.recognizeOpenAI(ctx, audio)
	default:
		return "", fmt.Errorf("unsupported STT provider: %s", c.cfg.STTProvider)
	}
}

// recognizeOpenAI uses OpenAI's Whisper API.
func (c *Client) recognizeOpenAI(ctx context.Context, audio []byte) (string, error) {
	if c.cfg.OpenAIAPIKey == "" {
		return "", fmt.Errorf("OpenAI API key is not configured")
	}

	// Detect format from audio content.
	format := "mp3"
	if isWAV(audio) {
		format = "wav"
	} else if isOGG(audio) {
		format = "ogg"
	} else if isFLAC(audio) {
		format = "flac"
	}

	// Build multipart form request.
	boundary := "boundary-" + randomString(16)
	body := &bytes.Buffer{}

	// Write model field.
	fmt.Fprintf(body, "--%s\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nwhisper-1\r\n", boundary)

	// Write file field.
	fmt.Fprintf(body, "--%s\r\nContent-Disposition: form-data; name=\"file\"; filename=\"audio.%s\"\r\nContent-Type: audio/%s\r\n\r\n", boundary, format, format)
	body.Write(audio)
	fmt.Fprintf(body, "\r\n")

	// Write language field if set.
	if c.cfg.STTLanguage != "" {
		fmt.Fprintf(body, "--%s\r\nContent-Disposition: form-data; name=\"language\"\r\n\r\n%s\r\n", boundary, c.cfg.STTLanguage)
	}

	// Close boundary.
	fmt.Fprintf(body, "--%s--\r\n", boundary)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/audio/transcriptions", body)
	if err != nil {
		return "", fmt.Errorf("create stt request: %w", err)
	}

	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.Header.Set("Authorization", "Bearer "+c.cfg.OpenAIAPIKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("execute stt request: %w", err)
	}

	if err := resp.Body.Close(); err != nil {
		return "", fmt.Errorf("close stt response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)

		return "", fmt.Errorf("stt API returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Text string `json:"text"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode stt response: %w", err)
	}

	return result.Text, nil
}

// isWAV checks if audio is in WAV format.
func isWAV(audio []byte) bool {
	return len(audio) >= 4 && string(audio[:4]) == "RIFF"
}

// isOGG checks if audio is in OGG format.
func isOGG(audio []byte) bool {
	return len(audio) >= 4 && string(audio[:4]) == "OggS"
}

// isFLAC checks if audio is in FLAC format.
func isFLAC(audio []byte) bool {
	return len(audio) >= 4 && string(audio[:4]) == "fLaC"
}

// randomString generates a random string.
func randomString(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"

	b := make([]byte, n)

	for i := range b {
		b[i] = chars[int(time.Now().UnixNano())%len(chars)]

		time.Sleep(time.Nanosecond)
	}

	return string(b)
}

// AudioMessage represents a voice message.
type AudioMessage struct {
	Audio  []byte
	Format string
	Text   string // Optional transcript.
}

// FormatAudioPrompt formats an audio message for display.
func FormatAudioPrompt(audio []byte, format string) string {
	return fmt.Sprintf("[Audio message (%s, %d bytes)]", format, len(audio))
}

// ToBase64 converts audio to base64.
func ToBase64(audio []byte) string {
	return base64.StdEncoding.EncodeToString(audio)
}

// FromBase64 converts base64 to audio.
func FromBase64(b64 string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(b64)
}

// HandleVoiceCommand processes voice-related commands.
func HandleVoiceCommand(cmd string, args string, client *Client) (string, error) {
	if client == nil || !client.Enabled() {
		return "Voice mode is not enabled.", nil
	}

	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return "Unknown voice command.", nil
	}

	switch strings.ToLower(parts[0]) {
	case "tts", "speak":
		text := strings.Join(parts[1:], " ")
		if text == "" {
			return "Please provide text to speak.", nil
		}

		audio, err := client.Synthesize(context.Background(), text)
		if err != nil {
			return fmt.Sprintf("TTS failed: %v", err), nil
		}

		return fmt.Sprintf("[Generated audio: %d bytes]", len(audio)), nil

	case "stt", "transcribe":
		// STT requires audio input, which should be provided separately.
		return "Please provide audio to transcribe.", nil

	case "status":
		return fmt.Sprintf("Voice mode: enabled\nTTS provider: %s\nSTT provider: %s\nVoice: %s",
			client.cfg.TTSProvider, client.cfg.STTProvider, client.cfg.VoiceID), nil

	default:
		return "Unknown voice command. Use: tts, speak, stt, transcribe, status", nil
	}
}
