// Package transport owns the relay-v2 inbound ACK batcher.
//
// SUBSCRIBE pushes deliveries over the websocket, so draining a Subscription's local
// channel is neither a relay request nor a metered operation. ACK is still an explicit
// Worker operation; AckBatcher coalesces it off the delivery path at one per second.
package transport
