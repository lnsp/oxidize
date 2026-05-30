package translate

import (
	"regexp"
	"time"
)

// OxqlQuery is the parsed subset of an OxQL metric query we care about.
type OxqlQuery struct {
	Metric      string
	InstanceRef string // value of instance_id or attached_instance_id
	State       string // cpu state filter (run/idle/...), if any
	Start       time.Time
	End         time.Time
}

var (
	reMetric   = regexp.MustCompile(`get\s+(\S+)`)
	reInstance = regexp.MustCompile(`(?:attached_instance_id|instance_id)\s*==\s*"([^"]+)"`)
	reState    = regexp.MustCompile(`state\s*==\s*"([^"]+)"`)
	reStart    = regexp.MustCompile(`timestamp\s*>=\s*@(\S+)`)
	reEnd      = regexp.MustCompile(`timestamp\s*<\s*@(\S+)`)
)

// oxqlTimeLayout is the timestamp format the console emits (no zone, ms=000).
const oxqlTimeLayout = "2006-01-02T15:04:05.000"

// ParseOxqlQuery extracts the metric, instance, state, and time range from an
// OxQL query string produced by the console.
func ParseOxqlQuery(q string) OxqlQuery {
	out := OxqlQuery{}
	if m := reMetric.FindStringSubmatch(q); m != nil {
		out.Metric = m[1]
	}
	if m := reInstance.FindStringSubmatch(q); m != nil {
		out.InstanceRef = m[1]
	}
	if m := reState.FindStringSubmatch(q); m != nil {
		out.State = m[1]
	}
	if m := reStart.FindStringSubmatch(q); m != nil {
		if t, err := time.Parse(oxqlTimeLayout, m[1]); err == nil {
			out.Start = t.UTC()
		}
	}
	if m := reEnd.FindStringSubmatch(q); m != nil {
		if t, err := time.Parse(oxqlTimeLayout, m[1]); err == nil {
			out.End = t.UTC()
		}
	}
	return out
}

// TimeframeFor picks the Proxmox RRD timeframe whose retention covers the
// requested span at a useful resolution.
func TimeframeFor(start, end time.Time) string {
	span := end.Sub(start)
	switch {
	case span <= 75*time.Minute:
		return "hour"
	case span <= 25*time.Hour:
		return "day"
	case span <= 8*24*time.Hour:
		return "week"
	case span <= 32*24*time.Hour:
		return "month"
	default:
		return "year"
	}
}

// cpuScale converts a CPU utilization fraction (0..1) into the raw value the
// console's utilization chart expects. The chart computes
// displayed% = value*100 / (5e9 * timeseriesCount); with one timeseries this
// yields fraction*100, so value = fraction * 5e9.
const cpuScale = 5e9

// MetricValue maps a Proxmox RRD point to the value for the requested metric.
// ok is false when Proxmox doesn't provide the underlying data (the chart then
// shows a gap rather than a misleading zero).
func MetricValue(metric, state string, point map[string]any) (float64, bool) {
	switch metric {
	case "virtual_machine:vcpu_usage":
		cpu, ok := pointFloat(point, "cpu")
		if !ok {
			return 0, false
		}
		switch state {
		case "idle":
			return (1 - cpu) * cpuScale, true
		case "emulation", "waiting":
			return 0, true
		default: // run, all, or unspecified
			return cpu * cpuScale, true
		}
	case "virtual_disk:bytes_read":
		return pointFloat(point, "diskread")
	case "virtual_disk:bytes_written":
		return pointFloat(point, "diskwrite")
	case "instance_network_interface:bytes_received":
		return pointFloat(point, "netin")
	case "instance_network_interface:bytes_sent":
		return pointFloat(point, "netout")
	default:
		// Counts (reads/writes/flushes/packets), latencies, errors: Proxmox RRD
		// doesn't expose these, so report no data.
		return 0, false
	}
}

func pointFloat(point map[string]any, key string) (float64, bool) {
	v, ok := point[key]
	if !ok {
		return 0, false
	}
	f, ok := v.(float64)
	return f, ok
}
