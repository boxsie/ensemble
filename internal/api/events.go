package api

import (
	"context"
	"encoding/json"
	"fmt"

	apipb "github.com/boxsie/ensemble/api/pb"
	"github.com/boxsie/ensemble/internal/node"
	"google.golang.org/grpc"
)

// eventTypeNames maps internal event types to string names for the gRPC stream.
var eventTypeNames = map[node.EventType]string{
	node.EventTorBootstrap:    "tor_bootstrap",
	node.EventTorReady:        "tor_ready",
	node.EventPeerDiscovered:  "peer_discovered",
	node.EventConnectionReq:   "connection_request",
	node.EventConnectionState: "connection_state",
	node.EventChatMessage:     "chat_message",
	node.EventFileOffer:       "file_offer",
	node.EventFileProgress:    "file_progress",
	node.EventPeerOnline:      "peer_online",
	node.EventError:           "error",
}

// streamEvents subscribes to all event types and forwards them to the gRPC stream.
// Blocks until the stream context is cancelled (client disconnects).
func streamEvents(bus *node.EventBus, stream grpc.ServerStreamingServer[apipb.DaemonEvent]) error {
	ctx := stream.Context()
	merged := bus.SubscribeAll(ctx)
	return forwardToStream(ctx, merged, stream)
}

// forwardToStream reads events from the merged channel and sends them as gRPC messages.
func forwardToStream(ctx context.Context, events <-chan node.Event, stream grpc.ServerStreamingServer[apipb.DaemonEvent]) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e := <-events:
			msg, err := eventToProto(e)
			if err != nil {
				continue
			}
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
	}
}

// eventToProto converts an internal event to a gRPC DaemonEvent message.
func eventToProto(e node.Event) (*apipb.DaemonEvent, error) {
	name := eventTypeNames[e.Type]
	if name == "" {
		name = fmt.Sprintf("unknown_%d", e.Type)
	}

	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return nil, fmt.Errorf("marshalling event payload: %w", err)
	}

	return &apipb.DaemonEvent{
		Type:    name,
		Payload: string(payload),
	}, nil
}
