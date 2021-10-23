package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"strings"
	"time"

	"github.com/lithammer/shortuuid/v3"

	"github.com/yosssi/gohtml"

	"github.com/gorilla/websocket"
)

type ActionType string

const (
	Append  ActionType = "append"
	Prepend ActionType = "prepend"
	Replace ActionType = "replace"
	Update  ActionType = "update"
	Before  ActionType = "before"
	After   ActionType = "after"
	Remove  ActionType = "remove"
)

var actions = map[string]int{
	"append":  0,
	"prepend": 0,
	"replace": 0,
	"update":  0,
	"before":  0,
	"after":   0,
	"remove":  0,
}

type M map[string]interface{}

type TurboStream struct {
	Action   ActionType `json:"action"`
	Target   string     `json:"target,omitempty"`
	Targets  string     `json:"targets,omitempty"`
	Template string     `json:"template"`
}

type Event struct {
	ID     string          `json:"id"`
	Params json.RawMessage `json:"params"`
	*TurboStream
}

type EventHandler func(ctx Context) error

type SessionStore interface {
	Set(m M) error
	Get(key string) (interface{}, bool)
}

type Session interface {
	ChangePartial(turboStream *TurboStream, data M)
	ChangeDataset(target string, data M)
	ChangeClassList(target string, classList map[string]bool)
	Flash(duration time.Duration, data M)
	Temporary(keys ...string)
	SessionStore
}

type Context interface {
	Event() Event
	RequestContext() context.Context
	Session
}

func (c Event) DecodeParams(v interface{}) error {
	return json.NewDecoder(bytes.NewReader(c.Params)).Decode(v)
}

type session struct {
	rootTemplate         *template.Template
	topic                string
	event                Event
	conns                map[string]*websocket.Conn
	messageType          int
	store                SessionStore
	temporaryKeys        []string
	enableHTMLFormatting bool
	requestContext       context.Context
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

	s.write(
		&TurboStream{
			Action:   Replace,
			Target:   "glv-error",
			Template: "glv-error",
		},
		M{"error": userMessage})

}

func (s session) unsetError() {
	s.write(&TurboStream{
		Action:   Replace,
		Target:   "glv-error",
		Template: "glv-error",
	}, nil)
}

func (s session) write(turboStream *TurboStream, data M) {
	if turboStream == nil {
		log.Printf("turbo stream is nil for event %v\n", s.event)
		return
	}
	if turboStream.Action == "" {
		log.Printf("err action is empty for event %v\n", s.event)
		return
	}
	// stream response
	if turboStream.Target == "" && turboStream.Targets == "" {
		log.Printf("err target or targets %s empty for event %+v\n", turboStream, s.event)
		return
	}
	var buf bytes.Buffer
	if turboStream.Template != "" && turboStream.Action != Remove {
		err := s.rootTemplate.ExecuteTemplate(&buf, turboStream.Template, data)
		if err != nil {
			log.Printf("err %v,while executing template for event %+v\n", err, s.event)
			return
		}
	}
	html := buf.String()
	var message string
	if turboStream.Targets != "" {
		message = fmt.Sprintf(turboTargetsWrapper, turboStream.Action, turboStream.Targets, html)
	} else {
		message = fmt.Sprintf(turboTargetWrapper, turboStream.Action, turboStream.Target, html)
	}

	if s.enableHTMLFormatting {
		message = gohtml.Format(message)
	}

	s.writePreparedMessage([]byte(message))

}

func (s session) writePreparedMessage(message []byte) {
	preparedMessage, err := websocket.NewPreparedMessage(s.messageType, []byte(message))
	if err != nil {
		log.Printf("err preparing message %v\n", err)
		return
	}

	for topic, conn := range s.conns {
		err := conn.WritePreparedMessage(preparedMessage)
		if err != nil {
			log.Printf("err writing message for topic:%v, %v, closing conn", topic, err)
			conn.Close()
			return
		}
	}
}

