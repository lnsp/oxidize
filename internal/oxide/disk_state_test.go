package oxide

import (
	"encoding/json"
	"testing"
)

func TestDiskStateMarshal(t *testing.T) {
	cases := []struct {
		state DiskState
		want  string
	}{
		{Detached(), `{"state":"detached"}`},
		{Attached("abc"), `{"instance":"abc","state":"attached"}`},
		{DiskState{State: "creating"}, `{"state":"creating"}`},
		{DiskState{State: "detaching", Instance: "xyz"}, `{"instance":"xyz","state":"detaching"}`},
	}
	for _, c := range cases {
		b, err := json.Marshal(c.state)
		if err != nil {
			t.Fatalf("marshal %+v: %v", c.state, err)
		}
		if string(b) != c.want {
			t.Errorf("marshal %+v = %s want %s", c.state, b, c.want)
		}
	}
}

func TestPageNeverNull(t *testing.T) {
	b, _ := json.Marshal(Page([]Project(nil)))
	if string(b) != `{"items":[],"next_page":null}` {
		t.Errorf("empty page = %s; items must be [] not null", b)
	}
}
