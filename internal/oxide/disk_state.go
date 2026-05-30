package oxide

import "encoding/json"

// MarshalJSON renders DiskState as the Oxide tagged union. The attach-related
// states carry an `instance` field; all others are just `{ "state": "..." }`.
func (s DiskState) MarshalJSON() ([]byte, error) {
	switch s.State {
	case "attaching", "attached", "detaching":
		return json.Marshal(struct {
			Instance string `json:"instance"`
			State    string `json:"state"`
		}{Instance: s.Instance, State: s.State})
	default:
		return json.Marshal(struct {
			State string `json:"state"`
		}{State: s.State})
	}
}

// Attached builds an attached DiskState pointing at the given instance id.
func Attached(instanceID string) DiskState { return DiskState{State: "attached", Instance: instanceID} }

// Detached is the detached DiskState.
func Detached() DiskState { return DiskState{State: "detached"} }

// marshalIpStack renders a PrivateIpStack as the v4 variant of the Oxide union.
func marshalIpStack(v4 PrivateIpv4Stack) ([]byte, error) {
	if v4.TransitIps == nil {
		v4.TransitIps = []string{}
	}
	return json.Marshal(struct {
		Type  string           `json:"type"`
		Value PrivateIpv4Stack `json:"value"`
	}{Type: "v4", Value: v4})
}
