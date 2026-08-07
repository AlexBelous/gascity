package telemetry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/log/global"
	collectormetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/protobuf/proto"
)

func TestForceFlushDisabledIsNoop(t *testing.T) {
	if err := ForceFlush(context.Background(), nil); err != nil {
		t.Fatalf("ForceFlush(nil) = %v, want nil", err)
	}
}

func TestProviderForceFlushDoesNotShutdown(t *testing.T) {
	var mu sync.Mutex
	flushed, shutDown := 0, 0
	p := &Provider{
		flushes: []func(context.Context) error{func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			flushed++
			return nil
		}},
		shutdowns: []func(context.Context) error{func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			shutDown++
			return nil
		}},
	}

	if err := p.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush() = %v", err)
	}
	if p.shutdownDone {
		t.Fatal("ForceFlush marked provider shut down")
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() = %v", err)
	}
	if flushed != 1 || shutDown != 1 {
		t.Fatalf("flushes=%d shutdowns=%d, want 1 each", flushed, shutDown)
	}
}

func TestProviderForceFlushUnreachableAndCancellation(t *testing.T) {
	boom := errors.New("unreachable")
	p := &Provider{flushes: []func(context.Context) error{func(context.Context) error { return boom }}}
	if err := p.ForceFlush(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("ForceFlush() error = %v, want unreachable error", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	p = &Provider{flushes: []func(context.Context) error{func(context.Context) error {
		called = true
		return nil
	}}}
	if err := p.ForceFlush(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ForceFlush(canceled) error = %v, want context.Canceled", err)
	}
	if called {
		t.Fatal("ForceFlush invoked a flush function after cancellation")
	}
}

func TestProviderForceFlushAddsBoundedDeadline(t *testing.T) {
	deadlineSeen := false
	p := &Provider{flushes: []func(context.Context) error{func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("ForceFlush context has no deadline")
		}
		if remaining := time.Until(deadline); remaining <= 0 || remaining > ForceFlushTimeout {
			t.Fatalf("ForceFlush deadline remaining = %s, want (0, %s]", remaining, ForceFlushTimeout)
		}
		deadlineSeen = true
		return nil
	}}}
	if err := p.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush() = %v", err)
	}
	if !deadlineSeen {
		t.Fatal("ForceFlush did not invoke its flush function")
	}
}

func TestForceFlushAcknowledgesHealthProbeAtIsolatedOTLPReceiver(t *testing.T) {
	resetInitState(t)
	previousMeter := otel.GetMeterProvider()
	previousLogger := global.GetLoggerProvider()
	t.Cleanup(func() {
		otel.SetMeterProvider(previousMeter)
		global.SetLoggerProvider(previousLogger)
	})

	acknowledged := make(chan struct{}, 1)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/metrics" {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("reading OTLP metrics request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			request := &collectormetrics.ExportMetricsServiceRequest{}
			if err := proto.Unmarshal(body, request); err != nil {
				t.Errorf("decoding OTLP metrics request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if !otlpRequestHasMetric(request, beadStoreHealthMetric) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			select {
			case acknowledged <- struct{}{}:
			default:
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()
	t.Setenv(EnvMetricsURL, receiver.URL+"/v1/metrics")
	t.Setenv(EnvLogsURL, receiver.URL+"/v1/logs")
	t.Setenv(EnvOTLPEndpoint, "")

	p, err := Init(context.Background(), "test-svc", "0.0.1")
	if err != nil {
		t.Fatalf("Init() = %v", err)
	}
	if p == nil {
		t.Fatal("Init() returned nil provider")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = p.Shutdown(ctx)
	})
	RecordBeadStoreHealth(context.Background(), "city-a", true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.ForceFlush(ctx); err != nil {
		t.Fatalf("ForceFlush() = %v", err)
	}
	select {
	case <-acknowledged:
	case <-ctx.Done():
		t.Fatal("isolated OTLP receiver did not acknowledge the health probe")
	}
}

func otlpRequestHasMetric(request *collectormetrics.ExportMetricsServiceRequest, name string) bool {
	for _, resource := range request.ResourceMetrics {
		for _, scope := range resource.ScopeMetrics {
			for _, metric := range scope.Metrics {
				if metric.Name == name {
					return true
				}
			}
		}
	}
	return false
}
