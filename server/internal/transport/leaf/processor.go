package leaf

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"unicode/utf8"

	"github.com/taills/leaf/v2/network"
)

const MaxMessageBytes = 65_536

var ErrMessageTooLarge = errors.New("protocol message exceeds 65536 bytes")

type MessageID uint8

const (
	MessageHello    MessageID = 1
	MessageHelloAck MessageID = 2
	MessagePing     MessageID = 3
	MessagePong     MessageID = 4
)

var messageTypes = map[MessageID]string{
	MessageHello:    "system.hello",
	MessageHelloAck: "system.hello_ack",
	MessagePing:     "system.ping",
	MessagePong:     "system.pong",
}

var messageIDs = map[string]MessageID{
	"system.hello":     MessageHello,
	"system.hello_ack": MessageHelloAck,
	"system.ping":      MessagePing,
	"system.pong":      MessagePong,
}

type Request struct {
	ID        MessageID
	Type      string
	RequestID string
	Payload   map[string]any
}

type Response struct {
	ID           MessageID `json:"-"`
	RequestID    string    `json:"request_id"`
	Success      bool      `json:"success"`
	ServerSeq    string    `json:"server_seq"`
	StateVersion *string   `json:"state_version"`
	Data         any       `json:"data"`
	Error        any       `json:"error"`
	Meta         any       `json:"meta"`
}

type responseEnvelope struct {
	ProtocolVersion string  `json:"protocol_version"`
	RequestID       string  `json:"request_id"`
	Type            string  `json:"type"`
	Success         bool    `json:"success"`
	ServerSeq       string  `json:"server_seq"`
	StateVersion    *string `json:"state_version"`
	Data            any     `json:"data"`
	Error           any     `json:"error"`
	Meta            any     `json:"meta"`
}

type wireRequest struct {
	ProtocolVersion string          `json:"protocol_version"`
	RequestID       string          `json:"request_id"`
	Type            string          `json:"type"`
	Payload         json.RawMessage `json:"payload"`
}

type Processor struct{}

var _ network.Processor = (*Processor)(nil)

func NewProcessor() *Processor { return &Processor{} }

func (*Processor) Unmarshal(data []byte) (interface{}, error) {
	if len(data) > MaxMessageBytes {
		return nil, ErrMessageTooLarge
	}
	if !utf8.Valid(data) {
		return nil, errors.New("protocol message is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var envelope wireRequest
	if err := decoder.Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decode protocol request: %w", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return nil, err
	}
	if envelope.ProtocolVersion != "1" {
		return nil, fmt.Errorf("unsupported protocol version %q", envelope.ProtocolVersion)
	}
	if envelope.RequestID == "" || utf8.RuneCountInString(envelope.RequestID) > 128 {
		return nil, errors.New("request_id must contain 1 to 128 characters")
	}
	id, ok := messageIDs[envelope.Type]
	if !ok || (id != MessageHello && id != MessagePing) {
		return nil, fmt.Errorf("unknown client message type %q", envelope.Type)
	}
	var fields map[string]any
	if len(envelope.Payload) == 0 || string(envelope.Payload) == "null" {
		return nil, errors.New("payload must be an object")
	}
	if err := json.Unmarshal(envelope.Payload, &fields); err != nil || fields == nil {
		return nil, errors.New("payload must be an object")
	}
	if err := validatePayload(id, fields); err != nil {
		return nil, err
	}
	return Request{ID: id, Type: envelope.Type, RequestID: envelope.RequestID, Payload: fields}, nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("protocol message contains multiple JSON values")
		}
		return fmt.Errorf("decode trailing protocol data: %w", err)
	}
	return nil
}

func isUnsignedDecimal(value string) bool {
	if value == "0" {
		return true
	}
	if value == "" || value[0] < '1' || value[0] > '9' {
		return false
	}
	for _, digit := range value[1:] {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func isNilValue(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func isJSONObject(value any) bool {
	encoded, err := json.Marshal(value)
	if err != nil {
		return false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		return false
	}
	return object != nil
}

func validatePayload(id MessageID, payload map[string]any) error {
	allowed := []string{"nonce"}
	if id == MessageHello {
		allowed = []string{"game_id", "realm_id", "client_build", "release_id", "session_token", "capabilities"}
	}
	for field := range payload {
		if !containsField(allowed, field) {
			return fmt.Errorf("payload.%s is not allowed", field)
		}
	}
	for _, field := range allowed {
		value, ok := payload[field]
		if !ok {
			return fmt.Errorf("payload.%s is required", field)
		}
		if id == MessageHello && field == "capabilities" {
			capabilities, ok := value.([]any)
			if !ok || len(capabilities) == 0 {
				return errors.New("payload.capabilities must be a non-empty array")
			}
			for _, capability := range capabilities {
				if item, ok := capability.(string); !ok || item == "" {
					return errors.New("payload.capabilities entries must be non-empty strings")
				}
			}
			continue
		}
		text, ok := value.(string)
		if !ok || text == "" {
			return fmt.Errorf("payload.%s must be a non-empty string", field)
		}
	}
	return nil
}

func containsField(fields []string, field string) bool {
	for _, candidate := range fields {
		if field == candidate {
			return true
		}
	}
	return false
}

func (*Processor) Route(message interface{}, userData interface{}) error {
	request, ok := message.(Request)
	if !ok {
		return fmt.Errorf("unsupported protocol message value %T", message)
	}
	agent, ok := userData.(*Agent)
	if !ok || agent == nil {
		return errors.New("protocol route requires a Leaf agent")
	}
	return agent.handle(request)
}

func (*Processor) Marshal(message interface{}) ([][]byte, error) {
	response, ok := message.(Response)
	if !ok {
		return nil, fmt.Errorf("unsupported protocol response value %T", message)
	}
	typeName, ok := messageTypes[response.ID]
	if !ok || (response.ID != MessageHelloAck && response.ID != MessagePong) {
		return nil, fmt.Errorf("unknown server message ID %d", response.ID)
	}
	if response.RequestID == "" || utf8.RuneCountInString(response.RequestID) > 128 {
		return nil, errors.New("response request_id must contain 1 to 128 characters")
	}
	if !isUnsignedDecimal(response.ServerSeq) {
		return nil, errors.New("response server_seq must be an unsigned decimal string")
	}
	if response.StateVersion != nil && !isUnsignedDecimal(*response.StateVersion) {
		return nil, errors.New("response state_version must be an unsigned decimal string")
	}
	data := response.Data
	if isNilValue(data) {
		data = map[string]any{}
	}
	meta := response.Meta
	if isNilValue(meta) {
		meta = map[string]any{}
	}
	if !isJSONObject(data) {
		return nil, errors.New("response data must be an object")
	}
	if !isJSONObject(meta) {
		return nil, errors.New("response meta must be an object")
	}
	if !isNilValue(response.Error) && !isJSONObject(response.Error) {
		return nil, errors.New("response error must be an object or null")
	}
	encoded, err := json.Marshal(responseEnvelope{
		ProtocolVersion: "1",
		RequestID:       response.RequestID,
		Type:            typeName,
		Success:         response.Success,
		ServerSeq:       response.ServerSeq,
		StateVersion:    response.StateVersion,
		Data:            data,
		Error:           response.Error,
		Meta:            meta,
	})
	if err != nil {
		return nil, fmt.Errorf("encode protocol response: %w", err)
	}
	if len(encoded) > MaxMessageBytes {
		return nil, ErrMessageTooLarge
	}
	return [][]byte{encoded}, nil
}
