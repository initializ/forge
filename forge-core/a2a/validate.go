package a2a

import (
	"errors"
	"fmt"
)

// Errors returned by Validate. Exposed as sentinels so callers can
// branch on them (e.g. emit a specific audit-event reason code or log
// a dedicated counter) without parsing error strings.
var (
	// ErrPartKindMissing is returned when a Part has empty Kind. This
	// is the canonical "you sent `type` instead of `kind`" signal —
	// the wrapped error message names the likely mistake when the
	// part has content despite the missing discriminator.
	ErrPartKindMissing = errors.New("part kind is required")

	// ErrPartKindUnknown is returned when a Part has a Kind value the
	// runtime doesn't recognize. A2A 0.3.0 defines text / data / file.
	ErrPartKindUnknown = errors.New("part kind is not one of text/data/file")

	// ErrMessageRoleMissing is returned when a Message has no role.
	ErrMessageRoleMissing = errors.New("message role is required")

	// ErrMessagePartsEmpty is returned when a Message has zero parts.
	// Some A2A clients emit empty parts arrays for heartbeat-style
	// pings; Forge rejects those at the entry point so the executor
	// never sees a message with nothing to interpret.
	ErrMessagePartsEmpty = errors.New("message parts must contain at least one element")
)

// Validate reports whether the Part conforms to the A2A 0.3.0 shape.
//
// The most common failure — and the one this method was added to
// surface clearly — is a Part that omits `kind`. encoding/json
// silently drops unknown fields, so a client sending the pre-0.3.0
// `type` discriminator gets a Part with Kind == "" and a populated
// content field (Text / Data / File). Without explicit validation
// the executor receives a part it can't classify and the LLM
// responds with something like "It looks like your message didn't
// come through" — confusing the caller about what actually went
// wrong. See issue #119.
//
// The returned error wraps the sentinel (ErrPartKindMissing or
// ErrPartKindUnknown) so callers can branch on the cause; the
// wrapped message names the likely mistake when content is present.
func (p Part) Validate() error {
	if p.Kind == "" {
		// A part with empty Kind but populated content didn't
		// arrive that way honestly — it almost certainly came from
		// a client sending the legacy `type` discriminator, which
		// encoding/json dropped on the floor. Name the likely
		// mistake in the error string so the operator's logs are
		// self-explanatory.
		if p.Text != "" || p.Data != nil || p.File != nil {
			return fmt.Errorf(`%w (A2A 0.3.0); got empty kind with non-empty content — did you send "type" instead of "kind"? "type" is from the pre-0.3.0 dialect and is silently ignored by the decoder`, ErrPartKindMissing)
		}
		return fmt.Errorf("%w (A2A 0.3.0); got empty part", ErrPartKindMissing)
	}
	switch p.Kind {
	case PartKindText, PartKindData, PartKindFile:
		// Recognized kind. We intentionally do NOT require the
		// matching content field to be non-empty — an empty text
		// part is a legitimate A2A shape for placeholder /
		// streaming-warmup turns and rejecting it would be
		// over-strict.
		return nil
	default:
		return fmt.Errorf("%w; got %q", ErrPartKindUnknown, p.Kind)
	}
}

// Validate reports whether the Message conforms to the A2A 0.3.0
// shape. Role must be non-empty, Parts must be non-empty, and every
// part must Validate cleanly. The first failing part's error is
// returned (wrapped with its index) so the caller can name the
// offending position in their HTTP / JSON-RPC error message.
func (m Message) Validate() error {
	if m.Role == "" {
		return ErrMessageRoleMissing
	}
	if len(m.Parts) == 0 {
		return ErrMessagePartsEmpty
	}
	for i := range m.Parts {
		if err := m.Parts[i].Validate(); err != nil {
			return fmt.Errorf("parts[%d]: %w", i, err)
		}
	}
	return nil
}
