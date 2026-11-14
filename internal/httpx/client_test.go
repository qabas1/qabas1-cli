package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(base time.Duration, attempts int) (*http.Client, *Transport) {
	tr := NewTransport(http.DefaultTransport.(*http.Transport).Clone(), base, 4*base, attempts)
	tr.Backoff = time.Millisecond
	return &http.Client{Transport: tr}, tr
}

func TestRetriesServiceUnavailable(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	client, _ := testClient(time.Second, 4)
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "ok" || atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("body=%q calls=%d", body, calls)
	}
}

func TestTimeoutGrowsAndIsRemembered(t *testing.T) {
	var calls int32
	// Drive the first attempt to its context deadline directly. A sleeping HTTP
	// handler can race the deadline timer on a busy CI runner and occasionally
	// return successfully, leaving this test with only one call.
	next := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
	})
	tr := NewTransport(next, 10*time.Millisecond, 40*time.Millisecond, 3)
	tr.Backoff = time.Millisecond
	client := &http.Client{Transport: tr}
	resp, err := client.Get("http://example.test/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("calls=%d, want 2", calls)
	}
	if got := tr.startTimeout(); got <= 10*time.Millisecond {
		t.Fatalf("learned timeout %v did not grow", got)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPostIsNotRetried(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	client, _ := testClient(time.Second, 4)
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("x"))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("calls=%d, want 1", calls)
	}
}
