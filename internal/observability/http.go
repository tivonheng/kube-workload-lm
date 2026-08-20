package observability

import (
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func NewHTTPHandler(readiness *Readiness, metrics *Metrics, gatherer prometheus.Gatherer) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("{\"status\":\"ok\"}\n"))
	})
	mux.HandleFunc("/readyz", func(writer http.ResponseWriter, _ *http.Request) {
		policyReady := readiness.PolicyReady()
		ready := readiness.Ready()
		leader := readiness.Leading()
		metrics.SetReadiness(policyReady, ready)
		metrics.SetLeader(leader)
		writer.Header().Set("Content-Type", "application/json")
		role := "standby"
		if leader {
			role = "leader"
		}
		if !ready {
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(writer, "{\"status\":\"not-ready\",\"role\":%q}\n", role)
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(writer, "{\"status\":\"ready\",\"role\":%q}\n", role)
	})
	mux.Handle("/metrics", promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}))
	return mux
}
