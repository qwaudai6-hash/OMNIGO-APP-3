package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	paymentAttempts = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "payfast_payment_attempts_total",
		Help: "Total number of payment attempts initiated, labeled by gateway and payment method.",
	}, []string{"gateway", "method"})

	paymentOutcomes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "payfast_payment_outcomes_total",
		Help: "Total number of payment attempts resolved, labeled by method and final outcome (succeeded/failed/gateway_pending/three_ds_required).",
	}, []string{"method", "outcome"})

	fraudBlocks = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "payfast_fraud_blocks_total",
		Help: "Total number of payment attempts blocked by pre-authorization fraud/velocity checks, labeled by reason.",
	}, []string{"reason"})

	gatewayCallDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "payfast_gateway_call_duration_seconds",
		Help: "Duration of outbound calls to the PayFast gateway API, labeled by endpoint and outcome.",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 15, 20, 25},
	}, []string{"endpoint", "outcome"})

	threeDSCallbackDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "payfast_3ds_callback_duration_seconds",
		Help: "End-to-end duration of processing a 3DS callback, from receipt to final settlement decision, labeled by outcome.",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 15, 20, 30},
	}, []string{"outcome"})

	circuitBreakerState = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "payfast_circuit_breaker_state",
		Help: "Current circuit breaker state per gateway (0=CLOSED, 1=HALF_OPEN, 2=OPEN).",
	}, []string{"gateway"})

	circuitBreakerTransitions = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "payfast_circuit_breaker_transitions_total",
		Help: "Total number of circuit breaker state transitions, labeled by from-state and to-state.",
	}, []string{"from", "to"})

	reconciliationOutcomes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "payfast_reconciliation_outcomes_total",
		Help: "Total number of stuck-payment reconciliation attempts, labeled by outcome (settled/failed/still_pending/timeout).",
	}, []string{"outcome"})

	settlementDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "payfast_outbox_settlement_duration_seconds",
		Help: "Duration of processing a single outbox settlement event, labeled by outcome.",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10},
	}, []string{"outcome"})

	settlementOutcomes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "payfast_outbox_settlement_outcomes_total",
		Help: "Total number of outbox settlement events processed, labeled by outcome (settled/failed/retried).",
	}, []string{"outcome"})

	ipnReceived = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "payfast_ipn_received_total",
		Help: "Total number of IPN (Instant Payment Notification) callbacks received on checkout_url, labeled by whether hash verification succeeded.",
	}, []string{"verified"})
)

// RecordPaymentAttempt records that a payment attempt was initiated for the given gateway and method.
func RecordPaymentAttempt(gateway, method string) {
	paymentAttempts.WithLabelValues(gateway, method).Inc()
}

// RecordPaymentOutcome records the final resolved outcome of a payment attempt.
func RecordPaymentOutcome(method, outcome string) {
	paymentOutcomes.WithLabelValues(method, outcome).Inc()
}

// RecordFraudBlock records that a payment attempt was blocked by a fraud/velocity check.
func RecordFraudBlock(reason string) {
	fraudBlocks.WithLabelValues(reason).Inc()
}

// ObserveGatewayCallDuration records how long an outbound PayFast API call took.
func ObserveGatewayCallDuration(endpoint, outcome string, duration time.Duration) {
	gatewayCallDuration.WithLabelValues(endpoint, outcome).Observe(duration.Seconds())
}

// TimeGatewayCall is a convenience helper: call it with defer to automatically time and classify a gateway call.
func TimeGatewayCall(endpoint string, start time.Time, errPtr *error) {
	outcome := "success"
	if errPtr != nil && *errPtr != nil {
		if IsTimeoutError(*errPtr) {
			outcome = "timeout"
		} else {
			outcome = "error"
		}
	}
	ObserveGatewayCallDuration(endpoint, outcome, time.Since(start))
}

// IsTimeoutError does a best-effort classification of context deadline / timeout errors.
func IsTimeoutError(err error) bool {
	return errors.Is(err, context.DeadlineExceeded)
}

// Observe3DSCallbackDuration records how long a full 3DS callback took to process.
func Observe3DSCallbackDuration(outcome string, duration time.Duration) {
	threeDSCallbackDuration.WithLabelValues(outcome).Observe(duration.Seconds())
}

// SetCircuitBreakerState records the current state of a named gateway's circuit breaker.
func SetCircuitBreakerState(gateway string, stateValue float64) {
	circuitBreakerState.WithLabelValues(gateway).Set(stateValue)
}

// RecordCircuitBreakerTransition records a circuit breaker moving from one state to another.
func RecordCircuitBreakerTransition(from, to string) {
	circuitBreakerTransitions.WithLabelValues(from, to).Inc()
}

// RecordReconciliationOutcome records the result of a single stuck-payment reconciliation attempt.
func RecordReconciliationOutcome(outcome string) {
	reconciliationOutcomes.WithLabelValues(outcome).Inc()
}

// ObserveSettlementDuration records how long a single outbox settlement event took to process.
func ObserveSettlementDuration(outcome string, duration time.Duration) {
	settlementDuration.WithLabelValues(outcome).Observe(duration.Seconds())
	settlementOutcomes.WithLabelValues(outcome).Inc()
}

// RecordIPNReceived records an incoming IPN callback, labeled by whether its hash verified.
func RecordIPNReceived(verified bool) {
	label := "false"
	if verified {
		label = "true"
	}
	ipnReceived.WithLabelValues(label).Inc()
}

// Handler returns the standard Prometheus scrape handler.
func Handler() http.Handler {
	return promhttp.Handler()
}

// StartStandaloneServer starts a dedicated HTTP server exposing ONLY /metrics.
func StartStandaloneServer(ctx context.Context) error {
	addr := os.Getenv("METRICS_LISTEN_ADDR")
	if addr == "" {
		return errors.New("METRICS_LISTEN_ADDR environment variable is required to start the standalone metrics server (e.g. \":9090\")")
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", Handler())

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("[telemetry] metrics server shutdown error: %v", err)
		}
	}()

	log.Printf("[telemetry] metrics server listening on %s/metrics", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("metrics server failed: %w", err)
	}
	return nil
}
