package controller

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

type M map[string]interface{}

type Event struct {
	ID       string          `json:"id"`
	Selector string          `json:"selector"`
	Template string          `json:"template"`
	Params   json.RawMessage `json:"params"`
}

func (e Event) String() string {
	data, _ := json.MarshalIndent(e, "", " ")
	return string(data)
}

type EventHandler func(ctx Context) error

type Context interface {
	Event() Event
	DOM() DOM
	Store() Store
	Temporary(keys ...string)
	Request() *http.Request
	ResponseWriter() http.ResponseWriter
}

func (e Event) DecodeParams(v interface{}) error {
	return json.NewDecoder(bytes.NewReader(e.Params)).Decode(v)
}

type sessionContext struct {
	event Event
	dom   *dom
	r     *http.Request
	w     http.ResponseWriter
}

func (s sessionContext) setError(userMessage string, errs ...error) {
	if len(errs) != 0 {
		var errstrs []string
		for _, err := range errs {
			if err == nil {
				continue
			}
			errstrs = append(errstrs, err.Error())
		}
		log.Printf("err: %v, errors: %v\n", userMessage, strings.Join(errstrs, ","))
	}

	s.dom.Morph("#glv-error", "glv-error", M{"error": userMessage})

}

func (s sessionContext) unsetError() {
	s.dom.Morph("#glv-error", "glv-error", nil)
}

func (s sessionContext) DOM() DOM {
	return s.dom
}

func (s sessionContext) Event() Event {
	return s.event
}

func (s sessionContext) Request() *http.Request {
	return s.r
}

func (s sessionContext) ResponseWriter() http.ResponseWriter {
	return s.w
}

func (s sessionContext) Temporary(keys ...string) {
	s.dom.temporaryKeys = append(s.dom.temporaryKeys, keys...)
}

func (s sessionContext) Store() Store {
	return s.dom.store
}
