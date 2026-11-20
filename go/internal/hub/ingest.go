package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"agent-pulse/protocol"
)

func (h *Hub) acceptBatch(ctx context.Context, source string, batch protocol.EventBatch) protocol.BatchReceipt {
	out := protocol.BatchReceipt{Results: []protocol.EventResult{}}
	for _, e := range batch.Events {
		result := protocol.EventResult{EventID: e.EventID, Status: 400}
		raw, encodeErr := json.Marshal(e)
		if e.SourceID == source && e.Validate() == nil && encodeErr == nil && len(raw) <= protocol.MaxEventBytes {
			duplicate, err := h.Accept(ctx, e)
			switch {
			case err == nil:
				result.Status = 200
				result.Accepted = "durable"
				result.Duplicate = duplicate
			case errors.Is(err, ErrConflict):
				result.Status = 409
			case errors.Is(err, ErrHostDeleted):
				result.Status = 410
			default:
				result.Status = http.StatusServiceUnavailable
			}
		}
		out.Results = append(out.Results, result)
	}
	return out
}
