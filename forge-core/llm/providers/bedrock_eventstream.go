package providers

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
)

// AWS `application/vnd.amazon.eventstream` framing — the binary envelope
// Bedrock's converse-stream (and every other AWS event-stream API) uses.
// Hand-rolled here for the same reason the SigV4 signer is
// (sigv4_transport.go): keep the Bedrock path free of aws-sdk-go-v2.
//
// Each frame is laid out as:
//
//	┌────────────────── prelude (12 bytes) ──────────────────┐
//	│ total length (uint32) │ headers length (uint32) │ CRC32 │
//	├──────────────────── headers (variable) ────────────────┤
//	├──────────────────── payload (variable) ────────────────┤
//	└──────────────────── message CRC32 (uint32) ────────────┘
//
// total length counts the whole frame including the 4-byte trailing CRC.
// The prelude CRC covers the first 8 bytes; the message CRC covers every
// byte up to (but not including) itself. Both use the IEEE polynomial.
//
// Each header is: 1-byte name length, name, 1-byte value type, value.
// Only string headers (type 7) are surfaced — those carry the
// `:event-type` / `:message-type` / `:exception-type` we route on.
type eventStreamMessage struct {
	EventType     string // :event-type   (contentBlockDelta, messageStop, …)
	MessageType   string // :message-type (event | exception | error)
	ContentType   string // :content-type (application/json)
	ExceptionType string // :exception-type, set on modeled errors
	Headers       map[string]string
	Payload       []byte
}

// maxEventStreamFrame caps a single frame's total byte length. AWS caps
// Converse event-stream messages around 24 MB; this bound rejects a
// hostile prelude before allocating on its untrusted length.
const maxEventStreamFrame = 32 * 1024 * 1024

type eventStreamDecoder struct {
	r io.Reader
}

func newEventStreamDecoder(r io.Reader) *eventStreamDecoder {
	return &eventStreamDecoder{r: r}
}

// Next reads and validates the next frame. It returns io.EOF at a clean
// frame boundary (end of stream), io.ErrUnexpectedEOF on a truncated
// frame, and a descriptive error on any checksum/length violation.
func (d *eventStreamDecoder) Next() (*eventStreamMessage, error) {
	var prelude [12]byte
	if _, err := io.ReadFull(d.r, prelude[:]); err != nil {
		// io.EOF here means the stream ended exactly on a frame boundary.
		return nil, err
	}

	totalLen := binary.BigEndian.Uint32(prelude[0:4])
	headersLen := binary.BigEndian.Uint32(prelude[4:8])
	preludeCRC := binary.BigEndian.Uint32(prelude[8:12])

	if crc32.ChecksumIEEE(prelude[0:8]) != preludeCRC {
		return nil, fmt.Errorf("eventstream: prelude checksum mismatch")
	}
	// A frame is at minimum prelude(12) + message CRC(4). headersLen must
	// leave room for the trailing CRC.
	if totalLen < 16 || uint64(headersLen) > uint64(totalLen)-16 {
		return nil, fmt.Errorf("eventstream: invalid frame lengths (total=%d headers=%d)", totalLen, headersLen)
	}
	// Bound the allocation on the untrusted wire length before make(): a
	// prelude with a valid CRC but totalLen ~4 GB would otherwise allocate
	// multiple GB before any payload is read (memory-exhaustion DoS reachable
	// via a malicious/compromised endpoint or a base_url override). AWS caps
	// Converse event-stream messages well under this bound.
	if totalLen > maxEventStreamFrame {
		return nil, fmt.Errorf("eventstream: frame too large (%d > %d)", totalLen, maxEventStreamFrame)
	}

	rest := make([]byte, totalLen-12)
	if _, err := io.ReadFull(d.r, rest); err != nil {
		if err == io.EOF {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}

	msgCRC := binary.BigEndian.Uint32(rest[len(rest)-4:])
	h := crc32.NewIEEE()
	_, _ = h.Write(prelude[:])
	_, _ = h.Write(rest[:len(rest)-4])
	if h.Sum32() != msgCRC {
		return nil, fmt.Errorf("eventstream: message checksum mismatch")
	}

	headerBytes := rest[:headersLen]
	payload := rest[headersLen : len(rest)-4]

	msg := &eventStreamMessage{Headers: map[string]string{}, Payload: payload}
	if err := parseEventStreamHeaders(headerBytes, msg); err != nil {
		return nil, err
	}
	return msg, nil
}

// header value type tags (AWS event-stream spec).
const (
	esHdrBoolTrue  = 0
	esHdrBoolFalse = 1
	esHdrByte      = 2
	esHdrShort     = 3
	esHdrInt       = 4
	esHdrLong      = 5
	esHdrBytes     = 6
	esHdrString    = 7
	esHdrTimestamp = 8
	esHdrUUID      = 9
)

// parseEventStreamHeaders walks the header block. Non-string values are
// length-skipped (Bedrock only sends string headers on the frames we
// care about, but the walk must stay byte-accurate to reach the ones it
// does need). A malformed header block is an error rather than silently
// truncated so a framing bug surfaces instead of dropping events.
func parseEventStreamHeaders(b []byte, msg *eventStreamMessage) error {
	i := 0
	for i < len(b) {
		nameLen := int(b[i])
		i++
		if i+nameLen > len(b) {
			return fmt.Errorf("eventstream: truncated header name")
		}
		name := string(b[i : i+nameLen])
		i += nameLen
		if i >= len(b) {
			return fmt.Errorf("eventstream: missing header value type")
		}
		valType := b[i]
		i++

		var strVal string
		switch valType {
		case esHdrBoolTrue, esHdrBoolFalse:
			// no value bytes
		case esHdrByte:
			i += 1
		case esHdrShort:
			i += 2
		case esHdrInt:
			i += 4
		case esHdrLong, esHdrTimestamp:
			i += 8
		case esHdrUUID:
			i += 16
		case esHdrBytes, esHdrString:
			if i+2 > len(b) {
				return fmt.Errorf("eventstream: truncated header value length")
			}
			l := int(binary.BigEndian.Uint16(b[i : i+2]))
			i += 2
			if i+l > len(b) {
				return fmt.Errorf("eventstream: truncated header value")
			}
			if valType == esHdrString {
				strVal = string(b[i : i+l])
			}
			i += l
		default:
			return fmt.Errorf("eventstream: unknown header value type %d", valType)
		}
		if i > len(b) {
			return fmt.Errorf("eventstream: header value overruns block")
		}

		if valType == esHdrString {
			msg.Headers[name] = strVal
			switch name {
			case ":event-type":
				msg.EventType = strVal
			case ":message-type":
				msg.MessageType = strVal
			case ":content-type":
				msg.ContentType = strVal
			case ":exception-type":
				msg.ExceptionType = strVal
			}
		}
	}
	return nil
}
