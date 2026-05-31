package translate

import (
	"time"

	"github.com/lnsp/oxidize/internal/oxide"
)

// AffinityGroupRecord is the minimal view of a persisted affinity/anti-affinity
// group needed to render its Oxide wire shape. It mirrors store.AffinityGroup
// but lives here so this package stays free of an internal/store import — store
// derives group ids via AffinityGroupID, so importing it back would cycle.
type AffinityGroupRecord struct {
	ID            string
	Name          string
	Description   string
	ProjectID     string
	Policy        string
	FailureDomain string
	TimeCreated   time.Time
	TimeModified  time.Time
}

// AffinityGroupToOxide maps a persisted group record to its Oxide wire struct.
// The same shape serves both kinds (AffinityGroup and AntiAffinityGroup are
// structurally identical in the Oxide API).
func AffinityGroupToOxide(g AffinityGroupRecord) oxide.AffinityGroup {
	return oxide.AffinityGroup{
		ID:            g.ID,
		Name:          g.Name,
		Description:   g.Description,
		Policy:        g.Policy,
		FailureDomain: g.FailureDomain,
		ProjectID:     g.ProjectID,
		TimeCreated:   g.TimeCreated,
		TimeModified:  g.TimeModified,
	}
}
