package providers

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"io"
	"testing"
)

// encodeEventStreamFrame builds a well-formed vnd.amazon.eventstream
// frame with string-typed headers around the given JSON payload. It is
// the inverse of eventStreamDecoder and is reused by the Bedrock stream
// test to feed a fake converse-stream response.
func encodeEventStreamFrame(headers map[string]string, payload []byte) []byte {
	var hb []byte
	for name, val := range headers {
		hb = append(hb, byte(len(name)))
		hb = append(hb, name...)
		hb = append(hb, esHdrString)
		var l [2]byte
		binary.BigEndian.PutUint16(l[:], uint16(len(val)))
		hb = append(hb, l[:]...)
		hb = append(hb, val...)
	}
	headersLen := len(hb)
	totalLen := 12 + headersLen + len(payload) + 4

	frame := make([]byte, 0, totalLen)
	var prelude [12]byte
	binary.BigEndian.PutUint32(prelude[0:4], uint32(totalLen))
	binary.BigEndian.PutUint32(prelude[4:8], uint32(headersLen))
	binary.BigEndian.PutUint32(prelude[8:12], crc32.ChecksumIEEE(prelude[0:8]))
	frame = append(frame, prelude[:]...)
	frame = append(frame, hb...)
	frame = append(frame, payload...)

	var crcb [4]byte
	binary.BigEndian.PutUint32(crcb[:], crc32.ChecksumIEEE(frame))
	frame = append(frame, crcb[:]...)
	return frame
}

func eventFrame(eventType, payload string) []byte {
	return encodeEventStreamFrame(map[string]string{
		":event-type":   eventType,
		":content-type": "application/json",
		":message-type": "event",
	}, []byte(payload))
}

func TestEventStreamDecoder_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(eventFrame("contentBlockDelta", `{"delta":{"text":"hi"}}`))
	buf.Write(eventFrame("messageStop", `{"stopReason":"end_turn"}`))

	dec := newEventStreamDecoder(&buf)

	first, err := dec.Next()
	if err != nil {
		t.Fatalf("first frame: %v", err)
	}
	if first.EventType != "contentBlockDelta" {
		t.Errorf("event-type = %q; want contentBlockDelta", first.EventType)
	}
	if first.MessageType != "event" {
		t.Errorf("message-type = %q; want event", first.MessageType)
	}
	if string(first.Payload) != `{"delta":{"text":"hi"}}` {
		t.Errorf("payload = %q", first.Payload)
	}

	second, err := dec.Next()
	if err != nil {
		t.Fatalf("second frame: %v", err)
	}
	if second.EventType != "messageStop" {
		t.Errorf("event-type = %q; want messageStop", second.EventType)
	}

	if _, err := dec.Next(); err != io.EOF {
		t.Errorf("expected io.EOF at end of stream, got %v", err)
	}
}

func TestEventStreamDecoder_BadMessageCRC(t *testing.T) {
	frame := eventFrame("contentBlockDelta", `{"delta":{"text":"x"}}`)
	// Corrupt the trailing message CRC.
	frame[len(frame)-1] ^= 0xFF

	dec := newEventStreamDecoder(bytes.NewReader(frame))
	if _, err := dec.Next(); err == nil {
		t.Fatal("expected a checksum error, got nil")
	}
}

func TestEventStreamDecoder_BadPreludeCRC(t *testing.T) {
	frame := eventFrame("messageStop", `{}`)
	// Corrupt a prelude byte (total-length) without fixing the prelude CRC.
	frame[0] ^= 0xFF

	dec := newEventStreamDecoder(bytes.NewReader(frame))
	if _, err := dec.Next(); err == nil {
		t.Fatal("expected a prelude checksum error, got nil")
	}
}

func TestEventStreamDecoder_TruncatedFrame(t *testing.T) {
	frame := eventFrame("metadata", `{"usage":{"inputTokens":1}}`)
	dec := newEventStreamDecoder(bytes.NewReader(frame[:len(frame)-3]))
	if _, err := dec.Next(); err != io.ErrUnexpectedEOF {
		t.Errorf("expected io.ErrUnexpectedEOF, got %v", err)
	}
}

func TestEventStreamDecoder_ExceptionHeader(t *testing.T) {
	frame := encodeEventStreamFrame(map[string]string{
		":message-type":   "exception",
		":exception-type": "throttlingException",
		":content-type":   "application/json",
	}, []byte(`{"message":"slow down"}`))

	dec := newEventStreamDecoder(bytes.NewReader(frame))
	msg, err := dec.Next()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.MessageType != "exception" || msg.ExceptionType != "throttlingException" {
		t.Errorf("got message-type=%q exception-type=%q", msg.MessageType, msg.ExceptionType)
	}
}
