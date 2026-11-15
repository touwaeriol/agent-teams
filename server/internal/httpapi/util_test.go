package httpapi

import (
	"net/http"
	"net/url"
)

type httpCookie = http.Cookie

func mustURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}
