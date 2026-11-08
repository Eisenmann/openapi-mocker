package domain

// StreamStep is one entry of a stream timeline: after DelayMs it emits Data,
// Repeat times in all, IntervalMs apart. It serves Server-Sent Events,
// WebSocket messages and GraphQL subscription events alike.
type StreamStep struct {
	// DelayMs is the wait before the first emission of this step.
	DelayMs int `json:"delayMs,omitempty"`
	// Event is the SSE event name (ignored by WebSocket).
	Event string `json:"event,omitempty"`
	// ID is the SSE event id (ignored by WebSocket).
	ID string `json:"id,omitempty"`
	// Data is the payload. It may contain {{placeholders}}, which are filled
	// in anew on every emission, so repeated events differ.
	Data string `json:"data"`
	// Repeat is how many times the step is emitted; 0 and 1 mean once.
	Repeat int `json:"repeat,omitempty"`
	// IntervalMs separates the repetitions.
	IntervalMs int `json:"intervalMs,omitempty"`
	// Encoding says how Data is turned into a WebSocket message: "text" (the
	// default) sends it as a text frame; "base64" and "hex" decode it and send
	// a binary frame. SSE, gRPC and GraphQL ignore it.
	Encoding string `json:"encoding,omitempty"`
}

// Step encodings.
const (
	StepEncodingText   = "text"
	StepEncodingBase64 = "base64"
	StepEncodingHex    = "hex"
)

// StreamScript is a Server-Sent Events timeline.
type StreamScript struct {
	Steps []StreamStep `json:"steps"`
	// Loop restarts the timeline after its last step, until the client leaves.
	Loop bool `json:"loop,omitempty"`
	// RetryMs is sent to the client as the SSE "retry:" reconnection delay.
	RetryMs int `json:"retryMs,omitempty"`
}

// WebSocket match types.
const (
	WSMatchContains = "contains"
	WSMatchEquals   = "equals"
	WSMatchRegex    = "regex"
	// WSMatchJSON matches when every top-level field of the JSON pattern is
	// present, with the same value, in the received JSON message.
	WSMatchJSON = "json"
)

// WebSocket fallbacks, for messages no rule matches.
const (
	WSFallbackEcho   = "echo"
	WSFallbackIgnore = "ignore"
	WSFallbackClose  = "close"
)

// WSMatch tests a message received from the client.
type WSMatch struct {
	Type    string `json:"type"`
	Pattern string `json:"pattern"`
}

// WSRule replies to the messages it matches.
type WSRule struct {
	Match WSMatch `json:"match"`
	// Reply is the timeline of messages sent back; it may be empty.
	Reply []StreamStep `json:"reply,omitempty"`
	// Close ends the connection after the reply.
	Close bool `json:"close,omitempty"`
}

// WSScript describes the behaviour of a mocked WebSocket endpoint.
type WSScript struct {
	// OnConnect is sent as soon as the client has connected.
	OnConnect []StreamStep `json:"onConnect,omitempty"`
	// Loop repeats OnConnect until the client leaves.
	Loop bool `json:"loop,omitempty"`
	// Rules are tried in order for every message received.
	Rules []WSRule `json:"rules,omitempty"`
	// Fallback is what happens to a message no rule matches: "echo" (the
	// default), "ignore" or "close".
	Fallback string `json:"fallback,omitempty"`
	// PingIntervalMs makes the server send WebSocket pings.
	PingIntervalMs int `json:"pingIntervalMs,omitempty"`
	// CloseAfterMs closes the connection normally after this long.
	CloseAfterMs int `json:"closeAfterMs,omitempty"`
}
