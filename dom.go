package controller

import (
	"bytes"
	"encoding/json"
	"html/template"
	"log"
	"strings"

	"github.com/yosssi/gohtml"
)

type Op string

const (
	ClassList        Op = "classlist"
	Dataset          Op = "dataset"
	SetAttributes    Op = "setAttributes"
	RemoveAttributes Op = "removeAttributes"
	Morph            Op = "morph"
	Reload           Op = "reload"
	AddClass         Op = "addClass"
	RemoveClass      Op = "removeClass"
	SetValue         Op = "setValue"
	SetInnerHTML     Op = "setInnerHTML"
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
	SetDataset(selector string, data M)
	SetAttributes(selector string, data M)
	SetValue(selector string, value interface{})
	SetInnerHTML(selector string, value interface{})
	RemoveAttributes(selector string, data []string)
	ToggleClassList(selector string, classList map[string]bool)
	AddClass(selector, class string)
	RemoveClass(selector, class string)
	Morph(selector, template string, data M)
	Reload()
}

type dom struct {
	rootTemplate  *template.Template
	store         Store
	temporaryKeys []string
	topic         string
	wc            *websocketController
}

func (d *dom) SetAttributes(selector string, data M) {
	m := &Operation{
		Op:       SetAttributes,
		Selector: selector,
		Value:    data,
	}
	d.wc.message(d.topic, m.Bytes())
	d.setStore(data)
}

func (d *dom) RemoveAttributes(selector string, data []string) {
	m := &Operation{
		Op:       RemoveAttributes,
		Selector: selector,
		Value:    data,
	}
	d.wc.message(d.topic, m.Bytes())
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
	d.wc.message(d.topic, m.Bytes())
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
	d.wc.message(d.topic, m.Bytes())

	// update inmemStore
	data := make(map[string]interface{})
	for k, v := range boolData {
		data[k] = v
	}
	d.setStore(data)
}

func (d *dom) AddClass(selector, class string) {

	m := &Operation{
		Op:       AddClass,
		Selector: selector,
		Value:    class,
	}
	d.wc.message(d.topic, m.Bytes())

	// update store
	data := make(map[string]interface{})
	data[class] = true
	d.setStore(data)
}

func (d *dom) RemoveClass(selector, class string) {

	m := &Operation{
		Op:       RemoveClass,
		Selector: selector,
		Value:    class,
	}
	d.wc.message(d.topic, m.Bytes())

	// update store
	data := make(map[string]interface{})
	data[class] = false
	d.setStore(data)
}

func (d *dom) SetValue(selector string, value interface{}) {

	m := &Operation{
		Op:       SetValue,
		Selector: selector,
		Value:    value,
	}
	d.wc.message(d.topic, m.Bytes())

	// update store
	data := make(map[string]interface{})
	data[strings.TrimPrefix(selector, "#")] = value
	d.setStore(data)
}

func (d *dom) SetInnerHTML(selector string, value interface{}) {

	m := &Operation{
		Op:       SetInnerHTML,
		Selector: selector,
		Value:    value,
	}
	d.wc.message(d.topic, m.Bytes())
}

func (d *dom) Morph(selector, template string, data M) {
	var buf bytes.Buffer
	err := d.rootTemplate.ExecuteTemplate(&buf, template, data)
	if err != nil {
		log.Printf("err %v with data => \n %+v\n", err, getJSON(data))
		return
	}
	if d.wc.debugLog {
		log.Printf("rendered template %+v, with data => \n %+v\n", template, getJSON(data))
	}
	html := buf.String()
	if d.wc.enableHTMLFormatting {
		html = gohtml.Format(html)
	}
	buf.Reset()

	m := &Operation{
		Op:       Morph,
		Selector: selector,
		Value:    html,
	}
	d.wc.message(d.topic, m.Bytes())
	d.setStore(data)
}

func (d *dom) Reload() {
	m := &Operation{
		Op: Reload,
	}
	d.wc.message(d.topic, m.Bytes())
}

func (d *dom) setStore(data M) {
	// delete keys which are marked temporary
	for _, t := range d.temporaryKeys {
		delete(data, t)
	}
	// update inmemStore
	err := d.store.Put(data)
	if err != nil {
		log.Printf("error inmemStore.set %v\n", err)
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
