package flickrclient

import (
	"errors"
	"testing"
	"time"
)

func resetBreaker(t *testing.T) {
	t.Helper()
	apiBreaker.reset()
	t.Cleanup(apiBreaker.reset)
}

func TestCall_RetriesTransientErrorThenSucceeds(t *testing.T) {
	resetBreaker(t)
	calls := 0
	withUnsignedGet(t, func(string, map[string]string) (string, error) {
		calls++
		if calls < 3 {
			return "", errors.New("connection timed out")
		}
		return `{"stat":"ok"}`, nil
	})
	if _, err := makeClient().Call("flickr.test.echo", nil); err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls)
	}
}

func TestCall_BackoffDelays(t *testing.T) {
	resetBreaker(t)
	var delays []time.Duration
	orig := sleepFunc
	sleepFunc = func(d time.Duration) {
		if d >= retryBaseDelay {
			delays = append(delays, d)
		}
	}
	t.Cleanup(func() { sleepFunc = orig })
	withUnsignedGet(t, func(string, map[string]string) (string, error) {
		return "", errors.New("timeout")
	})
	_, err := makeClient().Call("flickr.test.echo", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}
	if len(delays) != len(want) {
		t.Fatalf("delays = %v, want %v", delays, want)
	}
	for i := range want {
		if delays[i] != want[i] {
			t.Fatalf("delays = %v, want %v", delays, want)
		}
	}
}

func TestCall_DoesNotRetryAPIError(t *testing.T) {
	resetBreaker(t)
	calls := 0
	withUnsignedGet(t, func(string, map[string]string) (string, error) {
		calls++
		return `{"stat":"fail","code":1,"message":"Photo not found"}`, nil
	})
	_, err := makeClient().Call("flickr.photos.getInfo", nil)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != 1 {
		t.Fatalf("expected APIError code 1, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("APIError must not be retried, got %d calls", calls)
	}
}

func TestCall_RetriesServiceUnavailable(t *testing.T) {
	resetBreaker(t)
	calls := 0
	withUnsignedGet(t, func(string, map[string]string) (string, error) {
		calls++
		if calls == 1 {
			return `{"stat":"fail","code":105,"message":"Service currently unavailable"}`, nil
		}
		return `{"stat":"ok"}`, nil
	})
	if _, err := makeClient().Call("flickr.test.echo", nil); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 attempts, got %d", calls)
	}
}

func TestCall_RetriesNonJSONBody(t *testing.T) {
	resetBreaker(t)
	calls := 0
	withUnsignedGet(t, func(string, map[string]string) (string, error) {
		calls++
		if calls == 1 {
			return "<html>502 Bad Gateway</html>", nil
		}
		return `{"stat":"ok"}`, nil
	})
	if _, err := makeClient().Call("flickr.test.echo", nil); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestBreaker_OpensOnSustainedFailures(t *testing.T) {
	resetBreaker(t)
	calls := 0
	withUnsignedGet(t, func(string, map[string]string) (string, error) {
		calls++
		return "", errors.New("timeout")
	})
	c := makeClient()
	var last error
	for i := 0; i < breakerMinSamples; i++ {
		_, last = c.Call("flickr.test.echo", nil)
	}
	if errors.Is(last, ErrCircuitOpen) {
		t.Fatal("breaker should open only after the call that crosses the threshold")
	}
	before := calls
	_, err := c.Call("flickr.test.echo", nil)
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}
	if calls != before {
		t.Fatal("open breaker must not hit the network")
	}
}

func TestBreaker_StaysClosedOnIsolatedFailures(t *testing.T) {
	resetBreaker(t)
	c := makeClient()
	// ogni 4a chiamata fallisce per tutti i retry: ~25% di fallimenti
	for i := 0; i < 50; i++ {
		if i%4 == 3 {
			withUnsignedGet(t, func(string, map[string]string) (string, error) { return "", errors.New("timeout") })
		} else {
			withUnsignedGet(t, func(string, map[string]string) (string, error) { return `{"stat":"ok"}`, nil })
		}
		_, err := c.Call("flickr.test.echo", nil)
		if errors.Is(err, ErrCircuitOpen) {
			t.Fatalf("breaker opened at 25%% failure rate (call %d)", i)
		}
	}
}
