// Package runtime 是模块化单体游戏进程的核心：每个 Realm 一个单写者事件循环、
// 协议注册表、玩家会话、owner epoch 守卫与 PostgreSQL 事务入口。
package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"unicode/utf8"
)

const MaxMessageBytes = 65_536

// Catalog 是从 contracts/protocol/message_catalog.json 加载的消息注册表。
type Catalog struct {
	ProtocolVersion string
	responses       map[string]string // request type -> response type
	required        map[string][]string
	optional        map[string][]string
	serverTypes     map[string]bool
}

type catalogFile struct {
	ProtocolVersion string `json:"protocol_version"`
	Transport       struct {
		MaxMessageBytes int `json:"max_message_bytes"`
	} `json:"transport"`
	Messages []struct {
		Type     string   `json:"type"`
		Dir      string   `json:"direction"`
		Required []string `json:"required_payload_fields"`
		Optional []string `json:"optional_payload_fields"`
		Response string   `json:"response_type"`
	} `json:"messages"`
}

// LoadCatalog 读取受信任的协议目录。
func LoadCatalog(path string) (*Catalog, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- 路径来自部署配置的受信任 contracts 目录。
	if err != nil {
		return nil, fmt.Errorf("read protocol catalog: %w", err)
	}
	var file catalogFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("decode protocol catalog: %w", err)
	}
	if file.ProtocolVersion != "1" || file.Transport.MaxMessageBytes != MaxMessageBytes {
		return nil, errors.New("protocol catalog version or frame limit mismatch")
	}
	catalog := &Catalog{ProtocolVersion: file.ProtocolVersion, responses: map[string]string{}, required: map[string][]string{},
		optional: map[string][]string{}, serverTypes: map[string]bool{}}
	for _, message := range file.Messages {
		switch message.Dir {
		case "client_to_server":
			catalog.responses[message.Type] = message.Response
			catalog.required[message.Type] = message.Required
			catalog.optional[message.Type] = message.Optional
		case "server_to_client":
			catalog.serverTypes[message.Type] = true
		default:
			return nil, fmt.Errorf("protocol catalog message %q has invalid direction", message.Type)
		}
	}
	for request, response := range catalog.responses {
		if response == "" || !catalog.serverTypes[response] {
			return nil, fmt.Errorf("protocol catalog request %q has no server response type", request)
		}
	}
	return catalog, nil
}

// Request 是已通过信封与字段白名单校验的客户端请求。
type Request struct {
	Type      string
	RequestID string
	Payload   json.RawMessage
}

type wireRequest struct {
	ProtocolVersion string          `json:"protocol_version"`
	RequestID       string          `json:"request_id"`
	Type            string          `json:"type"`
	Payload         json.RawMessage `json:"payload"`
}

// Decode 校验信封、目录中的类型与字段白名单；字段值由各处理器严格解码。
func (c *Catalog) Decode(data []byte) (Request, error) {
	if len(data) > MaxMessageBytes {
		return Request{}, errors.New("protocol message exceeds 65536 bytes")
	}
	if !utf8.Valid(data) {
		return Request{}, errors.New("protocol message is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var envelope wireRequest
	if err := decoder.Decode(&envelope); err != nil {
		return Request{}, fmt.Errorf("decode protocol request: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Request{}, errors.New("protocol message contains trailing data")
	}
	if envelope.ProtocolVersion != c.ProtocolVersion {
		return Request{}, fmt.Errorf("unsupported protocol version %q", envelope.ProtocolVersion)
	}
	if envelope.RequestID == "" || utf8.RuneCountInString(envelope.RequestID) > 128 {
		return Request{}, errors.New("request_id must contain 1 to 128 characters")
	}
	if _, ok := c.responses[envelope.Type]; !ok {
		return Request{}, fmt.Errorf("unknown client message type %q", envelope.Type)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Payload, &fields); err != nil || fields == nil {
		return Request{}, errors.New("payload must be an object")
	}
	allowed := map[string]bool{}
	for _, field := range c.required[envelope.Type] {
		allowed[field] = true
		if _, ok := fields[field]; !ok {
			return Request{}, fmt.Errorf("payload.%s is required", field)
		}
	}
	for _, field := range c.optional[envelope.Type] {
		allowed[field] = true
	}
	for field := range fields {
		if !allowed[field] {
			return Request{}, fmt.Errorf("payload.%s is not allowed", field)
		}
	}
	return Request{Type: envelope.Type, RequestID: envelope.RequestID, Payload: envelope.Payload}, nil
}

// ResponseType 返回请求对应的响应类型。
func (c *Catalog) ResponseType(requestType string) string { return c.responses[requestType] }

// IsServerType 判断类型是否为目录中的服务端消息。
func (c *Catalog) IsServerType(messageType string) bool { return c.serverTypes[messageType] }

// ProtocolError 是返回给客户端的稳定错误。
type ProtocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *ProtocolError) Error() string { return e.Code + ": " + e.Message }

// Fail 构造协议错误。
func Fail(code, message string) *ProtocolError { return &ProtocolError{Code: code, Message: message} }

type responseEnvelope struct {
	ProtocolVersion string         `json:"protocol_version"`
	RequestID       string         `json:"request_id"`
	Type            string         `json:"type"`
	Success         bool           `json:"success"`
	ServerSeq       string         `json:"server_seq"`
	StateVersion    *string        `json:"state_version"`
	Data            any            `json:"data"`
	Error           *ProtocolError `json:"error"`
	Meta            map[string]any `json:"meta"`
}

func encodeResponse(protocolVersion, requestID, messageType string, seq uint64, stateVersion int64, data any, failure *ProtocolError) ([]byte, error) {
	if data == nil {
		data = map[string]any{}
	}
	var version *string
	if stateVersion > 0 {
		text := strconv.FormatInt(stateVersion, 10)
		version = &text
	}
	encoded, err := json.Marshal(responseEnvelope{ProtocolVersion: protocolVersion, RequestID: requestID, Type: messageType,
		Success: failure == nil, ServerSeq: strconv.FormatUint(seq, 10), StateVersion: version, Data: data, Error: failure, Meta: map[string]any{}})
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxMessageBytes {
		return nil, errors.New("protocol response exceeds 65536 bytes")
	}
	return encoded, nil
}

// DecodeStrict 严格解码处理器载荷（拒绝未知字段）。
func DecodeStrict(payload json.RawMessage, target any) *ProtocolError {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return Fail("invalid_payload", "请求载荷格式无效")
	}
	return nil
}
