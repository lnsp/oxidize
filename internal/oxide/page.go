package oxide

// ResultsPage is the Oxide list envelope: { "items": [...], "next_page": null }.
type ResultsPage[T any] struct {
	Items    []T     `json:"items"`
	NextPage *string `json:"next_page"`
}

// Page wraps a slice as a single-page result. Homelab-scale lists are small, so
// we return everything at once and never set a next-page token. Items is
// normalized to a non-nil empty slice so the JSON is `[]`, not `null`.
func Page[T any](items []T) ResultsPage[T] {
	if items == nil {
		items = []T{}
	}
	return ResultsPage[T]{Items: items, NextPage: nil}
}
