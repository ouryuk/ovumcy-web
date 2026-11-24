package services

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// addressAwareDial fails for specific addresses and hands out a conn for the
// rest, recording every address it is asked to dial in order.
type addressAwareDial struct {
	failFor map[string]error
	conn    net.Conn
	dialed  []string
}

func (stub *addressAwareDial) dial(_ context.Context, _, addr string) (net.Conn, error) {
	stub.dialed = append(stub.dialed, addr)
	if err, fail := stub.failFor[addr]; fail {
		return nil, err
	}
	return stub.conn, nil
}

// TestGuardedDialContextFallsThroughToTheNextResolvedAddress pins the
// happy-path retry inside the guarded dialer: when a hostname resolves to
// several validated public addresses and the first refuses the connection,
// the dialer must try the next one and return ITS conn — a single dead IP in a
// round-robin answer must not fail the reminder. Every address dialed must be
// one the resolver returned (never the hostname), and all of them are checked
// for privacy before the first dial.
func TestGuardedDialContextFallsThroughToTheNextResolvedAddress(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	stub := &addressAwareDial{
		failFor: map[string]error{"8.8.8.8:443": errors.New("connection refused")},
		conn:    client,
	}
	dialFn := guardedDialContext(stub.dial, resolverFor("ntfy.example.io", "8.8.8.8", "1.0.0.1"))

	conn, err := dialFn(context.Background(), "tcp", "ntfy.example.io:443")
	if err != nil {
		t.Fatalf("expected the second resolved address to be dialed, got %v", err)
	}
	if conn != client {
		t.Fatal("guarded dialer did not return the conn of the address that succeeded")
	}
	if len(stub.dialed) != 2 || stub.dialed[0] != "8.8.8.8:443" || stub.dialed[1] != "1.0.0.1:443" {
		t.Fatalf("expected [8.8.8.8:443 1.0.0.1:443] dialed in resolver order, got %v", stub.dialed)
	}
}

// TestGuardedDialContextSurfacesTheLastErrorWhenEveryAddressFails pins the
// all-fail side of the same loop: each resolved address is tried exactly once
// (no retry storm against one endpoint) and the error returned is the LAST
// dial error, which is the one that describes the final attempt.
func TestGuardedDialContextSurfacesTheLastErrorWhenEveryAddressFails(t *testing.T) {
	first := errors.New("connection refused")
	last := errors.New("connection timed out")
	stub := &addressAwareDial{failFor: map[string]error{"8.8.8.8:443": first, "1.0.0.1:443": last}}
	dialFn := guardedDialContext(stub.dial, resolverFor("ntfy.example.io", "8.8.8.8", "1.0.0.1"))

	conn, err := dialFn(context.Background(), "tcp", "ntfy.example.io:443")
	if conn != nil {
		_ = conn.Close()
		t.Fatal("expected no conn when every address fails")
	}
	if !errors.Is(err, last) {
		t.Fatalf("expected the last dial error to surface, got %v", err)
	}
	if errors.Is(err, first) {
		t.Fatalf("the first dial error must not be the one surfaced: %v", err)
	}
	if len(stub.dialed) != 2 {
		t.Fatalf("expected each address tried exactly once, got %v", stub.dialed)
	}
}

// TestGuardedDialContextChecksEveryAddressForPrivacyBeforeTheFirstDial pins
// the ordering of the two loops: the privacy sweep is complete BEFORE any dial
// begins, so a round-robin answer mixing a public and a private address is
// refused without the public one ever being contacted.
func TestGuardedDialContextChecksEveryAddressForPrivacyBeforeTheFirstDial(t *testing.T) {
	stub := &addressAwareDial{}
	dialFn := guardedDialContext(stub.dial, resolverFor("mixed.example", "8.8.8.8", "10.0.0.1"))

	_, err := dialFn(context.Background(), "tcp", "mixed.example:443")
	if !errors.Is(err, errWebhookPrivateAddress) {
		t.Fatalf("expected private-address refusal, got %v", err)
	}
	if len(stub.dialed) != 0 {
		t.Fatalf("no address may be dialed once any resolved address is private, got %v", stub.dialed)
	}
}

