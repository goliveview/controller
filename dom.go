package controller

import (
	"bytes"
	"encoding/json"
	"html/template"
	"log"
	"strings"

	"github.com/yosssi/gohtml"

	"github.com/gorilla/websocket"
)

type Op string

const (
	ClassList Op = "classlist"
	Dataset   Op = "dataset"
	Morph     Op = "morph"
)

type Operation struct {
	Op       Op          `json:"op"`
	Selector string      `json:"selector"`
	Value    interface{} `json:"value"`
}

func (m *Operation) Bytes() []byte {
	b, err := json.Marshal(m)
	if err != nil {
		log.Printf("error marshalling dom %v\n", err)
		return nil
	}
	return b
}

type DOM interface {
	SetDataset(target string, data M)
	ToggleClassList(target string, classList map[string]bool)
	Morph(selector, template string, data M)
}

type dom struct {
	rootTemplate         *template.Template
	conns                map[string]*websocket.Conn
	messageType          int
	store                SessionStore
	temporaryKeys        []string
	enableHTMLFormatting bool
	debugLog             bool
}

func (d *dom) SetDataset(selector string, data M) {
	dataset := make(map[string]interface{})
	for k, v := range data {
		if strings.HasPrefix(k, "data-") {
			k = strings.TrimPrefix(k, "data-")
		}
		dataset[kebabToCamelCase(k)] = v
	}

	m := &Operation{
		Op:       Dataset,
		Selector: selector,
		Value:    dataset,
	}
	d.writePreparedMessage(m.Bytes())
	d.setStore(data)
}

func (d *dom) ToggleClassList(selector string, boolData map[string]bool) {

	classList := make(map[string]interface{})
	for k, v := range boolData {
		classList[k] = v
	}

	m := &Operation{
		Op:       ClassList,
		Selector: selector,
		Value:    classList,
	}
	d.writePreparedMessage(m.Bytes())

	// update store
	data := make(map[string]interface{})
	for k, v := range boolData {
		data[k] = v
	}
	d.setStore(data)
}

func (d *dom) Morph(selector, template string, data M) {
	var buf bytes.Buffer
	err := d.rootTemplate.ExecuteTemplate(&buf, template, data)
	if err != nil {
		log.Printf("err %v with data => \n %+v\n", err, getJSON(data))
		return
	}
	if d.debugLog {
		log.Printf("rendered template %+v, with data => \n %+v\n", template, getJSON(data))
	}
	html := buf.String()
	if d.enableHTMLFormatting {
		html = gohtml.Format(html)
	}
	buf.Reset()

	m := &Operation{
		Op:       Morph,
		Selector: selector,
		Value:    html,
	}
	d.writePreparedMessage(m.Bytes())
	d.setStore(data)
}

func (d *dom) writePreparedMessage(message []byte) {
	preparedMessage, err := websocket.NewPreparedMessage(d.messageType, message)
	if err != nil {
		log.Printf("err preparing message %v\n", err)
		return
	}

	for topic, conn := range d.conns {
		err := conn.WritePreparedMessage(preparedMessage)
		if err != nil {
			log.Printf("err writing message for topic:%v, %v, closing conn", topic, err)
			conn.Close()
			return
		}
	}
}

func (d *dom) setStore(data M) {
	// delete keys which are marked temporary
	for _, t := range d.temporaryKeys {
		delete(data, t)
	}
	// update store
	err := d.store.Set(data)
	if err != nil {
		log.Printf("error store.set %v\n", err)
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

func getJSON(data M) string {
	b, err := json.MarshalIndent(data, "", " ")
	if err != nil {
		return err.Error()
	}
	return string(b)
}
