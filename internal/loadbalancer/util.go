package loadbalancer

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"
)

func (lb *LoadBalancer) GetHealthyNodesAmount(w http.ResponseWriter, r *http.Request) {
	m := w.Header()
	m["Content-Type"] = []string{"application/json"}

	res := fmt.Sprintf("{\"amount\": %v}", len(lb.Nodes))
	w.Write([]byte(res))
}

type SafeMap struct {
	mu sync.RWMutex
	m  map[string]time.Duration
}

func NewSafeMap() *SafeMap {
	return &SafeMap{
		m: make(map[string]time.Duration),
	}
}

func (sm *SafeMap) Set(key string, value time.Duration) {
	sm.mu.Lock()
	sm.m[key] = value
	sm.mu.Unlock()
}

func (sm *SafeMap) Get(key string) (time.Duration, bool) {
	sm.mu.RLock()
	value, ok := sm.m[key]
	sm.mu.RUnlock()
	return value, ok
}

func (sm *SafeMap) Delete(key string) {
	sm.mu.Lock()
	delete(sm.m, key)
	sm.mu.Unlock()
}

// sortNodes sorts the nodes by the duration of the durationMap
func (lb *LoadBalancer) sortNodes() {
	sort.Slice(lb.Nodes, func(i, j int) bool {
		iTime, _ := lb.NodeTimes.Get(lb.Nodes[i])
		jTime, _ := lb.NodeTimes.Get(lb.Nodes[j])
		return iTime < jTime
	})
}

// remove node from list
func (lb *LoadBalancer) removeNode(addr string) {
	lb.mtx.Lock()
	for i, node := range lb.Nodes {
		if node == addr {
			lb.Nodes = append(lb.Nodes[:i], lb.Nodes[i+1:]...)
		}
	}
	lb.mtx.Unlock()
}
