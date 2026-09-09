package chatgpt

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// errReader yields a transport error after producing n bytes of payload.
type errReader struct {
	payload []byte
	err     error
	off     int
}

func (r *errReader) Read(p []byte) (int, error) {
	if r.off >= len(r.payload) {
		return 0, r.err
	}
	n := copy(p, r.payload[r.off:])
	r.off += n
	return n, nil
}

func TestReadCappedBody(t *testing.T) {
	t.Parallel()
	cap := maxResponseBodySize
	tests := map[string]struct {
		reader   io.Reader
		wantN    int
		wantErr  error
		wantText string
	}{
		"empty": {
			reader:  bytes.NewReader(nil),
			wantN:   0,
			wantErr: nil,
		},
		"small body under cap": {
			reader:  bytes.NewReader([]byte(`{"ok":true}`)),
			wantN:   len(`{"ok":true}`),
			wantErr: nil,
		},
		"exactly at cap": {
			reader:  bytes.NewReader(bytes.Repeat([]byte{'a'}, cap)),
			wantN:   cap,
			wantErr: nil,
		},
		"one byte over cap": {
			reader:  bytes.NewReader(bytes.Repeat([]byte{'a'}, cap+1)),
			wantN:   0,
			wantErr: ErrResponseTooLarge,
		},
		"far over cap": {
			reader:  bytes.NewReader(bytes.Repeat([]byte{'a'}, cap*2)),
			wantN:   0,
			wantErr: ErrResponseTooLarge,
		},
		"reader error under cap": {
			reader:  &errReader{payload: []byte("partial"), err: io.ErrUnexpectedEOF},
			wantN:   0,
			wantErr: io.ErrUnexpectedEOF,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			body, err := readCappedBody(tc.reader)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("readCappedBody err = %v, want errors.Is(%v)", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("readCappedBody err = %v, want nil", err)
			}
			if len(body) != tc.wantN {
				t.Errorf("len(body) = %d, want %d", len(body), tc.wantN)
			}
		})
	}
}

// overSizeHandler streams maxResponseBodySize+1 bytes and a 200 status.
// Used by the four endpoint-level cap tests below.
func overSizeHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bytes.Repeat([]byte{'a'}, maxResponseBodySize+1))
	}
}

func TestRequestDeviceCode_BodyTooLarge(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(overSizeHandler())
	defer srv.Close()
	c := newTestClient(t, srv)

	_, err := c.RequestDeviceCode(context.Background())
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want errors.Is(ErrResponseTooLarge)", err)
	}
	if !strings.Contains(err.Error(), "device-code response") {
		t.Errorf("err msg = %q, want contains %q", err.Error(), "device-code response")
	}
}

func TestPollOnce_BodyTooLarge(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(overSizeHandler())
	defer srv.Close()
	c := newTestClient(t, srv)
	dc := deviceCodeForTest("dev_abc", "USER-1234", srv.URL+"/codex/device", 5*1e6)

	_, err := c.PollOnce(context.Background(), dc)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want errors.Is(ErrResponseTooLarge)", err)
	}
	if !strings.Contains(err.Error(), "poll response") {
		t.Errorf("err msg = %q, want contains %q", err.Error(), "poll response")
	}
}

func TestExchangeCode_BodyTooLarge(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(overSizeHandler())
	defer srv.Close()
	c := newTestClient(t, srv)
	ac := &AuthorizationCode{Code: "auth_code", CodeVerifier: "verifier"}

	_, err := c.ExchangeCode(context.Background(), ac)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want errors.Is(ErrResponseTooLarge)", err)
	}
	if !strings.Contains(err.Error(), "token-exchange response") {
		t.Errorf("err msg = %q, want contains %q", err.Error(), "token-exchange response")
	}
}

func TestRefresh_BodyTooLarge(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(overSizeHandler())
	defer srv.Close()
	c := newTestClient(t, srv)

	_, err := c.Refresh(context.Background(), "refresh_token")
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want errors.Is(ErrResponseTooLarge)", err)
	}
	var refreshErr *RefreshError
	if !errors.As(err, &refreshErr) {
		t.Fatalf("err = %v, want errors.As(*RefreshError)", err)
	}
	if refreshErr.Permanent() {
		t.Errorf("Permanent() = true, want false (oversized body should be transient)")
	}
}
