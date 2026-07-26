//go:build productmetrics_testhook

package main

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/productmetrics"
	"github.com/gastownhall/gascity/internal/testutil"
)

const (
	productMetricsTesthookEndpointEnvironment = "GC_PRODUCT_METRICS_TESTHOOK_ENDPOINT"
	productMetricsTesthookCAFileEnvironment   = "GC_PRODUCT_METRICS_TESTHOOK_CA_FILE"
	productMetricsTestReleaseVersion          = "0.31.0"
	productMetricsTestInstallationID          = "3cf9fd4e-3337-4c29-a0ab-2858cd8a1f21"
	productMetricsTestSpoolGeneration         = "22222222-2222-4222-8222-222222222222"
	productMetricsTestEventID                 = "8c4f4128-a6e8-4f66-bd1b-1fcf1298b124"
)

type capturedProductMetricsRequest struct {
	method             string
	path               string
	contentType        string
	accept             string
	userAgent          string
	acceptEncoding     string
	authorization      string
	cookie             string
	proxyAuthorization string
	batch              productmetrics.Batch
	err                error
}

func runProductMetricsPrivateUploaderProcessContract(t *testing.T, taggedBinary string) {
	t.Helper()
	requests := make(chan capturedProductMetricsRequest, 2)
	var abortAcknowledgement atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, readErr := io.ReadAll(io.LimitReader(request.Body, 65*1024))
		batch, decodeErr := productmetrics.DecodeBatch(body)
		requests <- capturedProductMetricsRequest{
			method:             request.Method,
			path:               request.URL.Path,
			contentType:        request.Header.Get("Content-Type"),
			accept:             request.Header.Get("Accept"),
			userAgent:          request.Header.Get("User-Agent"),
			acceptEncoding:     request.Header.Get("Accept-Encoding"),
			authorization:      request.Header.Get("Authorization"),
			cookie:             request.Header.Get("Cookie"),
			proxyAuthorization: request.Header.Get("Proxy-Authorization"),
			batch:              batch,
			err:                errors.Join(readErr, decodeErr),
		}
		if abortAcknowledgement.Load() {
			panic(http.ErrAbortHandler)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer,
			`{"schema_version":1,"app":"gascity","action":"accepted","event_ids":[%q]}`,
			productMetricsTestEventID,
		)
	}))
	t.Cleanup(server.Close)
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	caFile := filepath.Join(t.TempDir(), "loopback-ca.pem")
	if err := os.WriteFile(caFile, certificatePEM, 0o600); err != nil {
		t.Fatal(err)
	}

	workingDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workingDir, "city.toml"), []byte("invalid = [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	privateHome := t.TempDir()
	attemptToken := "6ba7b810-9dad-41d1-80b4-00c04fd430c8"
	queuedEvent := seedPrivateUploaderProcessFixture(t, privateHome, attemptToken, time.Now().UTC())
	queuedPath := filepath.Join(
		privateHome, "product-usage", "queue", productMetricsTestSpoolGeneration, productMetricsTestEventID+".json",
	)
	inflightPath := filepath.Join(
		privateHome, "product-usage", "inflight", productMetricsTestSpoolGeneration, productMetricsTestEventID+".json",
	)
	baseEnvironment := []string{
		"GC_HOME=" + privateHome,
		"GC_OTEL_METRICS_URL=://invalid-private-uploader-test-url",
		"HTTPS_PROXY=http://127.0.0.1:1",
		"SSL_CERT_FILE=/does/not/exist",
		productMetricsTesthookEndpointEnvironment + "=" + server.URL + "/v1/command-usage",
		productMetricsTesthookCAFileEnvironment + "=" + caFile,
		"HOME=" + t.TempDir(),
		"LANG=C",
	}
	t.Run("missing marker cannot read tagged CA", func(t *testing.T) {
		mkfifo, err := exec.LookPath("mkfifo")
		if err != nil {
			t.Skip("mkfifo is unavailable")
		}
		blockedCA := filepath.Join(t.TempDir(), "blocked-ca.pem")
		if output, err := exec.Command(mkfifo, blockedCA).CombinedOutput(); err != nil {
			t.Fatalf("mkfifo: %v\n%s", err, output)
		}
		ctx, cancel := context.WithTimeout(context.Background(), testutil.ExecRaceTimeout)
		defer cancel()
		missingMarker := exec.CommandContext(ctx, taggedBinary,
			productMetricsPrivateUploaderSentinelFixture,
			attemptToken,
		)
		missingMarker.Dir = workingDir
		missingMarker.Env = replaceProductMetricsProcessEnvironment(
			baseEnvironment,
			productMetricsTesthookCAFileEnvironment,
			blockedCA,
		)
		output, err := missingMarker.CombinedOutput()
		if ctx.Err() != nil {
			t.Fatal("missing-marker child tried to open the blocking tagged CA path")
		}
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) || exitError.ExitCode() == 0 {
			t.Fatalf("missing-marker child error = %v, want nonzero exit", err)
		}
		if len(output) != 0 {
			t.Fatalf("missing-marker child wrote normal output: %q", output)
		}
		select {
		case request := <-requests:
			t.Fatalf("missing-marker child reached injected transport: %#v", request)
		default:
		}
	})

	newAuthorizedChild := func() *exec.Cmd {
		command := exec.Command(taggedBinary,
			productMetricsPrivateUploaderSentinelFixture,
			attemptToken,
		)
		command.Dir = workingDir
		command.Env = append(slices.Clone(baseEnvironment),
			"GC_PRODUCT_METRICS_PRIVATE_UPLOADER=1",
			taggedProductMetricsDiagnosticFDEnvironment+"="+taggedProductMetricsDiagnosticFDEnvironmentValue,
		)
		return command
	}

	abortAcknowledgement.Store(true)
	transient := runProductMetricsPrivateChildWithDiagnostic(t, newAuthorizedChild())
	var transientExit *exec.ExitError
	if !errors.As(transient.waitErr, &transientExit) || transientExit.ExitCode() != 1 {
		t.Fatalf("aborted acknowledgement child error = %v, want exit status 1", transient.waitErr)
	}
	if len(transient.output) != 0 {
		t.Fatalf("aborted acknowledgement child wrote normal output: %q", transient.output)
	}
	if transient.diagnosticErr != nil ||
		string(transient.diagnostic) != "productmetrics: upload request failed\n" {
		t.Fatalf("aborted acknowledgement diagnostic = %q, %v; want bounded upload failure", transient.diagnostic, transient.diagnosticErr)
	}
	var transientRequest capturedProductMetricsRequest
	select {
	case transientRequest = <-requests:
	case <-time.After(testutil.ExecRaceTimeout):
		t.Fatal("aborted acknowledgement child made no injected upload request")
	}
	if transientRequest.err != nil || transientRequest.method != http.MethodPost ||
		transientRequest.path != "/v1/command-usage" {
		t.Fatalf("aborted acknowledgement upload = %#v", transientRequest)
	}
	if _, err := os.Stat(queuedPath); err != nil {
		t.Fatalf("aborted acknowledgement did not restore queued event: %v", err)
	}
	if _, err := os.Stat(inflightPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("aborted acknowledgement retained inflight event: %v", err)
	}

	abortAcknowledgement.Store(false)
	valid := runProductMetricsPrivateChildWithDiagnostic(t, newAuthorizedChild())
	if valid.waitErr != nil || len(valid.output) != 0 {
		requestSummary := "not observed"
		select {
		case observed := <-requests:
			requestSummary = fmt.Sprintf("observed method=%q path=%q decode=%v", observed.method, observed.path, observed.err)
		default:
		}
		t.Fatalf("valid private child = %v, output %q, diagnostic %q/%v, request %s, queue=%s, inflight=%s; want silent success",
			valid.waitErr,
			valid.output,
			valid.diagnostic,
			valid.diagnosticErr,
			requestSummary,
			productMetricsProcessPathState(queuedPath),
			productMetricsProcessPathState(inflightPath),
		)
	}
	if valid.diagnosticErr != nil || len(valid.diagnostic) != 0 {
		t.Fatalf("successful private child diagnostic = %q, %v; want none", valid.diagnostic, valid.diagnosticErr)
	}
	var captured capturedProductMetricsRequest
	select {
	case captured = <-requests:
	case <-time.After(testutil.ExecRaceTimeout):
		t.Fatal("tagged private child made no injected upload request")
	}
	if captured.err != nil || captured.method != http.MethodPost || captured.path != "/v1/command-usage" ||
		captured.contentType != "application/json" || captured.accept != "application/json" ||
		captured.userAgent != "gascity-product-metrics/1" || captured.acceptEncoding != "" ||
		captured.authorization != "" || captured.cookie != "" || captured.proxyAuthorization != "" ||
		len(captured.batch.Events) != 1 || captured.batch.Events[0] != queuedEvent {
		t.Fatalf("captured upload = %#v", captured)
	}
	if _, err := os.Stat(queuedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("accepted event still queued: %v", err)
	}
	replacementToken := "123e4567-e89b-42d3-a456-426614174000"
	if err := os.WriteFile(filepath.Join(privateHome, "product-usage", "spawn-throttle"), []byte(fmt.Sprintf(
		"throttle_schema = 1\nattempt_token = %q\nattempted_at = %q\n",
		replacementToken, time.Now().UTC().Format(time.RFC3339Nano),
	)), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := exec.Command(taggedBinary, productMetricsPrivateUploaderSentinelFixture, attemptToken)
	stale.Dir = workingDir
	stale.Env = slices.Clone(baseEnvironment)
	stale.Env = append(stale.Env, "GC_PRODUCT_METRICS_PRIVATE_UPLOADER=1")
	if output, err := stale.CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatalf("stale private child = %v, output %q; want silent success", err, output)
	}
	select {
	case extra := <-requests:
		t.Fatalf("stale private child reached injected transport: %#v", extra)
	default:
	}

	malformed := exec.Command(taggedBinary, productMetricsPrivateUploaderSentinelFixture, "not-a-uuid", "version")
	malformed.Dir = workingDir
	malformed.Env = baseEnvironment
	output, err := malformed.CombinedOutput()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() == 0 {
		t.Fatalf("malformed private child error = %v, want nonzero exit", err)
	}
	if len(output) != 0 {
		t.Fatalf("malformed private child reached normal output: %q", output)
	}
	select {
	case extra := <-requests:
		t.Fatalf("malformed private child reached injected transport: %#v", extra)
	default:
	}
}

