package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"
	"github.com/onyxgrid/icon-load-balancer/internal/loadbalancer"
	"golang.org/x/crypto/acme/autocert"
)

func main() {
	godotenv.Load()
	domain := os.Getenv("DOMAIN")
	if domain == "" {
		log.Fatal("DOMAIN is not set in env")
	}

	lb := loadbalancer.New()
	lb.GetValidators()
	lb.CheckNodes()

	//todo make a route for tls connections and a route for non-tls connections

	http.Handle("/api/v3", lb.RateLimiter(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lb.ForwardRequest(lb.Nodes, w, r)
	})))

	certManager := autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(domain),
		Cache:      autocert.DirCache("certs"),
	}

	server := &http.Server{
		Addr:           ":443",
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1 MB
		TLSConfig:      certManager.TLSConfig(),
	}

	go server.ListenAndServeTLS("", "")
	select {}
}