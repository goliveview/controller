package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/lithammer/shortuuid"
)

type Status struct {
	Code    int    `json:"statusCode"`
	Message string `json:"statusMessage"`
}

type View interface {
	Content() string
	Layout() string
	OnMount(w http.ResponseWriter, r *http.Request) (Status, M)
	OnEvent(ctx Context) error
	LayoutContentName() string
	Partials() []string
	Extensions() []string
	FuncMap() template.FuncMap
}

type DefaultView struct{}

func (d DefaultView) Content() string {
	return "./templates/index.html"
}

func (d DefaultView) Layout() string {
	return "./templates/layouts/index.html"
}

func (d DefaultView) OnMount(w http.ResponseWriter, r *http.Request) (Status, M) {
	return Status{Code: 200, Message: "ok"}, M{}
}

func (d DefaultView) OnEvent(ctx Context) error {
	switch ctx.Event().ID {
	default:
		log.Printf("[defaultView] warning:handler not found for event => \n %+v\n", ctx.Event())
	}
	return nil
}

func (d DefaultView) LayoutContentName() string {
	return "content"
}

func (d DefaultView) Partials() []string {
	return []string{"./templates/partials"}
}

func (d DefaultView) Extensions() []string {
	return []string{".html", ".tmpl"}
}

func (d DefaultView) FuncMap() template.FuncMap {
	return DefaultFuncMap()
}

type DefaultErrorView struct{}

func (d DefaultErrorView) Content() string {
	return "./templates/error.html"
}

func (d DefaultErrorView) Layout() string {
	return "./templates/layouts/error.html"
}

func (d DefaultErrorView) OnMount(w http.ResponseWriter, r *http.Request) (Status, M) {
	return Status{Code: 500, Message: "Internal Error"}, M{}
}

func (d DefaultErrorView) OnEvent(ctx Context) error {
	switch ctx.Event().ID {
	default:
		log.Printf("[DefaultErrorView] warning:handler not found for event => \n %+v\n", ctx.Event())
	}
	return nil
}

func (d DefaultErrorView) LayoutContentName() string {
	return "content"
}

func (d DefaultErrorView) Partials() []string {
	return []string{"./templates/partials"}
}

func (d DefaultErrorView) Extensions() []string {
	return []string{".html", ".tmpl"}
}

func (d DefaultErrorView) FuncMap() template.FuncMap {
	return DefaultFuncMap()
}

type viewHandler struct {
	view              View
	errorView         View
	viewTemplate      *template.Template
	errorViewTemplate *template.Template
	mountData         M
	user              int
	wc                *websocketController
}

func (v *viewHandler) reloadTemplates() {
	var err error
	if v.wc.disableTemplateCache {
		v.viewTemplate, err = parseTemplate(v.view)
		if err != nil {
			panic(err)
		}

		v.errorViewTemplate, err = parseTemplate(v.errorView)
		if err != nil {
			panic(err)
		}
	}
}

func onMount(w http.ResponseWriter, r *http.Request, v *viewHandler) {
	v.reloadTemplates()
	var err error
	var status Status
	status, v.mountData = v.view.OnMount(w, r)
	if v.mountData == nil {
		v.mountData = make(M)
	}
	v.mountData["app_name"] = v.wc.name
	w.WriteHeader(status.Code)
	if status.Code > 299 {
		onMountError(w, r, v, &status)
		return
	}

	v.viewTemplate.Option("missingkey=zero")
	err = v.viewTemplate.ExecuteTemplate(w, filepath.Base(v.view.Layout()), v.mountData)
	if err != nil {
		onMountError(w, r, v, nil)
	}
	if v.wc.debugLog {
		log.Printf("onMount render view %+v, with data => \n %+v\n",
			v.view.Content(), getJSON(v.mountData))
	}

}

func onMountError(w http.ResponseWriter, r *http.Request, v *viewHandler, status *Status) {
	var errorStatus Status
	errorStatus, v.mountData = v.errorView.OnMount(w, r)
	if v.mountData == nil {
		v.mountData = make(M)
	}
	if status == nil {
		status = &errorStatus
	}
	v.mountData["statusCode"] = status.Code
	v.mountData["statusMessage"] = status.Message
	err := v.errorViewTemplate.ExecuteTemplate(w, filepath.Base(v.view.Layout()), v.mountData)
	if err != nil {
		log.Printf("err rendering error template: %v\n", err)
		_, errWrite := w.Write([]byte("Something went wrong"))
		if errWrite != nil {
			panic(errWrite)
		}
	}
}

