package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ardanlabs/service-training/05-configuration/cmd/salesapi/internal/handlers"
	"github.com/jmoiron/sqlx"
	"github.com/kelseyhightower/envconfig"
	_ "github.com/lib/pq"
)

// This is the application name.
const name = "salesapi"

type config struct {
	// NOTE: We don't pass in a connection string b/c our application may assume
	//       certain parameters are set.
	DB struct {
		User     string `default:"postgres"`
		Password string `default:"postgres" json:"-"` // Prevent the marshalling of secrets.
		Host     string `default:"localhost"`
		Name     string `default:"postgres"`

		DisableTLS bool `default:"false" envconfig:"disable_tls"`
	}

	HTTP struct {
		Address string `default:":8000"`
	}
}

func main() {
	// Process inputs.
	var flags struct {
		configOnly bool
	}
	flag.Usage = func() {
		fmt.Print("This daemon is a service which manages products.\n\nUsage of sales-api:\n\nsales-api [flags]\n\n")
		flag.CommandLine.SetOutput(os.Stdout)
		flag.PrintDefaults()
		fmt.Print("\nConfiguration:\n\n")
		envconfig.Usage(name, &config{})
	}
	flag.BoolVar(&flags.configOnly, "config-only", false, "only show parsed configuration and exit")
	flag.Parse()

	var cfg config
	if err := envconfig.Process(name, &cfg); err != nil {
		log.Fatalf("error: parsing config: %s", err)
	}

	if flags.configOnly {
		if err := json.NewEncoder(os.Stdout).Encode(cfg); err != nil {
			log.Fatalf("error: encoding config as json: %s", err)
		}
		return
	}

	// Initialize dependencies.
	var db *sqlx.DB
	{
		sslMode := "require"
		if cfg.DB.DisableTLS {
			sslMode = "disable"
		}
		u := url.URL{
			Scheme: "postgres",
			User:   url.UserPassword(cfg.DB.User, cfg.DB.Password),
			Host:   cfg.DB.Host,
			Path:   cfg.DB.Name,
			RawQuery: (url.Values{
				"sslmode":  []string{sslMode},
				"timezone": []string{"utc"},
			}).Encode(),
		}

		var err error
		db, err = sqlx.Connect("postgres", u.String())
		if err != nil {
			log.Fatalf("error: connecting to db: %s", err)
		}

		defer db.Close()
	}

	productsHandler := handlers.Products{DB: db}

	server := http.Server{
		Addr:    cfg.HTTP.Address,
		Handler: http.HandlerFunc(productsHandler.List),
	}

	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.ListenAndServe()
	}()

	osSignals := make(chan os.Signal, 1)
	signal.Notify(osSignals, os.Interrupt, syscall.SIGTERM)

	log.Print("startup complete")

	select {
	case err := <-serverErrors:
		log.Fatalf("error: listening and serving: %s", err)

	case <-osSignals:
		log.Print("caught signal, shutting down")

		// Give outstanding requests 15 seconds to complete.
		const timeout = 15 * time.Second
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			log.Printf("error: gracefully shutting down server: %s", err)
			if err := server.Close(); err != nil {
				log.Printf("error: closing server: %s", err)
			}
		}
	}

	log.Print("done")
}
