package proxmox

import (
	"context"
	"fmt"
	"time"
)

// PollTask polls a UPID task until it stops or the timeout elapses. It returns
// nil if the task finished with exitstatus "OK", an error if it failed, and a
// timeout error if it was still running when the deadline passed. A timeout is
// not necessarily fatal for the caller — the operation may still be in flight.
func (c *Client) PollTask(ctx context.Context, node, upid string, timeout time.Duration) error {
	if upid == "" {
		return nil
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		var st TaskStatus
		path := fmt.Sprintf("nodes/%s/tasks/%s/status", node, upid)
		if err := c.Get(ctx, path, &st); err != nil {
			return err
		}
		if st.Status == "stopped" {
			if st.ExitStatus != "" && st.ExitStatus != "OK" {
				return &Error{Status: 500, Msg: "task failed: " + st.ExitStatus}
			}
			return nil
		}
		if time.Now().After(deadline) {
			return context.DeadlineExceeded
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
