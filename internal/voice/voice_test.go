package voice

import (
	"context"
	"testing"
)

func TestNew(t *testing.T) {
	cfg := Config{Enabled: true}
	c := New(cfg)
	if c == nil {
		t.Fatal("New() returned nil")
	}
}

func TestEnabled(t *testing.T) {
	c := New(Config{Enabled: true})
	if !c.Enabled() {
		t.Error("Enabled() should be true")
	}

	c2 := New(Config{Enabled: false})
	if c2.Enabled() {
		t.Error("Enabled() should be false")
	}
}

func TestSynthesize_Disabled(t *testing.T) {
	c := New(Config{Enabled: false})
	_, err := c.Synthesize(context.Background(), "test")
	if err == nil {
		t.Error("Synthesize() should return error when disabled")
	}
}

func TestSynthesize_EmptyText(t *testing.T) {
	c := New(Config{Enabled: true, TTSProvider: ProviderOpenAI, OpenAIAPIKey: "test"})
	_, err := c.Synthesize(context.Background(), "")
	if err == nil {
		t.Error("Synthesize() should return error for empty text")
	}
}

func TestSynthesize_UnsupportedProvider(t *testing.T) {
	c := New(Config{Enabled: true, TTSProvider: Provider("invalid")})
	_, err := c.Synthesize(context.Background(), "test")
	if err == nil {
		t.Error("Synthesize() should return error for unsupported provider")
	}
}

func TestRecognize_Disabled(t *testing.T) {
	c := New(Config{Enabled: false})
	_, err := c.Recognize(context.Background(), []byte{})
	if err == nil {
		t.Error("Recognize() should return error when disabled")
	}
}

func TestRecognize_EmptyAudio(t *testing.T) {
	c := New(Config{Enabled: true, STTProvider: ProviderOpenAI})
	_, err := c.Recognize(context.Background(), []byte{})
	if err == nil {
		t.Error("Recognize() should return error for empty audio")
	}
}

func TestRecognize_UnsupportedProvider(t *testing.T) {
	c := New(Config{Enabled: true, STTProvider: Provider("invalid")})
	_, err := c.Recognize(context.Background(), []byte{1, 2, 3})
	if err == nil {
		t.Error("Recognize() should return error for unsupported provider")
	}
}

func TestIsWAV(t *testing.T) {
	if !isWAV([]byte("RIFF")) {
		t.Error("isWAV() should detect RIFF header")
	}
	if isWAV([]byte("OggS")) {
		t.Error("isWAV() should not detect OGG as WAV")
	}
}

func TestIsOGG(t *testing.T) {
	if !isOGG([]byte("OggS")) {
		t.Error("isOGG() should detect OGG header")
	}
	if isOGG([]byte("RIFF")) {
		t.Error("isOGG() should not detect WAV as OGG")
	}
}

func TestIsFLAC(t *testing.T) {
	if !isFLAC([]byte("fLaC")) {
		t.Error("isFLAC() should detect FLAC header")
	}
	if isFLAC([]byte("RIFF")) {
		t.Error("isFLAC() should not detect WAV as FLAC")
	}
}

func TestToBase64(t *testing.T) {
	data := []byte("test")
	b64 := ToBase64(data)
	if b64 == "" {
		t.Error("ToBase64() should not return empty string")
	}
}

func TestFromBase64(t *testing.T) {
	b64 := "dGVzdA==" // "test" in base64
	data, err := FromBase64(b64)
	if err != nil {
		t.Fatalf("FromBase64() error = %v", err)
	}
	if string(data) != "test" {
		t.Errorf("FromBase64() = %q, want %q", string(data), "test")
	}
}

func TestFromBase64_Invalid(t *testing.T) {
	_, err := FromBase64("!!!invalid!!!")
	if err == nil {
		t.Error("FromBase64() should return error for invalid input")
	}
}

func TestFormatAudioPrompt(t *testing.T) {
	prompt := FormatAudioPrompt([]byte{1, 2, 3}, "mp3")
	if prompt == "" {
		t.Error("FormatAudioPrompt() should not be empty")
	}
}

func TestHandleVoiceCommand_Status(t *testing.T) {
	c := New(Config{Enabled: true, TTSProvider: ProviderOpenAI, VoiceID: "alloy"})
	result, err := HandleVoiceCommand("status", "", c)
	if err != nil {
		t.Fatalf("HandleVoiceCommand() error = %v", err)
	}
	if result == "" {
		t.Error("HandleVoiceCommand status should not be empty")
	}
}

func TestHandleVoiceCommand_Disabled(t *testing.T) {
	c := New(Config{Enabled: false})
	result, err := HandleVoiceCommand("tts hello", "", c)
	if err != nil {
		t.Fatalf("HandleVoiceCommand() error = %v", err)
	}
	if result == "" {
		t.Error("HandleVoiceCommand() should return disabled message")
	}
}

func TestHandleVoiceCommand_None(t *testing.T) {
	result, err := HandleVoiceCommand("tts hello", "", nil)
	if err != nil {
		t.Fatalf("HandleVoiceCommand() error = %v", err)
	}
	if result == "" {
		t.Error("HandleVoiceCommand() with nil client should return disabled message")
	}
}

func TestProvider_Constants(t *testing.T) {
	if ProviderOpenAI != "openai" {
		t.Error("ProviderOpenAI should be 'openai'")
	}
	if ProviderElevenLabs != "elevenlabs" {
		t.Error("ProviderElevenLabs should be 'elevenlabs'")
	}
	if ProviderEdge != "edge" {
		t.Error("ProviderEdge should be 'edge'")
	}
}
