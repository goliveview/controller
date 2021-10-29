package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
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

type SessionStore interface {
	Set(m M) error
	Decode(key string, data interface{}) error
}

type Session interface {
	DOM() DOM
	Temporary(keys ...string)
	SessionStore
}

type Context interface {
	Event() Event
	RequestContext() context.Context
	Session
}

func (e Event) DecodeParams(v interface{}) error {
	return json.NewDecoder(bytes.NewReader(e.Params)).Decode(v)
}

type session struct {
	topic          string
	event          Event
	requestContext context.Context
	dom            *dom
}

func (s session) setError(userMessage string, errs ...error) {
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

func (s session) unsetError() {
	s.dom.Morph("#glv-error", "glv-error", nil)
}

func (s session) DOM() DOM {
	return s.dom
}

func (s session) Event() Event {
	return s.event
}

func (s session) RequestContext() context.Context {
	return s.requestContext
}

func (s session) Temporary(keys ...string) {
	s.dom.temporaryKeys = append(s.dom.temporaryKeys, keys...)
}

func (s session) Set(m M) error {
	return s.dom.store.Set(m)
}

func (s session) Decode(key string, data interface{}) error {
	return s.dom.store.Decode(key, data)
}
