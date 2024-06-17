package loadbalancer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"math/rand"
	"net"
	"net/http"
	"strings"
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
	}

	lb.cleanupVisitors()
	return lb
}

func (lb *LoadBalancer) ForwardRequest(Nodes []string, w http.ResponseWriter, r *http.Request) {
	// for test just return a "hello world" response
	// w.Write([]byte("Hello, world!"))
	// return

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

			// add a timeout to the request
			ctx, cancel := context.WithTimeout(outerCtx, 2*time.Second)
			defer cancel()

			req, err := http.NewRequestWithContext(ctx, r.Method, node+r.RequestURI, r.Body)
			if err != nil {
				continue
			}

			req.Header = r.Header
			resp, err := lb.client.Do(req)
			if err != nil {
				// if error due to context deadline exceeded, continue to the next node
				if ctx.Err() == context.DeadlineExceeded {
					continue
				}
				if opErr, ok := err.(*net.OpError); ok && opErr.Op == "dial" {
					// Handle connection refused error
					if opErr.Err.Error() == "connect: connection refused" {
						continue
					}
				}
				fmt.Println("error in resp node", err.Error())
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

	// if all lb.Nodes fail
	http.Error(w, "All Nodes failed", http.StatusServiceUnavailable)
}

// todo add a func that updates the nodes every n minutes

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

// checkNode checks if the node is healthy, if not, remove it from the lb.Nodes list
func (lb *LoadBalancer) CheckNodes() {
	wg := sync.WaitGroup{}
	for _, addr := range lb.Nodes {
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
				lb.mtx.Lock()
				for i, node := range lb.Nodes {
					if node == addr {
						lb.Nodes = append(lb.Nodes[:i], lb.Nodes[i+1:]...)
					}
				}
				lb.mtx.Unlock()
			}

			// Create a new HTTP request
			req, err := http.NewRequestWithContext(ctx, "POST", addr, bytes.NewBuffer(bodyBytes))
			if err != nil {
				fmt.Println("error creating request:", err)
				// caught = &err
				lb.mtx.Lock()
				for i, node := range lb.Nodes {
					if node == addr {
						lb.Nodes = append(lb.Nodes[:i], lb.Nodes[i+1:]...)
					}
				}
				lb.mtx.Unlock()
			}
			req.Header.Set("Content-Type", "application/json")

			// Send the request
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				fmt.Println("error sending request:", err, addr)

				// caught = &err
				lb.mtx.Lock()
				for i, node := range lb.Nodes {
					if node == addr {
						lb.Nodes = append(lb.Nodes[:i], lb.Nodes[i+1:]...)
					}
				}
				lb.mtx.Unlock()
				return
			}
			defer resp.Body.Close()

			// Decode the response
			var res map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
				fmt.Println("error decoding response:", err)
				// caught = &err
				lb.mtx.Lock()
				for i, node := range lb.Nodes {
					if node == addr {
						lb.Nodes = append(lb.Nodes[:i], lb.Nodes[i+1:]...)
					}
				}
				lb.mtx.Unlock()
				return
			}

			total := res["result"].(string)
			bn := new(big.Int)
			bn.SetString(total, 0)
			bn.Div(bn, big.NewInt(1000000000000000000))

			// if total supply is 0, remove node from lb.Nodes
			if bn.Cmp(big.NewInt(0)) == 0 || resp.StatusCode != http.StatusOK {
				// remove the node from the lb.Nodes list
				lb.mtx.Lock()
				for i, node := range lb.Nodes {
					if node == addr {
						lb.Nodes = append(lb.Nodes[:i], lb.Nodes[i+1:]...)
					}
				}
				lb.mtx.Unlock()
			}

		}(addr)
	}

	wg.Wait()

	for _, node := range lb.Nodes {
		fmt.Println(node)
	}

	fmt.Printf("%d healthy lb.Nodes\n", len(lb.Nodes))
}

func (lb *LoadBalancer) GetValidators() ([]string, error) {
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

	//TODO is there a health endpoint? if so, check that before adding to lb.Nodes
	for _, validator := range res {
		ip := validator.(map[string]interface{})["api_endpoint"]
		if ip != nil && ip != "" {
			if strings.HasPrefix(ip.(string), "http") || strings.HasPrefix(ip.(string), "https") {
				lb.Nodes = append(lb.Nodes, ip.(string))
			} else {
				lb.Nodes = append(lb.Nodes, "http://"+ip.(string)+"/api/v3")
			}
		}
	}

	return []string{}, nil
}