// TestWebhookDeliveryClassifiesTheRefusedRedirect pins the reason the deliverer
// logs and returns for a redirect: the transport error wraps errWebhookRedirect,
// and the classification switch must name it "redirect_refused" rather than the
// generic "transport_error", so an operator reading the log can tell a
// misconfigured endpoint (pointing at a redirect) from an outage.
func TestWebhookDeliveryClassifiesTheRefusedRedirect(t *testing.T) {
	var secondaryHit atomic.Bool
	secondary := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		secondaryHit.Store(true)
		writer.WriteHeader(http.StatusOK)
	}))
	defer secondary.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Redirect(writer, &http.Request{}, secondary.URL, http.StatusFound)
	}))
	defer redirector.Close()

	deliverer := NewWebhookDeliverer(false)
	var err error
	output := captureLogOutput(t, func() {
		err = deliverer.Deliver(context.Background(), redirector.URL, samplePayload())
	})
	if err == nil {
		t.Fatal("expected a redirect to be refused")
	}
	if secondaryHit.Load() {
		t.Fatal("the redirect target must not be followed")
	}
	if !strings.Contains(err.Error(), "redirect_refused") {
		t.Fatalf("returned error should name the redirect_refused reason, got %q", err.Error())
	}
	if strings.Contains(err.Error(), "transport_error") {
		t.Fatalf("a refused redirect must not be logged as a generic transport error: %q", err.Error())
	}
	if !strings.Contains(output, "reason=redirect_refused") {
		t.Fatalf("expected the redirect_refused reason in the log, got %q", output)
	}
}

// TestWebhookDeliveryClassifiesTheBlockedPrivateAddressFromTheDialer pins the
// third arm of the same switch: when the private-address refusal comes from the
// guarded DIALER (a hostname resolving to a private IP — the Deliver-level
// literal check cannot see it), the transport error is classified
// "private_address_blocked", not "transport_error".
func TestWebhookDeliveryClassifiesTheBlockedPrivateAddressFromTheDialer(t *testing.T) {
	const hostname = "internal.example"
	deliverer := newWebhookDelivererWithResolver(true, resolverFor(hostname, "192.168.1.10"))

	var err error
	output := captureLogOutput(t, func() {
		err = deliverer.Deliver(context.Background(), "https://"+hostname+"/hook", samplePayload())
	})
	if err == nil {
		t.Fatal("expected a hostname resolving to a private address to be refused when gated on")
	}
	if !strings.Contains(err.Error(), "private_address_blocked") {
		t.Fatalf("returned error should name the private_address_blocked reason, got %q", err.Error())
	}
	if strings.Contains(err.Error(), "transport_error") {
		t.Fatalf("a blocked private address must not be logged as a generic transport error: %q", err.Error())
	}
	if !strings.Contains(output, "reason=private_address_blocked host="+hostname) {
		t.Fatalf("expected the private_address_blocked reason with the host in the log, got %q", output)
	}
	if strings.Contains(output, "192.168.1.10") {
		t.Fatalf("log leaked the resolved IP: %q", output)
	}
}

// TestWebhookDeliveryReportsAPlainTransportErrorWhenNothingMoreSpecificApplies
// is the default arm: an outage (connection refused, nothing to do with
// redirects or private addresses) is reported as "transport_error" with the
// host, and never as one of the specific reasons.
func TestWebhookDeliveryReportsAPlainTransportErrorWhenNothingMoreSpecificApplies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	target := server.URL
	server.Close() // the port is now closed: the connection is refused

	deliverer := NewWebhookDeliverer(false)
	var err error
	output := captureLogOutput(t, func() {
		err = deliverer.Deliver(context.Background(), target, samplePayload())
	})
	if err == nil {
		t.Fatal("expected the closed endpoint to fail")
	}
	if !strings.Contains(err.Error(), "transport_error") {
		t.Fatalf("returned error should name the transport_error reason, got %q", err.Error())
	}
	for _, specific := range []string{"redirect_refused", "private_address_blocked"} {
		if strings.Contains(err.Error(), specific) || strings.Contains(output, specific) {
			t.Fatalf("an outage must not be classified as %q: err=%q log=%q", specific, err.Error(), output)
		}
	}
	if !strings.Contains(output, "reason=transport_error host=") {
		t.Fatalf("expected the transport_error reason with the host in the log, got %q", output)
	}
}
