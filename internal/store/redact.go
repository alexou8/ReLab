package store

import (
	"errors"
	"net/url"
	"strings"
)

// redact rewrites an error message so that it cannot carry the database
// password. pgx echoes the connection string in several of its failure modes,
// and those messages reach logs and CLI output.
//
// It replaces both the whole DSN and, for URL-form DSNs, the password alone —
// a truncated or reformatted DSN inside a driver message would otherwise slip
// past a whole-string match.
func redact(err error, dsn string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	replaced := msg
	if dsn != "" {
		replaced = strings.ReplaceAll(replaced, dsn, "<dsn>")
	}
	if pw, ok := passwordOf(dsn); ok {
		replaced = strings.ReplaceAll(replaced, pw, "<redacted>")
	}
	if replaced == msg {
		return err
	}
	return errors.New(replaced)
}

// passwordOf extracts the password from a URL-form DSN, and from the
// password= key of a keyword/value DSN.
func passwordOf(dsn string) (string, bool) {
	if u, err := url.Parse(dsn); err == nil && u.User != nil {
		if pw, set := u.User.Password(); set && pw != "" {
			return pw, true
		}
	}
	for _, field := range strings.Fields(dsn) {
		if after, ok := strings.CutPrefix(field, "password="); ok && after != "" {
			return after, true
		}
	}
	return "", false
}
