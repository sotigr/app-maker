package api

import "context"

// Transcriber turns recorded speech into text (ADR 0028: dictation in UI
// text fields). Audio and text are never stored.
type Transcriber interface {
	// Transcribe returns the text spoken in audio (contentType e.g.
	// "audio/webm"); lang is an ISO 639-1 code ("el") or empty.
	Transcribe(ctx context.Context, audio []byte, contentType, lang string) (Transcript, error)
}

// Transcript is a transcription result.
type Transcript struct {
	Text    string  `json:"text"`
	Seconds float64 `json:"seconds,omitempty"` // audio duration when the provider reports it
}
