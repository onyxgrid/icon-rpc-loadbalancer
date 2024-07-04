package loadbalancer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// setValidators sets all the validators using the tracker.icon.community api
func (lb *LoadBalancer) setValidators() error {
	u := "https://tracker.icon.community/api/v1/governance/preps"
	n := []string{}
	// Create a new HTTP request
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return err
	}

	// Set the request headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Send the request
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var res []any
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return err
	}

	for _, validator := range res {
		ip := validator.(map[string]interface{})["api_endpoint"]
		if ip != nil && ip != "" {
			if strings.HasPrefix(ip.(string), "http") || strings.HasPrefix(ip.(string), "https") {
				n = append(n, ip.(string))
			} else {
				n = append(n, "http://"+ip.(string)+"/api/v3")
			}
		}
	}

	lb.mtx.Lock()
	lb.Nodes = n
	lb.mtx.Unlock()
	return nil
}

// checkNode checks if the node is healthy, if not, remove it from the lb.Nodes list
func (lb *LoadBalancer) CheckNodes() {
	err := lb.setValidators()
	if err != nil {
		//todo log
		fmt.Println(err)
	}

	wg := sync.WaitGroup{}
	for _, addr := range lb.Nodes {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()

			body := map[string]interface{}{
				"jsonrpc": "2.0",
				"method":  "icx_getTotalSupply",
				"id":      0,
			}

			// Encode the body to JSON
			bodyBytes, err := json.Marshal(body)
			if err != nil {
				// todo | log
			}

			// Create a new HTTP request
			req, err := http.NewRequestWithContext(ctx, "POST", addr, bytes.NewBuffer(bodyBytes))
			if err != nil {
				// todo 
				lb.removeNode(addr)
			}
			req.Header.Set("Content-Type", "application/json")

			start := time.Now()
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				// todo | log
				lb.removeNode(addr)
				return
			}
			defer resp.Body.Close()
			lb.NodeTimes.Set(addr, time.Since(start))

			var res map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
				// todo | log
				lb.removeNode(addr)
				return
			}

			total := res["result"].(string)
			bn := new(big.Int)
			bn.SetString(total, 0)
			bn.Div(bn, big.NewInt(1000000000000000000))

			if bn.Cmp(big.NewInt(0)) == 0 || resp.StatusCode != http.StatusOK {
				// todo | log
				lb.removeNode(addr)
			}

		}(addr)
	}
	wg.Wait()

	lb.sortNodes()
	fmt.Printf("%d healthy lb.Nodes\n", len(lb.Nodes))
}
