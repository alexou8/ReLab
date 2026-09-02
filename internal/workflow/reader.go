package workflow

import (
	"bytes"
	"io"
)

// newReader wraps the definition bytes. It exists so that Parse takes []byte
// while yaml.NewDecoder takes an io.Reader, without every caller building the
// reader itself.
func newReader(data []byte) io.Reader { return bytes.NewReader(data) }
