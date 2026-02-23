/*
Copyright 2024 pbsladek.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package metrics

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

// PrometheusClient queries the Prometheus HTTP API for kubelet volume stats.
// It implements PVCMetricsClient.
type PrometheusClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewPrometheusClient creates a PrometheusClient targeting the given Prometheus base URL
// (e.g. "http://prometheus:9090").
func NewPrometheusClient(baseURL string) *PrometheusClient {
	return &PrometheusClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// GetUsage queries Prometheus for the current kubelet_volume_stats_used_bytes
// and kubelet_volume_stats_capacity_bytes for the given PVC.
func (c *PrometheusClient) GetUsage(ctx context.Context, key PVCKey) (PVCUsage, error) {
	usedQuery := fmt.Sprintf(
		`kubelet_volume_stats_used_bytes{namespace=%q,persistentvolumeclaim=%q}`,
		key.Namespace, key.Name,
	)
	capQuery := fmt.Sprintf(
		`kubelet_volume_stats_capacity_bytes{namespace=%q,persistentvolumeclaim=%q}`,
		key.Namespace, key.Name,
	)

	used, err := c.queryInstant(ctx, usedQuery)
	if err != nil {
		return PVCUsage{}, fmt.Errorf("querying used bytes for %s/%s: %w", key.Namespace, key.Name, err)
	}
	cap, err := c.queryInstant(ctx, capQuery)
	if err != nil {
		return PVCUsage{}, fmt.Errorf("querying capacity bytes for %s/%s: %w", key.Namespace, key.Name, err)
	}
	return PVCUsage{UsedBytes: used, CapacityBytes: cap}, nil
}

// GetUsageRange queries Prometheus for historical kubelet_volume_stats_used_bytes
// for the given PVC over the specified time range and step.
func (c *PrometheusClient) GetUsageRange(ctx context.Context, key PVCKey, start, end time.Time, step time.Duration) ([]RangePoint, error) {
	query := fmt.Sprintf(
		`kubelet_volume_stats_used_bytes{namespace=%q,persistentvolumeclaim=%q}`,
		key.Namespace, key.Name,
	)
	return c.queryRange(ctx, query, start, end, step)
}

// prometheusResponse is the envelope for Prometheus HTTP API responses.
type prometheusResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []interface{}     `json:"value"`  // instant: [timestamp, "value"]
			Values [][]interface{}   `json:"values"` // range: [[timestamp, "value"], ...]
		} `json:"result"`
	} `json:"data"`
	Error     string `json:"error,omitempty"`
	ErrorType string `json:"errorType,omitempty"`
}

// queryInstant calls GET /api/v1/query and returns the first scalar result as int64.
// Returns 0 (not an error) if no series are found.
func (c *PrometheusClient) queryInstant(ctx context.Context, query string) (int64, error) {
	u := fmt.Sprintf("%s/api/v1/query?query=%s", c.baseURL, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("prometheus request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("reading prometheus response: %w", err)
	}

	var pr prometheusResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return 0, fmt.Errorf("decoding prometheus response: %w", err)
	}
	if pr.Status != "success" {
		return 0, fmt.Errorf("prometheus error (%s): %s", pr.ErrorType, pr.Error)
	}
	if len(pr.Data.Result) == 0 {
		return 0, nil
	}

	valStr, ok := pr.Data.Result[0].Value[1].(string)
	if !ok {
		return 0, fmt.Errorf("unexpected value type in prometheus response")
	}
	f, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing prometheus value %q: %w", valStr, err)
	}
	return int64(f), nil
}

// queryRange calls GET /api/v1/query_range and returns all data points as RangePoints.
func (c *PrometheusClient) queryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]RangePoint, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("start", strconv.FormatFloat(float64(start.Unix()), 'f', 0, 64))
	params.Set("end", strconv.FormatFloat(float64(end.Unix()), 'f', 0, 64))
	params.Set("step", strconv.FormatFloat(step.Seconds(), 'f', 0, 64))

	u := fmt.Sprintf("%s/api/v1/query_range?%s", c.baseURL, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prometheus range request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading prometheus range response: %w", err)
	}

	var pr prometheusResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("decoding prometheus range response: %w", err)
	}
	if pr.Status != "success" {
		return nil, fmt.Errorf("prometheus error (%s): %s", pr.ErrorType, pr.Error)
	}
	if len(pr.Data.Result) == 0 {
		return nil, nil
	}

	var points []RangePoint
	for _, pair := range pr.Data.Result[0].Values {
		if len(pair) < 2 {
			continue
		}
		tsFloat, ok := pair[0].(float64)
		if !ok {
			continue
		}
		valStr, ok := pair[1].(string)
		if !ok {
			continue
		}
		f, err := strconv.ParseFloat(valStr, 64)
		if err != nil {
			continue
		}
		points = append(points, RangePoint{
			Timestamp: time.Unix(int64(tsFloat), 0),
			UsedBytes: int64(f),
		})
	}
	return points, nil
}
