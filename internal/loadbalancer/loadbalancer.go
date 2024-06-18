package loadbalancer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// todo: fix the superfluous stuff in the ForwardRequest function
// might be fixed by resetting the body of the request in each loop using the io.NopCloser(bytes.NewBuffer(bodyBytes)) line

// Struct to hold rate limiter and block expiration time
type visitor struct {
	Limiter  *rate.Limiter
	LastSeen time.Time
}

type LoadBalancer struct {
	Nodes         []string
	visitors      map[string]*visitor
	mtx           sync.Mutex
	cleanupTicker *time.Ticker
	client        *http.Client
}

func New() *LoadBalancer {
	lb := &LoadBalancer{}
	lb.visitors = make(map[string]*visitor)
	lb.mtx = sync.Mutex{}
	lb.cleanupTicker = time.NewTicker(time.Minute * 1)
	lb.client = &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        100,
			IdleConnTimeout:     90 * time.Second,
			MaxIdleConnsPerHost: 100,
		},
	}
	lb.Nodes = []string{
		"https://ctz.solidwallet.io/api/v3",
		"https://api.icon.community/api/v3",
	}

	lb.cleanupVisitors()
	return lb
}

func (lb *LoadBalancer) ForwardRequest(Nodes []string, w http.ResponseWriter, r *http.Request) {
	outerCtx, wrappingCancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer wrappingCancel()

	fmt.Println("Incoming request from", r.RemoteAddr)

	tried := make(map[int]bool)
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for len(tried) < len(lb.Nodes) {
		select {
		case <-outerCtx.Done():
			http.Error(w, "Request timeout", http.StatusGatewayTimeout)
			return
		default:
			try := rand.Intn(len(lb.Nodes))
			if tried[try] {
				continue
			}

			node := lb.Nodes[try]
			tried[try] = true

			ctx, cancel := context.WithTimeout(outerCtx, time.Second)
			defer cancel()

			r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes)) // reset the body
			req, err := http.NewRequestWithContext(ctx, r.Method, node+r.RequestURI, r.Body)
			if err != nil {
				cancel()
				continue
			}

			req.Header = r.Header
			resp, err := lb.client.Do(req)
			cancel()

			if err != nil {
				// if error due to context deadline exceeded, continue to the next node
				if ctx.Err() == context.DeadlineExceeded {
					continue
				}
				// if error due to connection refused, continue to the next node
				if opErr, ok := err.(*net.OpError); ok && opErr.Op == "dial" {
					if opErr.Err.Error() == "connect: connection refused" {
						continue
					}
				}
				fmt.Println("error in resp node", err.Error())
				return
			}
			defer resp.Body.Close()

			// if ratelimit error, continue to the next node
			if resp.StatusCode == http.StatusTooManyRequests {
				continue
			}

			for k, v := range resp.Header {
				w.Header()[k] = v
			}
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)

			if resp.StatusCode == http.StatusOK {
				return
			} else {
				fmt.Println("failed getting response from node", node, "with status code", resp.StatusCode)
			}
		}
	}
	http.Error(w, "All Nodes failed", http.StatusServiceUnavailable)
}


func (lb *LoadBalancer) ForwardRequestWithSSL(Nodes []string, w http.ResponseWriter, r *http.Request) {
	outerCtx, wrappingCancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer wrappingCancel()

	tried := make(map[int]bool)
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// HTTP client without TLS for communicating with nodes
	nonSSLClient := &http.Client{
		Timeout: 5 * time.Second,
	}

	for len(tried) < len(lb.Nodes) {
		select {
		case <-outerCtx.Done():
			http.Error(w, "Request timeout", http.StatusGatewayTimeout)
			return
		default:
			try := rand.Intn(len(lb.Nodes))
			if tried[try] {
				continue
			}

			node := lb.Nodes[try]
			tried[try] = true

			ctx, cancel := context.WithTimeout(outerCtx, time.Second)
			defer cancel()

			r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes)) // reset the body
			req, err := http.NewRequestWithContext(ctx, r.Method, "http://"+node+r.RequestURI, r.Body)
			if err != nil {
				cancel()
				continue
			}

			req.Header = r.Header
			resp, err := nonSSLClient.Do(req)
			cancel()

			if err != nil {
				// if error due to context deadline exceeded, continue to the next node
				if ctx.Err() == context.DeadlineExceeded {
					continue
				}
				// if error due to connection refused, continue to the next node
				if opErr, ok := err.(*net.OpError); ok && opErr.Op == "dial" {
					if opErr.Err.Error() == "connect: connection refused" {
						continue
					}
				}
				fmt.Println("error in resp node", err.Error())
				continue
			}
			defer resp.Body.Close()

			// if ratelimit error, continue to the next node
			if resp.StatusCode == http.StatusTooManyRequests {
				continue
			}

			for k, v := range resp.Header {
				w.Header()[k] = v
			}
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)

			if resp.StatusCode == http.StatusOK {
				return
			} else {
				fmt.Println("failed getting response from node", node, "with status code", resp.StatusCode)
			}
		}
	}
	http.Error(w, "All Nodes failed", http.StatusServiceUnavailable)
}


// todo add a func that updates the nodes every n minutes - just a loop that runs getvalidators() and checknodes() over n mintues

// Middleware to apply rate limiting |
// Takes in a http.Handler, and returns a http.Handler that applies rate limiting on that handler
func (lb *LoadBalancer) RateLimiter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "Unable to parse IP address", http.StatusInternalServerError)
			return
		}

		// log.Default().Println("Request from", ip)
		lb.mtx.Lock()
		v, exists := lb.visitors[ip]
		if !exists {
			v = &visitor{
				Limiter: rate.NewLimiter(10, 20), // 10 requests per second, burst of 20
			}
			lb.visitors[ip] = v
		}
		v.LastSeen = time.Now()
		lb.mtx.Unlock()

		if !v.Limiter.Allow() {
			http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// Cleanup function to remove old entries
func (lb *LoadBalancer) cleanupVisitors() {
	for {
		<-lb.cleanupTicker.C
		lb.mtx.Lock()
		for ip, v := range lb.visitors {
			if time.Since(v.LastSeen) > 10*time.Minute {
				delete(lb.visitors, ip)
			}
		}
		lb.mtx.Unlock()
	}
}

// todo: make health endpoint?

// todo: handle tls stuff