func (s session) render(turboStream *TurboStream, data M) {
	if turboStream == nil && s.event.TurboStream != nil {
		turboStream = s.event.TurboStream
	}
	s.write(turboStream, data)

	// delete keys which are marked temporary
	for _, t := range s.temporaryKeys {
		delete(data, t)
	}
	// update store
	err := s.store.Set(data)
	if err != nil {
		log.Printf("error store.set %v\n", err)
	}
}

func (s session) ChangePartial(turboStream *TurboStream, data M) {
	s.render(turboStream, data)
}

// https://github.com/siongui/userpages/blob/master/content/code/go/kebab-case-to-camelCase/converter.go
func kebabToCamelCase(kebab string) (camelCase string) {
	isToUpper := false
	for _, runeValue := range kebab {
		if isToUpper {
			camelCase += strings.ToUpper(string(runeValue))
			isToUpper = false
		} else {
			if runeValue == '-' {
				isToUpper = true
			} else {
				camelCase += string(runeValue)
			}
		}
	}
	return
}

func (s session) ChangeDataset(target string, data M) {
	datasetChange := make(map[string]interface{})
	datasetChange["target"] = target
	dataset := make(map[string]interface{})
	for k, v := range data {
		if strings.HasPrefix(k, "data-") {
			k = strings.TrimPrefix(k, "data-")
		}
		dataset[kebabToCamelCase(k)] = v
	}

	datasetChange["dataset"] = dataset

	message, err := json.Marshal(&datasetChange)
	if err != nil {
		log.Printf("err marshalling datasetChange %v\n", err)
		return
	}

	s.writePreparedMessage(message)

	// delete keys which are marked temporary
	for _, t := range s.temporaryKeys {
		delete(data, t)
	}
	// update store
	err = s.store.Set(data)
	if err != nil {
		log.Printf("error store.set %v\n", err)
	}
}

func (s session) ChangeClassList(target string, data map[string]bool) {
	classListChange := make(map[string]interface{})
	classListChange["target"] = target
	classList := make(map[string]interface{})
	for k, v := range data {
		classList[k] = v
	}

	classListChange["classList"] = classList
	message, err := json.Marshal(&classListChange)
	if err != nil {
		log.Printf("err marshalling datasetChange %v\n", err)
		return
	}

	s.writePreparedMessage(message)

	// delete keys which are marked temporary
	for _, t := range s.temporaryKeys {
		delete(data, t)
	}
	// update store
	datax := make(map[string]interface{})
	for k, v := range data {
		datax[k] = v
	}
	err = s.store.Set(datax)
	if err != nil {
		log.Printf("error store.set %v\n", err)
	}
}

func (s session) Event() Event {
	return s.event
}

func (s session) RequestContext() context.Context {
	return s.requestContext
}

func (s session) Temporary(keys ...string) {
	s.temporaryKeys = append(s.temporaryKeys, keys...)
}

func (s session) Flash(duration time.Duration, data M) {
	turboStream := &TurboStream{
		Action:   Append,
		Target:   "glv-flash",
		Targets:  "",
		Template: "glv-flash-message",
	}

	flashID := shortuuid.New()
	data["flash_id"] = flashID

	s.render(turboStream, data)
	go func() {
		time.Sleep(duration)
		turboStream.Action = Remove
		s.render(turboStream, M{
			"flash_id": flashID,
		})
	}()
}

func (s session) Set(m M) error {
	return s.store.Set(m)
}

func (s session) Get(key string) (interface{}, bool) {
	return s.store.Get(key)
}

var turboTargetWrapper = `{
							"message":
							  "<turbo-stream action="%s" target="%s">
								<template>
									%s
								</template>
							   </turbo-stream>"
						  }`

var turboTargetsWrapper = `{
							"message":
							  "<turbo-stream action="%s" targets="%s">
								<template>
									%s
								</template>
							   </turbo-stream>"
						  }`