type productMetricsPrivateChildResult struct {
	waitErr       error
	output        []byte
	diagnostic    []byte
	diagnosticErr error
}

func runProductMetricsPrivateChildWithDiagnostic(t *testing.T, command *exec.Cmd) productMetricsPrivateChildResult {
	t.Helper()
	diagnosticReader, diagnosticWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	command.ExtraFiles = []*os.File{diagnosticWriter}
	var processOutput bytes.Buffer
	command.Stdout = &processOutput
	command.Stderr = &processOutput
	startErr := command.Start()
	parentCloseErr := diagnosticWriter.Close()
	if startErr != nil {
		_ = diagnosticReader.Close()
		t.Fatalf("start private child: %v", startErr)
	}
	waitErr := command.Wait()
	diagnostic, diagnosticErr := io.ReadAll(io.LimitReader(
		diagnosticReader,
		taggedProductMetricsMaximumDiagnosticBytes+1,
	))
	diagnosticErr = errors.Join(diagnosticErr, diagnosticReader.Close(), parentCloseErr)
	return productMetricsPrivateChildResult{
		waitErr:       waitErr,
		output:        processOutput.Bytes(),
		diagnostic:    diagnostic,
		diagnosticErr: diagnosticErr,
	}
}

func productMetricsProcessPathState(path string) string {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return "present"
	case errors.Is(err, os.ErrNotExist):
		return "absent"
	default:
		return err.Error()
	}
}

