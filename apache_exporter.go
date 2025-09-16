// Copyright (c) 2015 neezgee
//
// Licensed under the MIT license: https://opensource.org/licenses/MIT
// Permission is granted to use, copy, modify, and redistribute the work.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/client_golang/prometheus"
	versioncollector "github.com/prometheus/client_golang/prometheus/collectors/version"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/common/promslog/flag"
	"github.com/prometheus/common/version"
	"github.com/prometheus/exporter-toolkit/web"
	"github.com/prometheus/exporter-toolkit/web/kingpinflag"

	"github.com/Lusitaniae/apache_exporter/collector"
)

var (
	metricsEndpoint = kingpin.Flag("telemetry.endpoint", "Path under which to expose metrics.").Default("/metrics").String()
	hostOverride    = kingpin.Flag("host_override", "Override for HTTP Host header; empty string for no override.").Default("").String()
	insecure        = kingpin.Flag("insecure", "Ignore server certificate if using https.").Bool()
	toolkitFlags    = kingpinflag.AddFlags(kingpin.CommandLine, ":9117")
	customHeaders   = kingpin.Flag("custom_headers", "Adds custom headers to the collector.").StringMap()
)

func main() {
	promslogConfig := &promslog.Config{}

	// Parse flags
	flag.AddFlags(kingpin.CommandLine, promslogConfig)
	kingpin.HelpFlag.Short('h')
	kingpin.Version(version.Print("apache_exporter"))
	kingpin.Parse()
	logger := promslog.New(promslogConfig)

	prometheus.MustRegister(versioncollector.NewCollector("apache_exporter"))

	logger.Info("Starting apache_exporter", "version", version.Info())
	logger.Info("Build context", "build", version.BuildContext())
	logger.Info("Apache Exporter is running in multi-target mode. Scrape targets with /metrics?target=http://your-apache/server-status?auto")

	// The handler for multi-target scraping.
	http.HandleFunc(*metricsEndpoint, func(w http.ResponseWriter, r *http.Request) {
		handleMetricsScrape(w, r, logger)
	})

	// Default handler for scrapes of the exporter itself (e.g., process metrics).
	http.Handle("/exporter-metrics", promhttp.Handler())

	landingConfig := web.LandingConfig{
		Name:        "Apache Exporter",
		Description: "Prometheus exporter for Apache HTTP server metrics",
		Version:     version.Info(),
		Links: []web.LandingLinks{
			{
				Address: *metricsEndpoint,
				Text:    "Metrics",
			},
			{
				Address: "/exporter-metrics",
				Text:    "Exporter Metrics",
			},
		},
	}
	landingPage, err := web.NewLandingPage(landingConfig)
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
	http.Handle("/", landingPage)

	srv := &http.Server{}
	srvc := make(chan struct{})
	term := make(chan os.Signal, 1)
	signal.Notify(term, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := web.ListenAndServe(srv, toolkitFlags, logger); err != http.ErrServerClosed {
			logger.Error("Error starting HTTP server", "err", err)
			close(srvc)
		}
	}()

	for {
		select {
		case <-term:
			logger.Info("Received SIGTERM, exiting gracefully...")
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := srv.Shutdown(ctx); err != nil {
				logger.Error("Error shutting down server", "err", err)
			}
			os.Exit(0)
		case <-srvc:
			os.Exit(1)
		}
	}
}

func handleMetricsScrape(w http.ResponseWriter, r *http.Request, logger *slog.Logger) {
	target := r.URL.Query().Get("target")
	if target == "" {
		http.Error(w, "URL parameter 'target' is missing", http.StatusBadRequest)
		return
	}

	config := &collector.Config{
		ScrapeURI:     target,
		HostOverride:  *hostOverride,
		Insecure:      *insecure,
		CustomHeaders: *customHeaders,
	}

	// Create a new registry for this specific scrape.
	registry := prometheus.NewRegistry()

	// Create a new exporter with the target-specific config.
	exporter := collector.NewExporter(logger, config)

	// Register the new exporter.
	registry.MustRegister(exporter)

	// Delegate the serving of metrics to the Prometheus handler.
	// The handler will scrape the exporter and write the metrics to the response.
	h := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	h.ServeHTTP(w, r)
}
