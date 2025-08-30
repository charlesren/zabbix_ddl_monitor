package connection

import (
	"context"
)

// Protocol driver interfaces and types
type ProtocolDriver interface {
	ProtocolType() Protocol
	Close() error
	Execute(ctx context.Context, req *ProtocolRequest) (*ProtocolResponse, error)
	GetCapability() ProtocolCapability
}

type ProtocolRequest struct {
	CommandType CommandType // commands/interactive_event
	Payload     interface{} // []string 或 []*channel.SendInteractiveEvent
}

type ProtocolResponse struct {
	Success    bool
	RawData    []byte
	Structured interface{} // *response.Response 或 *response.MultiResponse
}
