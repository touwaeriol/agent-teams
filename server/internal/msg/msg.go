// Package msg carries user-facing text as a translatable message: a key (the Simplified Chinese
// source text with named {placeholders}, the same keys as web/src/i18n) plus string params.
// The server renders Chinese for logs and old clients; the web app translates key + params
// into the user's interface language (web/scripts/i18n-extract.mjs collects the keys from Go).
//
// Keys must not contain the vue-i18n syntax characters @ $ | outside {name} placeholders.
package msg

import (
	"errors"
	"fmt"
	"strings"
)

// Text is one translatable message.
type Text struct {
	Key    string            `json:"key" doc:"Message key: the Simplified Chinese text with {name} placeholders"`
	Params map[string]string `json:"params,omitempty" doc:"Values for the placeholders"`
	Nested map[string]Text   `json:"nested,omitempty" doc:"Params that are messages themselves (translate them too; params holds their Chinese)"`
}

// T builds a message from a key and name/value pairs: T("最多可以添加 {n} 个节点", "n", 3).
// A value that is itself a Text is nested: translated too, with its Chinese in Params as fallback.
func T(key string, kv ...any) Text {
	t := Text{Key: key}
	for i := 0; i+1 < len(kv); i += 2 {
		if t.Params == nil {
			t.Params = map[string]string{}
		}
		name := fmt.Sprint(kv[i])
		if inner, ok := kv[i+1].(Text); ok {
			if t.Nested == nil {
				t.Nested = map[string]Text{}
			}
			t.Params[name], t.Nested[name] = inner.String(), inner
			continue
		}
		t.Params[name] = fmt.Sprint(kv[i+1])
	}
	return t
}

// String renders the Chinese text.
func (t Text) String() string {
	s := t.Key
	for k, v := range t.Params {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s
}

// Error is an error whose message is translatable. Its text is the whole message the user sees;
// a cause (a sentinel such as ErrInvalidCredential) stays reachable for errors.Is / As.
type Error struct {
	Text
	cause error
}

func (e *Error) Error() string { return e.String() }

// Unwrap returns the cause given to Wrap, if any.
func (e *Error) Unwrap() error { return e.cause }

// Err builds a translatable error: Err("已经有叫 {name} 的项目了", "name", n). As a package
// variable it is a sentinel like errors.New.
func Err(key string, kv ...any) error { return &Error{Text: T(key, kv...)} }

// Wrap is Err that still matches cause with errors.Is: Wrap(ErrTimeout, "连接 {addr} 超时", "addr", a).
func Wrap(cause error, key string, kv ...any) error { return &Error{Text: T(key, kv...), cause: cause} }

// K marks a fixed user-facing string as a message key (for code that passes plain strings).
func K(key string) string { return key }

// Of returns the translatable message an error carries, or the error text as a plain key
// (the web app shows an unknown key as it is).
func Of(err error) Text {
	var e *Error
	if errors.As(err, &e) {
		return e.Text
	}
	var m interface{ Msg() Text }
	if errors.As(err, &m) {
		return m.Msg()
	}
	return Text{Key: err.Error()}
}
