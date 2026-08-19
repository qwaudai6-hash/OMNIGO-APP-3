package telemetry

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	metricsOnce sync.Once

	PaymentAttemptsTotal *prometheus.CounterVec
	PaymentSuccessTotal  *prometheus.CounterVec
	PaymentFailureTotal  *prometheus.CounterVec
	GatewayLatency       *prometheus.HistogramVec
	FraudBlocksTotal     *prometheus.CounterVec
	CircuitBreakerGauge  *prometheus.GaugeVec
)

// InitPaymentMetrics registers all payment telemetry counters and histograms.
func InitPaymentMetrics() {
	metricsOnce.Do(func() {
		PaymentAttemptsTotal = promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "omnigo_payment_attempts_total",
				Help: "Total number of payment checkout attempts initiated",
			},
			[]string{"gateway", "method"},
		)

		PaymentSuccessTotal = promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "omnigo_payment_success_total",
				Help: "Total number of successfully captured and settled payments",
			},
			[]string{"gateway"},
		)

		PaymentFailureTotal = promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "omnigo_payment_failure_total",
				Help: "Total number of failed or rejected payment attempts",
			},
			[]string{"gateway", "code"},
		)

		GatewayLatency = promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "omnigo_gateway_latency_seconds",
				Help:    "Latency histogram of payment gateway HTTP requests",
				Buckets: []float64{0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 20.0},
			},
			[]string{"gateway", "endpoint"},
		)

		FraudBlocksTotal = promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "omnigo_fraud_blocks_total",
				Help: "Total number of payments blocked by pre-authorization fraud checks",
			},
			[]string{"reason"},
		)

		CircuitBreakerGauge = promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "omnigo_circuit_breaker_state",
				Help: "Operating state of payment circuit breakers (0=Closed, 1=HalfOpen, 2=Open)",
			},
			[]string{"gateway"},
		)
	})
}

// RecordPaymentAttempt tracks an initiated checkout.
func RecordPaymentAttempt(gateway, method string) {
	InitPaymentMetrics()
	PaymentAttemptsTotal.WithLabelValues(gateway, method).Inc()
}

// RecordPaymentSuccess tracks a successful checkout.
func RecordPaymentSuccess(gateway string) {
	InitPaymentMetrics()
	PaymentSuccessTotal.WithLabelValues(gateway).Inc()
}

// RecordPaymentFailure tracks a failed checkout.
func RecordPaymentFailure(gateway, code string) {
	InitPaymentMetrics()
	PaymentFailureTotal.WithLabelValues(gateway, code).Inc()
}

// ObserveGatewayLatency tracks network duration to payment gateway.
func ObserveGatewayLatency(gateway, endpoint string, duration time.Duration) {
	InitPaymentMetrics()
	GatewayLatency.WithLabelValues(gateway, endpoint).Observe(duration.Seconds())
}

// RecordFraudBlock tracks a blocked suspicious attempt.
func RecordFraudBlock(reason string) {
	InitPaymentMetrics()
	FraudBlocksTotal.WithLabelValues(reason).Inc()
}

// SetCircuitBreakerState updates gauge for gateway state.
func SetCircuitBreakerState(gateway string, state float64) {
	InitPaymentMetrics()
	CircuitBreakerGauge.WithLabelValues(gateway).Set(state)
}
