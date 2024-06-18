package loadbalancer

import (
	"net/http"
	"fmt"
)

func (lb *LoadBalancer)GetHealthyNodesAmount(w http.ResponseWriter, r *http.Request) {
	m := w.Header()
	m["Content-Type"] = []string{"application/json"}

	res := fmt.Sprintf("{\"amount\": %v}", len(lb.Nodes))
	w.Write([]byte(res))
}