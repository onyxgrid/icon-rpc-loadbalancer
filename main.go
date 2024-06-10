package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

var nodes = []string{
	// "https://ctz.solidwallet.io/api/v3",
}

// Struct to hold rate limiter and block expiration time
type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

var (
	visitors      = make(map[string]*visitor)
	mtx           sync.Mutex
	cleanupTicker = time.NewTicker(time.Minute * 1)
	client        = &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        100,
			IdleConnTimeout:     90 * time.Second,
			MaxIdleConnsPerHost: 100,
		},
	}
)

func init() {
	go cleanupVisitors()
}

// Middleware to apply rate limiting
func rateLimiter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "Unable to parse IP address", http.StatusInternalServerError)
			return
		}

		// log.Default().Println("Request from", ip)
		mtx.Lock()
		v, exists := visitors[ip]
		if !exists {
			v = &visitor{
				limiter: rate.NewLimiter(10, 20), // 10 requests per second, burst of 20
			}
			visitors[ip] = v
		}
		v.lastSeen = time.Now()
		mtx.Unlock()

		if !v.limiter.Allow() {
			http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// Cleanup function to remove old entries
func cleanupVisitors() {
	for {
		<-cleanupTicker.C
		mtx.Lock()
		for ip, v := range visitors {
			if time.Since(v.lastSeen) > 10*time.Minute {
				delete(visitors, ip)
			}
		}
		mtx.Unlock()
	}
}

func main() {
	http.Handle("/", rateLimiter(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwardRequest(nodes, w, r)
	})))

	getValidators()
	checkNodes()

	server := &http.Server{
		Addr:           ":9000",
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1 MB
	}
	log.Println("Load balancer running on port 9000")
	log.Fatal(server.ListenAndServe())

}

func forwardRequest(nodes []string, w http.ResponseWriter, r *http.Request) {
	// create a wrapping context with a 7-second timeout
	outerCtx, wrappingCancel := context.WithTimeout(r.Context(), 7*time.Second)
	defer wrappingCancel()

	tried := make(map[int]bool)
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	for len(tried) < len(nodes) {
		select {
		case <-outerCtx.Done():
			http.Error(w, "Request timeout", http.StatusGatewayTimeout)
			return
		default:
			try := rand.Intn(len(nodes))
			if tried[try] {
				continue
			}

			node := nodes[try]
			tried[try] = true

			// add a timeout to the request
			ctx, cancel := context.WithTimeout(outerCtx, 2*time.Second)
			defer cancel()

			// log.Default().Println("Forwarding request to", node)
			req, err := http.NewRequestWithContext(ctx, r.Method, node+r.RequestURI, r.Body)
			if err != nil {
				// http.Error(w, err.Error(), http.StatusInternalServerError)
				// return
				continue
			}

			req.Header = r.Header
			resp, err := client.Do(req)
			if err != nil {
				// if error due to context deadline exceeded, continue to the next node
				if ctx.Err() == context.DeadlineExceeded {
					// print address
					fmt.Println("context deadline exceeded - ", node)
					continue
				}
				if opErr, ok := err.(*net.OpError); ok && opErr.Op == "dial" {
					// Handle connection refused error
					if opErr.Err.Error() == "connect: connection refused" {
						fmt.Println("Connection refused")
						continue
					}
				}

				// todo also catch random errors and just continue to the next node
				// todo, might be hard, because error could also be due to the client...
				fmt.Println("error in resp node", err.Error())
				// http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer resp.Body.Close()

			for k, v := range resp.Header {
				w.Header()[k] = v
			}
			w.WriteHeader(resp.StatusCode)

			io.Copy(w, resp.Body)

			// if response body is empty, print the address
			_, err = io.ReadAll(resp.Body)
			if err != nil {
				fmt.Println("error reading body", err)
			}

			if resp.StatusCode == http.StatusOK {
				fmt.Println("success")
				return
			} else {
				fmt.Println("failed")
			}
		}
	}

	// if all nodes fail
	http.Error(w, "All nodes failed", http.StatusServiceUnavailable)
}

func getValidators() ([]string, error) {
	u := "https://tracker.icon.community/api/v1/governance/preps"

	// Create a new HTTP request
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}

	// Set the request headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Send the request
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var res []any
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}

	//TODO is there a health endpoint? if so, check that before adding to nodes
	for _, validator := range res {
		ip := validator.(map[string]interface{})["api_endpoint"]
		if ip != nil && ip != "" {
			if strings.HasPrefix(ip.(string), "http") || strings.HasPrefix(ip.(string), "https") {
				nodes = append(nodes, ip.(string))
			} else {
				nodes = append(nodes, "http://"+ip.(string)+"/api/v3")
			}
		}
	}

	return []string{}, nil
}

// checkNode checks if the node is healthy, if not, remove it from the nodes list
func checkNodes() {
	wg := sync.WaitGroup{}
	for _, addr := range nodes {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			// var caught *error

			body := map[string]interface{}{
				"jsonrpc": "2.0",
				"method":  "icx_getTotalSupply",
				"id":      0,
			}

			// Encode the body to JSON
			bodyBytes, err := json.Marshal(body)
			if err != nil {
				// caught = &err
				mtx.Lock()
				for i, node := range nodes {
					if node == addr {
						nodes = append(nodes[:i], nodes[i+1:]...)
					}
				}
				mtx.Unlock()
			}

			// Create a new HTTP request
			req, err := http.NewRequestWithContext(ctx, "POST", addr, bytes.NewBuffer(bodyBytes))
			if err != nil {
				fmt.Println("error creating request:", err)
				// caught = &err
				mtx.Lock()
				for i, node := range nodes {
					if node == addr {
						nodes = append(nodes[:i], nodes[i+1:]...)
					}
				}
				mtx.Unlock()
			}
			req.Header.Set("Content-Type", "application/json")

			// Send the request
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				fmt.Println("error sending request:", err, addr)

				// caught = &err
				mtx.Lock()
				for i, node := range nodes {
					if node == addr {
						nodes = append(nodes[:i], nodes[i+1:]...)
					}
				}
				mtx.Unlock()
				return
			}
			defer resp.Body.Close()

			// Decode the response
			var res map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
				fmt.Println("error decoding response:", err)
				// caught = &err
				mtx.Lock()
				for i, node := range nodes {
					if node == addr {
						nodes = append(nodes[:i], nodes[i+1:]...)
					}
				}
				mtx.Unlock()
				return
			}

			total := res["result"].(string)
			bn := new(big.Int)
			bn.SetString(total, 0)
			bn.Div(bn, big.NewInt(1000000000000000000))

			// if total supply is 0, remove node from nodes
			if bn.Cmp(big.NewInt(0)) == 0 || resp.StatusCode != http.StatusOK {
				// remove the node from the nodes list
				mtx.Lock()
				for i, node := range nodes {
					if node == addr {
						nodes = append(nodes[:i], nodes[i+1:]...)
					}
				}
				mtx.Unlock()
			}

		}(addr)
	}

	wg.Wait()

	for _, node := range nodes {
		fmt.Println(node)
	}

	fmt.Printf("%d healthy nodes\n", len(nodes))
}
