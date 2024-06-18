package main

import (
	"fmt"
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
	fmt.Println("Domain is set to: ", domain)

	lb := loadbalancer.New()
	lb.GetValidators()
	lb.CheckNodes()

	http.Handle("/api/v3", lb.RateLimiter(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lb.ForwardRequestWithSSL(lb.Nodes, w, r)
	})))

	http.Handle("/nodes", http.HandlerFunc(lb.GetHealthyNodesAmount))

	certManager := autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(domain),
		Cache:      autocert.DirCache("certs"),
	}

	tlsConfig := certManager.TLSConfig()
	server := &http.Server{
		Addr:           ":443",
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1 MB
		TLSConfig:      tlsConfig,
	}

	fmt.Println("Starting server on port 443")
	go server.ListenAndServeTLS("", "")
	select {}
}