func onEvent(w http.ResponseWriter, r *http.Request, v *viewHandler) {
	ctx := r.Context()
	if v.wc.requestContextFunc != nil {
		ctx = v.wc.requestContextFunc(r)
	}
	var topic *string
	if v.wc.subscribeTopicFunc != nil {
		topic = v.wc.subscribeTopicFunc(r)
	}

	c, err := v.wc.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()

	connID := shortuuid.New()
	if topic != nil {
		v.wc.addConnection(*topic, connID, c)
	}

	store := v.wc.userSessions.getOrCreate(v.user)
	err = store.Set(v.mountData)
	if err != nil {
		log.Printf("onEvent: store.Set(mountData) err %v\n", err)
	}

loop:
	for {
		mt, message, err := c.ReadMessage()
		if err != nil {
			log.Println("c.readMessage error: ", err)
			break loop
		}

		event := new(Event)
		err = json.NewDecoder(bytes.NewReader(message)).Decode(event)
		if err != nil {
			log.Printf("err: parsing event, msg %s \n", string(message))
			continue
		}

		if event.ID == "" {
			log.Printf("err: event %v, field event.id is required\n", event)
			continue
		}

		v.reloadTemplates()
		sess := session{
			dom: &dom{
				messageType:          mt,
				conns:                v.wc.getTopicConnections(*topic),
				store:                store,
				rootTemplate:         v.viewTemplate,
				temporaryKeys:        []string{"selector", "template"},
				enableHTMLFormatting: v.wc.enableHTMLFormatting,
				debugLog:             v.wc.debugLog,
			},
			event:          *event,
			requestContext: ctx,
		}

		sess.unsetError()

		var eventHandlerErr error
		if v.wc.debugLog {
			log.Printf("[controller] received event %+v \n", sess.event)
		}
		eventHandlerErr = v.view.OnEvent(sess)

		if eventHandlerErr != nil {
			log.Printf("[error] \n event => %+v, \n err: %v\n", event, eventHandlerErr)
			sess.setError(UserError(eventHandlerErr), eventHandlerErr)
		}
	}

	if topic != nil {
		v.wc.removeConnection(*topic, connID)
	}
}

func parseTemplate(view View) (*template.Template, error) {
	// layout
	commonFiles := []string{view.Layout()}
	// global partials
	for _, p := range view.Partials() {
		commonFiles = append(commonFiles, find(p, view.Extensions())...)
	}
	layoutTemplate := template.Must(template.New("").Funcs(view.FuncMap()).ParseFiles(commonFiles...))

	pageTemplateClone := template.Must(layoutTemplate.Clone())
	var pageFiles []string
	// page and its partials
	pageFiles = append(pageFiles, find(view.Content(), view.Extensions())...)
	// contains: 1. layout 2. page  3. partials
	viewTemplate, err := pageTemplateClone.ParseFiles(pageFiles...)
	if err != nil {
		return nil, fmt.Errorf("error parsing files err %v", err)
	}

	if ct := viewTemplate.Lookup(view.LayoutContentName()); ct == nil {
		return nil,
			fmt.Errorf("err looking up layoutContent: the layout %s expects a template named %s",
				view.Layout(), view.LayoutContentName())
	}

	if err != nil {
		return nil, err
	}

	return viewTemplate, nil
}

var DefaultUserErrorMessage = "internal error"

func UserError(err error) string {
	userMessage := DefaultUserErrorMessage
	if userError := errors.Unwrap(err); userError != nil {
		userMessage = userError.Error()
	}
	return userMessage
}

func find(p string, extensions []string) []string {
	var files []string

	fi, err := os.Stat(p)
	if os.IsNotExist(err) {
		return files
	}
	if !fi.IsDir() {
		if !contains(extensions, filepath.Ext(p)) {
			return files
		}
		files = append(files, p)
		return files
	}
	err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if contains(extensions, filepath.Ext(d.Name())) {
			files = append(files, path)
		}
		return nil
	})

	if err != nil {
		panic(err)
	}

	return files
}

func contains(arr []string, s string) bool {
	for _, a := range arr {
		if a == s {
			return true
		}
	}
	return false
}
