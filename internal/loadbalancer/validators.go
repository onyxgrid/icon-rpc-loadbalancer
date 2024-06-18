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

// todo | handle a ratelimit response from the node. atm i think the node will than be taken out of the lb.Nodes list. But what if if the ratelimit on all the nodes is reached..
// todo | nodes that respond with a ratelimit response should be taken out of the lb.Nodes list for a certain amount of time, and then put back in


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

	// for _, node := range lb.Nodes {
	// 	fmt.Println(node)
	// }

	fmt.Printf("%d healthy lb.Nodes\n", len(lb.Nodes))
}
