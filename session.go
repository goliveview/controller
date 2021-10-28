package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"html/template"
	"log"
	"strings"

	"github.com/yosssi/gohtml"

	"github.com/gorilla/websocket"
)

type M map[string]interface{}

type Event struct {
	ID       string          `json:"id"`
	Query    string          `json:"query"`
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
	ChangeDataset(target string, data M)
	ChangeClassList(target string, classList map[string]bool)
	Morph(query, template string, data M)
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
	rootTemplate         *template.Template
	topic                string
	event                Event
	conns                map[string]*websocket.Conn
	messageType          int
	store                SessionStore
	temporaryKeys        []string
	enableHTMLFormatting bool
	requestContext       context.Context
	debugLog             bool
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

	s.Morph("#glv-error", "glv-error", M{"error": userMessage})

}

func (s session) unsetError() {
	s.Morph("#glv-error", "glv-error", nil)
}

func getJSON(data M) string {
	b, err := json.MarshalIndent(data, "", " ")
	if err != nil {
		return err.Error()
	}
	return string(b)
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

func (s session) Morph(query, template string, data M) {
	var buf bytes.Buffer

	err := s.rootTemplate.ExecuteTemplate(&buf, template, data)
	if err != nil {
		log.Printf("err %v with data => \n %+v\n", err, getJSON(data))
		return
	}
	if s.debugLog {
		log.Printf("rendered template %+v, with data => \n %+v\n", template, getJSON(data))
	}
	html := buf.String()
	if s.enableHTMLFormatting {
		html = gohtml.Format(html)
	}

	buf.Reset()

	morphData := map[string]interface{}{
		"selectQuery": query,
		"html":        html,
	}

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(true)
	err = enc.Encode(&morphData)
	if err != nil {
		log.Printf("err marshalling morphData %v\n", err)
		return
	}

	s.writePreparedMessage(buf.Bytes())

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

func (s session) Event() Event {
	return s.event
}

func (s session) RequestContext() context.Context {
	return s.requestContext
}

func (s session) Temporary(keys ...string) {
	s.temporaryKeys = append(s.temporaryKeys, keys...)
}

func (s session) Set(m M) error {
	return s.store.Set(m)
}

func (s session) Decode(key string, data interface{}) error {
	return s.store.Decode(key, data)
}