func configureProductMetricsTrustedProcessTempRoot(t *testing.T) {
	t.Helper()
	trustedTempRoot := "/tmp"
	if runtime.GOOS == "darwin" {
		trustedTempRoot = "/private/tmp"
	}
	// Go 1.26's testing.T.TempDir prefers GOTMPDIR over TMPDIR. Product
	// metrics deliberately reject a user-owned writable ancestor, so keep
	// these process trust-boundary fixtures below the root-owned sticky
	// directory even when repository build scratch lives below /data.
	t.Setenv("GOTMPDIR", trustedTempRoot)
	t.Setenv("TMPDIR", trustedTempRoot)
}

func replaceProductMetricsProcessEnvironment(environment []string, name, value string) []string {
	prefix := name + "="
	replaced := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			replaced = append(replaced, entry)
		}
	}
	return append(replaced, prefix+value)
}

func seedPrivateUploaderProcessFixture(t *testing.T, home, attemptToken string, now time.Time) productmetrics.Event {
	t.Helper()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatalf("make product-metrics test home private: %v", err)
	}
	root := filepath.Join(home, "product-usage")
	queue := filepath.Join(root, "queue", productMetricsTestSpoolGeneration)
	if err := os.MkdirAll(queue, 0o700); err != nil {
		t.Fatal(err)
	}
	event := productmetrics.Event{
		EventID:         productMetricsTestEventID,
		InstallationID:  productMetricsTestInstallationID,
		App:             productmetrics.AppGasCity,
		ReleaseVersion:  productMetricsTestReleaseVersion,
		OS:              productmetrics.OperatingSystem(runtime.GOOS),
		OccurredHourUTC: now.UTC().Truncate(time.Hour).Format(time.RFC3339),
		CommandID:       productmetrics.CommandHelp,
	}
	eventBytes, err := productmetrics.EncodeEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		filepath.Join(root, "config.toml"): []byte(fmt.Sprintf(
			"state_schema = 1\ncounter_namespace = 1\nstate_generation = 1\npreference = \"enabled\"\n"+
				"required_notice_version = 1\naccepted_notice_version = 1\ninstallation_id = %q\n"+
				"spool_generation = %q\ncleanup_kind = \"none\"\ncleanup_epoch = 0\npaused_through_metrics_epoch = 0\n",
			productMetricsTestInstallationID, productMetricsTestSpoolGeneration,
		)),
		filepath.Join(root, "quota.toml"): []byte(fmt.Sprintf(
			"quota_schema = 1\nreserved_events = 1\nreserved_bytes = %d\n", len(eventBytes),
		)),
		filepath.Join(root, "spawn-throttle"): []byte(fmt.Sprintf(
			"throttle_schema = 1\nattempt_token = %q\nattempted_at = %q\n",
			attemptToken, now.UTC().Format(time.RFC3339Nano),
		)),
		filepath.Join(queue, productMetricsTestEventID+".json"): eventBytes,
	}
	for path, contents := range files {
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatalf("write product-metrics process fixture %s: %v", path, err)
		}
	}
	return event
}
