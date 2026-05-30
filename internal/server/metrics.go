package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/lennart/oxidize/internal/oxide"
	"github.com/lennart/oxidize/internal/translate"
)

// handleSystemMetric answers the legacy provisioned-capacity metrics
// (cpus_provisioned / ram_provisioned / virtual_disk_space_provisioned) used by
// the system & silo Utilization pages. Proxmox keeps no provisioning history,
// so we return the current provisioned total as a flat series over the window.
func (s *Server) handleSystemMetric(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, prov, err := s.clusterUtilization(ctx)
	if err != nil {
		oxide.WriteJSON(w, http.StatusOK, oxide.Page([]any{}))
		return
	}
	var val int64
	switch r.PathValue("metricName") {
	case "cpus_provisioned":
		val = prov.CPUs
	case "ram_provisioned":
		val = prov.Memory
	case "virtual_disk_space_provisioned":
		val = prov.Storage
	}

	now := time.Now().UTC()
	end := parseTimeParam(r, "end_time", now)
	point := func(t time.Time) map[string]any {
		return map[string]any{
			"timestamp": t.Format(time.RFC3339),
			"datum":     map[string]any{"type": "i64", "datum": val},
		}
	}
	items := []map[string]any{}
	if startStr := r.URL.Query().Get("start_time"); startStr != "" {
		items = append(items, point(parseTimeParam(r, "start_time", now)))
	}
	items = append(items, point(end))
	oxide.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "next_page": nil})
}

func parseTimeParam(r *http.Request, key string, fallback time.Time) time.Time {
	if v := r.URL.Query().Get(key); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t.UTC()
		}
	}
	return fallback
}

// handleTimeseriesQuery answers OxQL metric queries (POST /v1/timeseries/query
// and the system variant) using Proxmox per-VM RRD data. It maps the requested
// metric to the corresponding RRD field over the requested time window.
//
// The console's chart code reads `tables[0].timeseries[0].points`, sums across
// timeseries, and drops the first point, so we return exactly one timeseries
// with timestamps + values. Metrics Proxmox doesn't track (IOPS, packet counts,
// latencies) come back as gaps rather than misleading zeros.
func (s *Server) handleTimeseriesQuery(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		oxide.WriteJSON(w, http.StatusOK, emptyTimeseries())
		return
	}

	q := translate.ParseOxqlQuery(body.Query)
	ref, err := s.resolveInstance(ctx, q.InstanceRef)
	if err != nil || ref == nil || q.Metric == "" {
		oxide.WriteJSON(w, http.StatusOK, emptyTimeseries())
		return
	}

	timeframe := translate.TimeframeFor(q.Start, q.End)
	points, err := s.pve.RRD(ctx, ref.node, ref.vmid, timeframe)
	if err != nil {
		oxide.WriteJSON(w, http.StatusOK, emptyTimeseries())
		return
	}

	timestamps := make([]string, 0, len(points))
	values := make([]any, 0, len(points))
	for _, p := range points {
		t, ok := p["time"].(float64)
		if !ok {
			continue
		}
		ts := time.Unix(int64(t), 0).UTC()
		// Keep points within the requested window (when parsed).
		if !q.Start.IsZero() && ts.Before(q.Start) {
			continue
		}
		if !q.End.IsZero() && ts.After(q.End) {
			continue
		}
		timestamps = append(timestamps, ts.Format(time.RFC3339))
		if v, ok := translate.MetricValue(q.Metric, q.State, p); ok {
			values = append(values, v)
		} else {
			values = append(values, nil)
		}
	}

	result := map[string]any{
		"tables": []map[string]any{{
			"name": q.Metric,
			"timeseries": []map[string]any{{
				"fields": map[string]any{},
				"points": map[string]any{
					"timestamps": timestamps,
					"values": []map[string]any{{
						"metric_type": "gauge",
						"values":      map[string]any{"type": "double", "values": values},
					}},
				},
			}},
		}},
	}
	oxide.WriteJSON(w, http.StatusOK, result)
}

// emptyTimeseries is one empty table — enough for the chart to render "no data"
// without throwing on tables[0].
func emptyTimeseries() map[string]any {
	return map[string]any{
		"tables": []map[string]any{
			{"name": "oxidize", "timeseries": []any{}},
		},
	}
}
