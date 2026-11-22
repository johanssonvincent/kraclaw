package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestWaitForInputDeliversMessage covers the normal path.
func TestWaitForInputDeliversMessage(t *testing.T) {
	msgCh := make(chan *InboundMessage, 1)
	errCh := make(chan error, 1)

	want := &InboundMessage{Type: "message", Payload: json.RawMessage(`{"text":"hi"}`)}
	msgCh <- want

	got, err := WaitForInput(context.Background(), msgCh, errCh)
	if err != nil {
		t.Fatalf("WaitForInput returned error: %v", err)
	}

	if got != want {
		t.Fatalf("WaitForInput returned %v, want the queued message", got)
	}
}

// TestWaitForInputClosedErrorChannelIsCleanShutdown is the regression guard for
// the agent loop's previous behaviour: a closed errCh yields a nil error value
// on every receive, and fmt.Errorf("...: %w", nil) is a non-nil error whose
// message is "%!w(<nil>)". Both agent binaries therefore reported a bogus
// failure - and exited non-zero - on every clean shutdown, when the reader
// closes msgCh and errCh together.
func TestWaitForInputClosedErrorChannelIsCleanShutdown(t *testing.T) {
	msgCh := make(chan *InboundMessage)
	errCh := make(chan error, 1)

	// Mirror startReadInput: flush the cause (none here) and close both.
	close(msgCh)
	close(errCh)

	done := make(chan struct{})

	var (
		got *InboundMessage
		err error
	)

	go func() {
		defer close(done)

		got, err = WaitForInput(context.Background(), msgCh, errCh)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForInput blocked on closed channels")
	}

	if err != nil {
		t.Fatalf("WaitForInput returned error %v on a clean shutdown, want nil", err)
	}

	if got != nil {
		t.Fatalf("WaitForInput returned message %v, want nil", got)
	}
}

// TestWaitForInputReturnsBufferedError checks a real reader failure still
// surfaces: the reader sends it before closing the channel.
func TestWaitForInputReturnsBufferedError(t *testing.T) {
	msgCh := make(chan *InboundMessage)
	errCh := make(chan error, 1)

	rootCause := errors.New("nats: connection closed")
	errCh <- rootCause
	close(errCh)
	close(msgCh)

	_, err := WaitForInput(context.Background(), msgCh, errCh)
	if err == nil {
		t.Fatal("WaitForInput returned nil error, want the buffered reader failure")
	}

	if !errors.Is(err, rootCause) {
		t.Fatalf("error %v does not wrap the reader failure", err)
	}

	if !strings.Contains(err.Error(), "ipc read") {
		t.Fatalf("error %q lost its operation context", err.Error())
	}
}

// TestWaitForInputInputClosedWithLiveContext covers a reader that stops without
// a cause while the context is still live - a genuine anomaly.
func TestWaitForInputInputClosedWithLiveContext(t *testing.T) {
	msgCh := make(chan *InboundMessage)
	errCh := make(chan error, 1)

	close(msgCh)

	_, err := WaitForInput(context.Background(), msgCh, errCh)
	if !errors.Is(err, ErrInputChannelClosed) {
		t.Fatalf("WaitForInput returned %v, want ErrInputChannelClosed", err)
	}
}

// TestWaitForInputContextCancelled checks shutdown does not depend on the
// channels closing.
func TestWaitForInputContextCancelled(t *testing.T) {
	msgCh := make(chan *InboundMessage)
	errCh := make(chan error, 1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	msg, err := WaitForInput(ctx, msgCh, errCh)
	if err != nil {
		t.Fatalf("WaitForInput returned error %v after cancellation, want nil", err)
	}

	if msg != nil {
		t.Fatalf("WaitForInput returned message %v, want nil", msg)
	}
}
