package httptransport

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"
)

// maxBodyBytes bounds every request body. Admin requests are a few hundred
// bytes; anything near this is a mistake or an attack.
const maxBodyBytes = 64 << 10

// requestError is a problem with the request itself. Its message is
// written for the client and is safe to return as is.
type requestError struct {
	status  int
	message string
}

func (e *requestError) Error() string { return e.message }

func badRequest(format string, args ...any) *requestError {
	return &requestError{status: http.StatusBadRequest, message: fmt.Sprintf(format, args...)}
}

// decodeJSON reads exactly one JSON object from the body into dst. The body
// is capped at maxBodyBytes, unknown fields are rejected so a typo cannot
// pass silently, and trailing data after the object is an error.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) *requestError {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mediaType, _, err := mime.ParseMediaType(ct)
		if err != nil || mediaType != "application/json" {
			return &requestError{status: http.StatusUnsupportedMediaType, message: "Content-Type must be application/json"}
		}
	}

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return decodeError(err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return decodeError(err)
		}
		return badRequest("request body must contain a single JSON object")
	}
	return nil
}

func decodeError(err error) *requestError {
	var (
		syntax    *json.SyntaxError
		typeErr   *json.UnmarshalTypeError
		tooLarge  *http.MaxBytesError
		unknownAt = "json: unknown field "
	)
	switch {
	case errors.Is(err, io.EOF):
		return badRequest("request body is required")
	case errors.As(err, &tooLarge):
		return &requestError{status: http.StatusRequestEntityTooLarge, message: fmt.Sprintf("request body must be at most %d bytes", maxBodyBytes)}
	case errors.As(err, &syntax), errors.Is(err, io.ErrUnexpectedEOF):
		return badRequest("request body is not valid JSON")
	case errors.As(err, &typeErr):
		if typeErr.Field == "" {
			return badRequest("request body must be a JSON object")
		}
		return badRequest("field %q has the wrong type", typeErr.Field)
	case strings.HasPrefix(err.Error(), unknownAt):
		// encoding/json has no typed error for this; the field name it
		// quotes came from the client.
		return badRequest("unknown field %s", strings.TrimPrefix(err.Error(), unknownAt))
	default:
		return badRequest("request body could not be read")
	}
}

// newValidator reports fields by their JSON names, which is what the
// client sent.
func newValidator() *validator.Validate {
	v := validator.New(validator.WithRequiredStructEnabled())
	v.RegisterTagNameFunc(func(f reflect.StructField) string {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			return ""
		}
		return name
	})
	return v
}

// validateRequest checks dst's validate tags and turns the first failure
// into a client-facing message.
func validateRequest(v *validator.Validate, dst any) *requestError {
	err := v.Struct(dst)
	if err == nil {
		return nil
	}
	var fieldErrs validator.ValidationErrors
	if !errors.As(err, &fieldErrs) || len(fieldErrs) == 0 {
		return badRequest("request is invalid")
	}
	fe := fieldErrs[0]
	switch fe.Tag() {
	case "required":
		return badRequest("%s is required", fe.Field())
	case "max":
		return badRequest("%s must be at most %s characters", fe.Field(), fe.Param())
	default:
		return badRequest("%s is invalid", fe.Field())
	}
}
