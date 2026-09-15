package cluster_hash

import (
	"fmt"
	"hash/fnv"
	"net"
	"strings"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/tidwall/gjson"
)

const (
	DefaultHashHeader    = "x-mse-consumer"
	DefaultClusterHeader = "x-higress-target-cluster"

	// hash_source 取值
	HashSourceHeader   = "header"    // 默认，从 hash_header 指定的请求头取值
	HashSourceSourceIP = "source_ip" // 从连接源 IP（source.address 属性）取值
)

type clusterEntry struct {
	Cluster string
	Weight  int
}

type ClusterHashLoadBalancer struct {
	HashSource    string
	HashHeader    string
	ClusterHeader string
	// slots is expanded from clusters by weight, length == 100.
	slots []string
}

func NewClusterHashLoadBalancer(json gjson.Result) (ClusterHashLoadBalancer, error) {
	lb := ClusterHashLoadBalancer{}

	lb.HashSource = json.Get("hash_source").String()
	if lb.HashSource == "" {
		lb.HashSource = HashSourceHeader
	}
	switch lb.HashSource {
	case HashSourceSourceIP, HashSourceHeader:
	default:
		return lb, fmt.Errorf("hash_source %s is not supported", lb.HashSource)
	}

	lb.HashHeader = json.Get("hash_header").String()
	if lb.HashHeader == "" {
		lb.HashHeader = DefaultHashHeader
	}

	lb.ClusterHeader = json.Get("cluster_header").String()
	if lb.ClusterHeader == "" {
		lb.ClusterHeader = DefaultClusterHeader
	}

	clustersJson := json.Get("clusters")
	if !clustersJson.Exists() || !clustersJson.IsArray() || len(clustersJson.Array()) == 0 {
		return lb, fmt.Errorf("clusters is required and must be a non-empty array")
	}

	var clusters []clusterEntry
	var totalWeight int
	for _, c := range clustersJson.Array() {
		cluster := c.Get("cluster").String()
		if cluster == "" {
			return lb, fmt.Errorf("each entry must have a non-empty cluster field")
		}
		weight := int(c.Get("weight").Int())
		if weight <= 0 {
			return lb, fmt.Errorf("cluster %q has invalid weight %d, must be > 0", cluster, weight)
		}
		clusters = append(clusters, clusterEntry{Cluster: cluster, Weight: weight})
		totalWeight += weight
	}

	if totalWeight != 100 {
		return lb, fmt.Errorf("sum of cluster weights must be 100, got %d", totalWeight)
	}

	slots := make([]string, 0, 100)
	for _, c := range clusters {
		for i := 0; i < c.Weight; i++ {
			slots = append(slots, c.Cluster)
		}
	}
	lb.slots = slots
	return lb, nil
}

func (lb ClusterHashLoadBalancer) selectCluster(hashKey string) string {
	h := fnv.New32a()
	h.Write([]byte(hashKey))
	index := int(h.Sum32()) % len(lb.slots)
	if index < 0 {
		index += len(lb.slots)
	}
	return lb.slots[index]
}

func (lb ClusterHashLoadBalancer) HandleHttpRequestHeaders(ctx wrapper.HttpContext) types.Action {
	var (
		hashKey string
		err     error
	)

	switch lb.HashSource {
	case HashSourceSourceIP:
		// 从连接源地址属性获取直连上一跳的 IP:PORT，形如 "1.2.3.4:56789" 或 "[::1]:56789"
		bs, err := proxywasm.GetProperty([]string{"source", "address"})
		if err != nil || len(bs) == 0 {
			log.Warnf("[ai-load-balancer/cluster_hash] missing source address, rejecting request")
			_ = proxywasm.SendHttpResponse(403, nil, []byte("source address required"), -1)
			return types.ActionPause
		}
		hashKey = extractIP(string(bs))
		if hashKey == "" {
			log.Warnf("[ai-load-balancer/cluster_hash] invalid source address %q, rejecting request", string(bs))
			_ = proxywasm.SendHttpResponse(403, nil, []byte("source address required"), -1)
			return types.ActionPause
		}
		// 打印源 IP，方便定位请求来源与路由结果
		log.Infof("[ai-load-balancer/cluster_hash] source address %q -> source ip %q", string(bs), hashKey)
	default: // HashSourceHeader，读取 hash_header 指定的请求头
		hashKey, err = proxywasm.GetHttpRequestHeader(lb.HashHeader)
		if err != nil || hashKey == "" {
			log.Warnf("[ai-load-balancer/cluster_hash] missing hash header %q, rejecting request", lb.HashHeader)
			_ = proxywasm.SendHttpResponse(403, nil, []byte("hash header required"), -1)
			return types.ActionPause
		}
	}

	cluster := lb.selectCluster(hashKey)
	if err := proxywasm.ReplaceHttpRequestHeader(lb.ClusterHeader, cluster); err != nil {
		log.Errorf("[ai-load-balancer/cluster_hash] failed to set target header: %v", err)
		_ = proxywasm.SendHttpResponse(500, nil, []byte("internal error"), -1)
		return types.ActionPause
	}

	log.Debugf("[ai-load-balancer/cluster_hash] source=%s hashKey=%s -> %s=%s", lb.HashSource, hashKey, lb.ClusterHeader, cluster)
	return types.ActionContinue
}

// extractIP 从 "IP:PORT" 中提取纯 IP，兼容 IPv4 与 IPv6：
// "1.2.3.4:5678" -> "1.2.3.4"，"[::1]:8080" -> "::1"
func extractIP(address string) string {
	if host, _, err := net.SplitHostPort(address); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(address, "[]")
}

func (lb ClusterHashLoadBalancer) HandleHttpRequestBody(ctx wrapper.HttpContext, body []byte) types.Action {
	return types.ActionContinue
}

func (lb ClusterHashLoadBalancer) HandleHttpResponseHeaders(ctx wrapper.HttpContext) types.Action {
	return types.ActionContinue
}

func (lb ClusterHashLoadBalancer) HandleHttpStreamingResponseBody(ctx wrapper.HttpContext, data []byte, endOfStream bool) []byte {
	return data
}

func (lb ClusterHashLoadBalancer) HandleHttpResponseBody(ctx wrapper.HttpContext, body []byte) types.Action {
	return types.ActionContinue
}

func (lb ClusterHashLoadBalancer) HandleHttpStreamDone(ctx wrapper.HttpContext) {}
