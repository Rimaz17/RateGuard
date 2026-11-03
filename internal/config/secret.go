package config

import "log/slog"

const redacted = "[REDACTED]"

// Secret holds a sensitive configuration value such as a connection string
// with a password in it. It formats, logs and marshals as "[REDACTED]", so
// printing or logging a Config cannot leak it; Value returns the real thing
// and is the only way to read it.
type Secret string

// Value returns the secret in clear text. Pass it straight to the library
// that needs it; never log or wrap it into an error.
func (s Secret) Value() string { return string(s) }

// String implements fmt.Stringer, which also covers %v and %s.
func (s Secret) String() string { return redacted }

// GoString implements fmt.GoStringer, which covers %#v.
func (s Secret) GoString() string { return redacted }

// LogValue implements slog.LogValuer.
func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalText implements encoding.TextMarshaler, which also covers JSON.
func (s Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }
