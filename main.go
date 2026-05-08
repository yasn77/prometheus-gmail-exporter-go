package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/iancoleman/strcase"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/time/rate"
	"google.golang.org/api/gmail/v1"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Interval int      `yaml:"interval"`
	Labels   []string `yaml:"labels"`
}

func loadConfig(path string) (Config, error) {
	var config Config
	configFile, err := os.ReadFile(path)
	if err != nil {
		return config, fmt.Errorf("could not read config file: %w", err)
	}

	err = yaml.Unmarshal(configFile, &config)
	if err != nil {
		return config, fmt.Errorf("could not parse config file: %w", err)
	}

	if config.Interval <= 0 {
		return config, fmt.Errorf("interval must be greater than 0, got %d", config.Interval)
	}
	if len(config.Labels) == 0 {
		return config, fmt.Errorf("labels must not be empty")
	}

	return config, nil
}

func matchLabels(labels []*gmail.Label, desired []string) []string {
	var labelIds []string
	for _, lab := range labels {
		for _, desiredLabel := range desired {
			if desiredLabel == "" {
				continue
			}
			if lab.Name == desiredLabel {
				labelIds = append(labelIds, lab.Id)
				break
			}
		}
	}
	return labelIds
}

func scrapeMetrics(unreadGauge *prometheus.GaugeVec, totalGauge *prometheus.GaugeVec, labelIds []string, srv *gmail.Service) error {
	for _, labelId := range labelIds {
		label, err := srv.Users.Labels.Get("me", labelId).Do()
		if err != nil {
			return fmt.Errorf("failed to get label %s: %w", labelId, err)
		}
		prometheusLabels := map[string]string{"Label": "gmail_" + strcase.ToSnake(label.Name)}
		totalGauge.With(prometheusLabels).Set(float64(label.ThreadsTotal))
		unreadGauge.With(prometheusLabels).Set(float64(label.ThreadsUnread))
	}
	return nil
}

func recordMetrics(interval int, unreadGauge *prometheus.GaugeVec, totalGauge *prometheus.GaugeVec, labelIds []string, srv *gmail.Service, stopCh <-chan struct{}, scrapeErrors prometheus.Counter, scrapeSuccess prometheus.Counter) {
	go func() {
		ticker := time.NewTicker(time.Duration(interval) * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				func() {
					defer func() {
						if r := recover(); r != nil {
							scrapeErrors.Inc()
							fmt.Printf("panic in scrapeMetrics: %v\n", r)
						}
					}()
					fmt.Printf("scraping %d labels\n", len(labelIds))
					if err := scrapeMetrics(unreadGauge, totalGauge, labelIds, srv); err != nil {
						fmt.Printf("%v\n", err)
						scrapeErrors.Inc()
					} else {
						scrapeSuccess.Inc()
					}
				}()
			case <-stopCh:
				return
			}
		}
	}()
}

var limiter = rate.NewLimiter(rate.Limit(10), 5)

func rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow() {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func readyHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Ready"))
}

func getEnvDuration(key string, defaultVal time.Duration) time.Duration {
	if val := os.Getenv(key); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			return d
		}
		log.Printf("Invalid duration for %s, using default %v", key, defaultVal)
	}
	return defaultVal
}

func newServer(registry *prometheus.Registry, addr string, readTimeout, writeTimeout, idleTimeout time.Duration) *http.Server {
	mux := http.NewServeMux()
	metricsHandler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	mux.Handle("/metrics", rateLimitMiddleware(metricsHandler))
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/ready", readyHandler)
	return &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		IdleTimeout:  idleTimeout,
	}
}

func isLocalhost(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func run(configPath, credentialsPath string, srvFn func(string) (*gmail.Service, error)) error {
	config, err := loadConfig(configPath)
	if err != nil {
		return err
	}

	srv, err := srvFn(credentialsPath)
	if err != nil {
		return err
	}

	labels, err := getLabels(srv)
	if err != nil {
		return err
	}

	log.Printf("Discovered %d Gmail labels:", len(labels))
	for _, lab := range labels {
		log.Printf("  - %s (id=%s)", lab.Name, lab.Id)
	}

	labelIds := matchLabels(labels, config.Labels)

	log.Printf("Configured to monitor %d label(s):", len(config.Labels))
	for _, name := range config.Labels {
		found := false
		for _, lab := range labels {
			if lab.Name == name {
				found = true
				break
			}
		}
		if found {
			log.Printf("  - %s (following)", name)
		} else {
			log.Printf("  - %s (not found in account, skipping)", name)
		}
	}

	if len(labelIds) == 0 {
		return fmt.Errorf("none of the configured labels were found in the Gmail account")
	}

	unreadGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gmail_threads_unread",
			Help: "number of unread threads",
		},
		[]string{"Label"},
	)
	totalGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gmail_threads_total",
			Help: "total number of threads",
		},
		[]string{"Label"},
	)
	scrapeErrors := prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "gmail_scrape_errors_total",
			Help: "Total number of Gmail scrape errors",
		},
	)
	scrapeSuccess := prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "gmail_scrape_success_total",
			Help: "Total number of successful Gmail scrapes",
		},
	)
	registry := prometheus.NewRegistry()
	registry.MustRegister(unreadGauge, totalGauge, scrapeErrors, scrapeSuccess)

	stopCh := make(chan struct{})
	recordMetrics(config.Interval, unreadGauge, totalGauge, labelIds, srv, stopCh, scrapeErrors, scrapeSuccess)

	addr := "127.0.0.1:2112"
	if envAddr := os.Getenv("LISTEN_ADDRESS"); envAddr != "" {
		addr = envAddr
		if !isLocalhost(addr) {
			log.Printf("WARNING: LISTEN_ADDRESS %s exposes metrics on a non-localhost interface", addr)
		}
	}

	readTimeout := getEnvDuration("HTTP_READ_TIMEOUT", 15*time.Second)
	writeTimeout := getEnvDuration("HTTP_WRITE_TIMEOUT", 15*time.Second)
	idleTimeout := getEnvDuration("HTTP_IDLE_TIMEOUT", 60*time.Second)

	server := newServer(registry, addr, readTimeout, writeTimeout, idleTimeout)
	fmt.Printf("http://localhost%s/metrics\n", strings.TrimPrefix(addr, "127.0.0.1"))
	log.Printf("Starting HTTP server on %s", addr)
	return server.ListenAndServe()
}

func main() {
	configPath := "config.yml"
	if envPath := os.Getenv("CONFIG_PATH"); envPath != "" {
		configPath = envPath
	}

	credentialsPath := "credentials.json"
	if envPath := os.Getenv("GMAIL_CREDENTIALS_PATH"); envPath != "" {
		credentialsPath = envPath
	}

	if err := run(configPath, credentialsPath, createGmailService); err != nil {
		log.Fatal(err)
	}
}
