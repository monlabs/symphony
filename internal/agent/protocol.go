package agent

// ProtocolMessage represents a JSON-RPC-like message from the app-server.
type ProtocolMessage struct {
	ID     interface{}            `json:"id,omitempty"`
	Method string                 `json:"method,omitempty"`
	Params map[string]interface{} `json:"params,omitempty"`
	Result map[string]interface{} `json:"result,omitempty"`
	Error  *ProtocolError         `json:"error,omitempty"`
}

// ProtocolError represents a JSON-RPC error object.
type ProtocolError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// IsResponse returns true if the message is a response (has an ID and no method).
func (m *ProtocolMessage) IsResponse() bool {
	return m.ID != nil && m.Method == ""
}

// IsNotification returns true if the message is a notification (has a method but no ID).
func (m *ProtocolMessage) IsNotification() bool {
	return m.ID == nil && m.Method != ""
}

// IsRequest returns true if the message is a request (has both ID and method).
func (m *ProtocolMessage) IsRequest() bool {
	return m.ID != nil && m.Method != ""
}
