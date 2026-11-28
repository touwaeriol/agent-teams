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
}

// T builds a message from a key and name/value pairs: T("最多可以添加 {n} 个节点", "n", 3).
func T(key string, kv ...any) Text {
	t := Text{Key: key}
	for i := 0; i+1 < len(kv); i += 2 {
		if t.Params == nil {
			t.Params = map[string]string{}
		}
		t.Params[fmt.Sprint(kv[i])] = fmt.Sprint(kv[i+1])
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

// Error is an error whose message is translatable.
type Error struct{ Text }

func (e *Error) Error() string { return e.String() }

// Err builds a translatable error: Err("已经有叫 {name} 的项目了", "name", n).
func Err(key string, kv ...any) error { return &Error{T(key, kv...)} }

// Of returns the translatable message an error carries, or the error text as a plain key
// (the web app shows an unknown key as it is).
func Of(err error) Text {
	var e *Error
	if errors.As(err, &e) {
		return e.Text
	}
	return Text{Key: err.Error()}
}
