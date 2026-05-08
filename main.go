package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/iancoleman/strcase"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/api/gmail/v1"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Interval int      `yaml:"interval"`
	Labels   []string `yaml:"labels"`
}

func loadConfig(path string) (Config, error) {
	var config Config
	configFile, err := ioutil.ReadFile(path)
	if err != nil {
		return config, fmt.Errorf("could not read config file: %w", err)
	}

	err = yaml.Unmarshal(configFile, &config)
	if err != nil {
		return config, fmt.Errorf("could not parse config file: %w", err)
	}

	return config, nil
}

func matchLabels(labels []*gmail.Label, desired []string) []string {
	var labelIds []string
	for _, lab := range labels {
		for _, desiredLabel := range desired {
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

func recordMetrics(interval int, unreadGauge *prometheus.GaugeVec, totalGauge *prometheus.GaugeVec, labelIds []string, srv *gmail.Service, stopCh <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(time.Duration(interval) * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				fmt.Printf("scraping %d labels\n", len(labelIds))
				if err := scrapeMetrics(unreadGauge, totalGauge, labelIds, srv); err != nil {
					fmt.Printf("%v\n", err)
				}
			case <-stopCh:
				return
			}
		}
	}()
}

func newServer(registry *prometheus.Registry, addr string) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	return &http.Server{
		Addr:    addr,
		Handler: mux,
	}
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

	labelIds := matchLabels(labels, config.Labels)

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
	registry := prometheus.NewRegistry()
	registry.MustRegister(unreadGauge, totalGauge)

	stopCh := make(chan struct{})
	recordMetrics(config.Interval, unreadGauge, totalGauge, labelIds, srv, stopCh)

	addr := ":2112"
	if envAddr := os.Getenv("LISTEN_ADDRESS"); envAddr != "" {
		addr = envAddr
	}

	server := newServer(registry, addr)
	fmt.Printf("http://localhost%s/metrics\n", addr)
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
