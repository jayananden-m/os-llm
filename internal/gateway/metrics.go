package gateway

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type metrics struct {
	requests  *prometheus.CounterVec
	duration  *prometheus.HistogramVec
	queueWait *prometheus.HistogramVec
	rejected  *prometheus.CounterVec
}

func newMetrics(reg prometheus.Registerer, g *Gateway) *metrics {
	m := &metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "osllm_gateway_requests_total",
			Help: "Inference requests by model and HTTP status.",
		}, []string{"model", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "osllm_gateway_request_duration_seconds",
			Help:    "End-to-end request duration, queue wait included.",
			Buckets: prometheus.ExponentialBuckets(0.05, 2, 12), // 50ms to ~100s
		}, []string{"model"}),
		queueWait: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "osllm_gateway_queue_wait_seconds",
			Help:    "Time spent waiting for a free replica slot.",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 16), // 1ms to ~30s
		}, []string{"model"}),
		rejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "osllm_gateway_rejected_total",
			Help: "Requests rejected by admission control, by reason.",
		}, []string{"model", "reason"}),
	}
	reg.MustRegister(m.requests, m.duration, m.queueWait, m.rejected, newPoolCollector(g))
	return m
}

func (m *metrics) observe(model string, status int, elapsed time.Duration) {
	if status == 0 {
		status = 499 // client went away before any response was written
	}
	m.requests.WithLabelValues(model, strconv.Itoa(status)).Inc()
	m.duration.WithLabelValues(model).Observe(elapsed.Seconds())
}

// poolCollector reads live pool state at scrape time, so the gauges can never
// drift from the counters the pool actually uses.
type poolCollector struct {
	g          *Gateway
	queueDepth *prometheus.Desc
	inFlight   *prometheus.Desc
	capacity   *prometheus.Desc
}

func newPoolCollector(g *Gateway) *poolCollector {
	return &poolCollector{
		g: g,
		queueDepth: prometheus.NewDesc("osllm_gateway_queue_depth",
			"Requests waiting for a replica slot.", []string{"model"}, nil),
		inFlight: prometheus.NewDesc("osllm_gateway_replica_in_flight",
			"Requests currently running on a replica.", []string{"model", "replica"}, nil),
		capacity: prometheus.NewDesc("osllm_gateway_capacity_slots",
			"Total concurrent slots across a model's replicas.", []string{"model"}, nil),
	}
}

func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.queueDepth
	ch <- c.inFlight
	ch <- c.capacity
}

func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	for model, p := range c.g.poolsSnapshot() {
		reps, queued, capacity := p.Snapshot()
		ch <- prometheus.MustNewConstMetric(c.queueDepth, prometheus.GaugeValue, float64(queued), model)
		ch <- prometheus.MustNewConstMetric(c.capacity, prometheus.GaugeValue, float64(capacity), model)
		for _, r := range reps {
			ch <- prometheus.MustNewConstMetric(c.inFlight, prometheus.GaugeValue, float64(r.InFlight), model, r.Addr)
		}
	}
}

// statusRecorder remembers the status code while passing everything through.
// Flush and Unwrap keep streaming working through the wrapper.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 && code >= 200 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
