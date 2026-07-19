package warmup

import (
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/doctor"
)

type customWarmupMailCheck struct {
	stubWarmupCheck
	subject        string
	body           string
	calls          int
	receivedReport WarmupReport
}

func (c *customWarmupMailCheck) SoleFailureMail(report WarmupReport) (string, string) {
	c.calls++
	c.receivedReport = report
	return c.subject, c.body
}

func TestTryCustomSoleFailureMail_ZeroFailuresReturnsFalse(t *testing.T) {
	custom := &customWarmupMailCheck{
		stubWarmupCheck: stubWarmupCheck{name: "custom", warmup: true},
		subject:         "custom subject",
		body:            "custom body",
	}

	subject, body, ok := tryCustomSoleFailureMail(WarmupReport{}, []doctor.Check{custom})

	if ok {
		t.Fatal("ok = true, want false")
	}
	if subject != "" || body != "" {
		t.Fatalf("subject/body = %q/%q, want empty", subject, body)
	}
	if custom.calls != 0 {
		t.Fatalf("SoleFailureMail calls = %d, want 0", custom.calls)
	}
}

func TestRunWarmupChecks_CustomWarmupMail_MultipleDistinctCheckNamesUseDefaultMail(t *testing.T) {
	custom := &customWarmupMailCheck{
		stubWarmupCheck: stubWarmupCheck{
			name:            "custom",
			warmup:          true,
			returnedStatus:  doctor.StatusError,
			returnedMessage: "custom failed",
		},
		subject: "custom subject",
		body:    "custom body",
	}
	checks := []doctor.Check{
		custom,
		stubWarmupCheck{
			name:            "other",
			warmup:          true,
			returnedStatus:  doctor.StatusWarning,
			returnedMessage: "other warned",
		},
	}

	_, mailer, _ := runWarmupTest(t, checks, WarmupOpts{})

	if custom.calls != 0 {
		t.Fatalf("SoleFailureMail calls = %d, want 0", custom.calls)
	}
	if len(mailer.sent) != 1 {
		t.Fatalf("sent mail count = %d, want 1", len(mailer.sent))
	}
	if got, want := mailer.sent[0].Subject, "city warm-up: 2 doctor check(s) failed"; got != want {
		t.Fatalf("subject = %q, want %q", got, want)
	}
	if mailer.sent[0].Body == "custom body" {
		t.Fatal("body used custom body, want default warmup body")
	}
	if !strings.Contains(mailer.sent[0].Body, "custom failed") || !strings.Contains(mailer.sent[0].Body, "other warned") {
		t.Fatalf("body = %q, want default body with both failures", mailer.sent[0].Body)
	}
}

func TestTryCustomSoleFailureMail_SingleDistinctCheckNameLooksUpMatchingCheck(t *testing.T) {
	unmatched := &customWarmupMailCheck{
		stubWarmupCheck: stubWarmupCheck{name: "unmatched", warmup: true},
		subject:         "wrong subject",
		body:            "wrong body",
	}
	matching := &customWarmupMailCheck{
		stubWarmupCheck: stubWarmupCheck{name: "matching", warmup: true},
		subject:         "matching subject",
		body:            "matching body",
	}
	report := WarmupReport{
		Failures: []WarmupCheckResult{
			{Scope: "city", Check: "matching", Status: doctor.StatusError, Message: "first"},
			{Scope: "rig", Check: "matching", Status: doctor.StatusWarning, Message: "second"},
		},
	}

	subject, body, ok := tryCustomSoleFailureMail(report, []doctor.Check{unmatched, nil, matching})

	if !ok {
		t.Fatal("ok = false, want true")
	}
	if subject != "matching subject" || body != "matching body" {
		t.Fatalf("subject/body = %q/%q, want matching custom mail", subject, body)
	}
	if unmatched.calls != 0 {
		t.Fatalf("unmatched SoleFailureMail calls = %d, want 0", unmatched.calls)
	}
	if matching.calls != 1 {
		t.Fatalf("matching SoleFailureMail calls = %d, want 1", matching.calls)
	}
	if len(matching.receivedReport.Failures) != 2 {
		t.Fatalf("received failures = %d, want 2", len(matching.receivedReport.Failures))
	}
}

func TestRunWarmupChecks_CustomWarmupMail_TypeAssertionMissUsesDefaultMail(t *testing.T) {
	checks := []doctor.Check{
		stubWarmupCheck{
			name:            "plain",
			warmup:          true,
			returnedStatus:  doctor.StatusError,
			returnedMessage: "plain failed",
		},
	}

	_, mailer, _ := runWarmupTest(t, checks, WarmupOpts{})

	if len(mailer.sent) != 1 {
		t.Fatalf("sent mail count = %d, want 1", len(mailer.sent))
	}
	if got, want := mailer.sent[0].Subject, "plain alert during city warm-up"; got != want {
		t.Fatalf("subject = %q, want %q", got, want)
	}
	if !strings.Contains(mailer.sent[0].Body, "plain failed") {
		t.Fatalf("body = %q, want default body with failure message", mailer.sent[0].Body)
	}
}

func TestRunWarmupChecks_CustomWarmupMail_TypeAssertionHitUsesCustomMail(t *testing.T) {
	custom := &customWarmupMailCheck{
		stubWarmupCheck: stubWarmupCheck{
			name:            "custom",
			warmup:          true,
			returnedStatus:  doctor.StatusError,
			returnedMessage: "default failure text",
		},
		subject: "custom subject",
		body:    "custom body",
	}

	_, mailer, _ := runWarmupTest(t, []doctor.Check{custom}, WarmupOpts{})

	if custom.calls != 1 {
		t.Fatalf("SoleFailureMail calls = %d, want 1", custom.calls)
	}
	if len(mailer.sent) != 1 {
		t.Fatalf("sent mail count = %d, want 1", len(mailer.sent))
	}
	if got := mailer.sent[0].Subject; got != "custom subject" {
		t.Fatalf("subject = %q, want custom subject", got)
	}
	if got := mailer.sent[0].Body; got != "custom body" {
		t.Fatalf("body = %q, want custom body", got)
	}
}
