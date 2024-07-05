package loadbalancer

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Struct to hold rate limiter and block expiration time
type visitor struct {
	Limiter  *rate.Limiter
	LastSeen time.Time
}

type LoadBalancer struct {
	NodeTimes     SafeMap
	Nodes         []string
	visitors      map[string]*visitor
	mtx           sync.Mutex
	cleanupTicker *time.Ticker
	client        *http.Client
	Logger        *log.Logger
	debug         bool
}

// New creates a new LoadBalancer instance - takes in a log file and a debug flag
func New(lf *os.File, d bool) *LoadBalancer {
	lb := &LoadBalancer{}
	lb.debug = d
	lb.Logger = log.New(lf, "", log.LstdFlags)
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

	lb.NodeTimes = *NewSafeMap()
	lb.Nodes = []string{
		"https://ctz.solidwallet.io/api/v3",
		"https://api.icon.community/api/v3",
	}

	go lb.cleanupVisitors()

	// initial check
	lb.CheckNodes()

	// periodic check
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			lb.CheckNodes()
		}
	}()

	return lb
}

func (lb *LoadBalancer) ForwardRequestWithSSL(Nodes []string, w http.ResponseWriter, r *http.Request) {
	outerCtx, wrappingCancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer wrappingCancel()

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// HTTP client without TLS for communicating with nodes
	nonSSLClient := &http.Client{
		Timeout: 5 * time.Second,
	}

	for _, node := range lb.Nodes {
		select {
		case <-outerCtx.Done():
			http.Error(w, "Request timeout", http.StatusGatewayTimeout)
			return
		default:
			ctx, cancel := context.WithTimeout(outerCtx, time.Second)
			defer cancel()

			r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes)) // reset the body
			req, err := http.NewRequestWithContext(ctx, r.Method, node, r.Body)
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
				lb.Logger.Println("error in resp node", err.Error())
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
			w.Header().Set("node-used", node)

			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)

			if resp.StatusCode == http.StatusOK {
				return
			}
			if resp.StatusCode == http.StatusBadRequest {
				return
			}
		}
	}
	http.Error(w, "All Nodes failed", http.StatusServiceUnavailable)
}

// Middleware to apply rate limiting |
// Takes in a http.Handler, and returns a http.Handler that applies rate limiting on the returned handler
func (lb *LoadBalancer) RateLimiter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set CORS headers
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		// Handle preflight requests
		if r.Method == "OPTIONS" {
			return
		}

		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "Unable to parse IP address", http.StatusInternalServerError)
			return
		}

		lb.mtx.Lock()
		v, exists := lb.visitors[ip]
		if !exists {
			v = &visitor{
				Limiter: rate.NewLimiter(1000, 1500), // 1000 requests per second, burst of 1500
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
