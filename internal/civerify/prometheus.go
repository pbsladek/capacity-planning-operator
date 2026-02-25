package civerify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// PrometheusClient is a minimal client for Prometheus instant scalar queries.
type PrometheusClient struct {
	baseURL string
	http    *http.Client
}

// NewPrometheusClient creates a Prometheus API client.
func NewPrometheusClient(baseURL string, timeout time.Duration) *PrometheusClient {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &PrometheusClient{
		baseURL: baseURL,
		http:    &http.Client{Timeout: timeout},
	}
}

type prometheusInstantResponse struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType,omitempty"`
	Error     string `json:"error,omitempty"`
	Data      struct {
		Result []struct {
			Value []interface{} `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

func (c *PrometheusClient) queryInstant(ctx context.Context, query string) (prometheusInstantResponse, error) {
	var pr prometheusInstantResponse

	u, err := url.Parse(c.baseURL + "/api/v1/query")
	if err != nil {
		return pr, fmt.Errorf("invalid Prometheus URL: %w", err)
	}
	params := u.Query()
	params.Set("query", query)
	u.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return pr, fmt.Errorf("building request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return pr, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return pr, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return pr, fmt.Errorf("prometheus HTTP %d: %s", resp.StatusCode, string(body))
	}

	if err := json.Unmarshal(body, &pr); err != nil {
		return pr, fmt.Errorf("decoding response: %w", err)
	}
	if pr.Status != "success" {
		return pr, fmt.Errorf("prometheus query failed (%s): %s", pr.ErrorType, pr.Error)
	}
	return pr, nil
}

// QueryInstantScalar executes an instant query and returns the first scalar.
// hasData=false indicates empty result (no time series at query time).
func (c *PrometheusClient) QueryInstantScalar(ctx context.Context, query string) (value float64, hasData bool, err error) {
	pr, err := c.queryInstant(ctx, query)
	if err != nil {
		return 0, false, err
	}
	if len(pr.Data.Result) == 0 {
		return 0, false, nil
	}
	if len(pr.Data.Result[0].Value) < 2 {
		return 0, false, fmt.Errorf("unexpected Prometheus value payload")
	}
	valStr, ok := pr.Data.Result[0].Value[1].(string)
	if !ok {
		return 0, false, fmt.Errorf("unexpected Prometheus value type")
	}
	val, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return 0, false, fmt.Errorf("parsing Prometheus value %q: %w", valStr, err)
	}
	return val, true, nil
}

// QueryInstantHasResults executes an instant query and reports whether at least one series is returned.
func (c *PrometheusClient) QueryInstantHasResults(ctx context.Context, query string) (bool, error) {
	pr, err := c.queryInstant(ctx, query)
	if err != nil {
		return false, err
	}
	return len(pr.Data.Result) > 0, nil
}

// BuildPVCGrowthDerivQuery builds the PromQL expression used for growth cross-check.
func BuildPVCGrowthDerivQuery(namespace, pvcName string, windowSeconds int) string {
	if windowSeconds < 1 {
		windowSeconds = 1
	}
	return fmt.Sprintf(
		`max(deriv(kubelet_volume_stats_used_bytes{namespace=%q,persistentvolumeclaim=%q}[%ds])) * 86400`,
		namespace,
		pvcName,
		windowSeconds,
	)
}

// BuildOperatorPVCGrowthDerivQuery builds a deriv query from the operator-exported
// capacityplan_pvc_usage_bytes gauge. This metric is sourced from the same watcher
// samples that feed CapacityPlan status, so it is preferred for growth math cross-checks.
func BuildOperatorPVCGrowthDerivQuery(namespace, pvcName string, windowSeconds int) string {
	if windowSeconds < 1 {
		windowSeconds = 1
	}
	return fmt.Sprintf(
		`max(deriv(capacityplan_pvc_usage_bytes{namespace=%q,pvc=%q}[%ds])) * 86400`,
		namespace,
		pvcName,
		windowSeconds,
	)
}
