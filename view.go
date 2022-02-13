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
	// Content represents the path to the html page content
	Content() string
	// Layout represents the path to the base layout to be used.
	/*
			layout.html e.g.
			<!DOCTYPE html>
			<html lang="en">
			<head>
				<title>{{.app_name}}</title>
				{{template "header" .}}
			</head>
			<body>
			{{template "navbar" .}}
			<div>
				{{template "content" .}}
			</div>
			{{template "footer" .}}
			</body>
			</html>
		 The {{template "content" .}} directive is replaced by the page in the path exposed by `Content`
	*/
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
	return ""
}

func (d DefaultView) Layout() string {
	return ""
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
	return `{{ define "content"}}
    <div style="text-align:center"><h1>{{.statusCode}}</h1></div>
    <div style="text-align:center"><h1>{{.statusMessage}}</h1></div>
    <div style="text-align:center"><a href="javascript:history.back()">back</a></div>
    <div style="text-align:center"><a href="/">home</a></div>
{{ end }}`
}

func (d DefaultErrorView) Layout() string {
	return ""
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
	err = v.viewTemplate.Execute(w, v.mountData)
	if err != nil {
		log.Printf("onMount viewTemplate.Execute error:  %v", err)
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
	err := v.errorViewTemplate.Execute(w, v.mountData)
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
	err = store.Put(v.mountData)
	if err != nil {
		log.Printf("onEvent: store.Put(mountData) err %v\n", err)
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

// creates a html/template from the View type.
func parseTemplate(view View) (*template.Template, error) {
	// if both layout and content is empty show a default view.
	if view.Layout() == "" && view.Content() == "" {
		return template.Must(template.New("").
			Parse(`<div style="text-align:center"> This is a default view. </div>`)), nil
	}

	// if layout is set and content is empty
	if view.Layout() != "" && view.Content() == "" {
		var layoutTemplate *template.Template
		// check if layout is not a file or directory
		if _, err := os.Stat(view.Layout()); errors.Is(err, os.ErrNotExist) {
			// is not a file but html content
			layoutTemplate = template.Must(template.New("").Funcs(view.FuncMap()).Parse(view.Layout()))
		} else {
			// layout must be a file
			ok, err := isDirectory(view.Layout())
			if err == nil && ok {
				return nil, fmt.Errorf("layout is a directory but it must be a file")
			}

			if err != nil {
				return nil, err
			}
			// compile layout
			commonFiles := []string{view.Layout()}
			// global partials
			for _, p := range view.Partials() {
				commonFiles = append(commonFiles, find(p, view.Extensions())...)
			}
			layoutTemplate = template.Must(template.New(view.Layout()).
				Funcs(view.FuncMap()).
				ParseFiles(commonFiles...))
		}
		return template.Must(layoutTemplate.Clone()), nil
	}

	// if layout is empty and content is set
	if view.Layout() == "" && view.Content() != "" {
		// check if content is a not a file or directory
		if _, err := os.Stat(view.Content()); errors.Is(err, os.ErrNotExist) {
			return template.Must(template.New("base").
				Funcs(view.FuncMap()).
				Parse(view.Content())), nil
		} else {
			// is a file or directory
			var pageFiles []string
			// view and its partials
			pageFiles = append(pageFiles, find(view.Content(), view.Extensions())...)
			for _, p := range view.Partials() {
				pageFiles = append(pageFiles, find(p, view.Extensions())...)
			}
			return template.Must(template.New(view.Content()).
				Funcs(view.FuncMap()).
				ParseFiles(pageFiles...)), nil
		}
	}

	// if both layout and content are set
	var viewTemplate *template.Template
	// 1. build layout
	var layoutTemplate *template.Template
	// check if layout is not a file or directory
	if _, err := os.Stat(view.Layout()); errors.Is(err, os.ErrNotExist) {
		// is not a file but html content
		layoutTemplate = template.Must(template.New("base").Funcs(view.FuncMap()).Parse(view.Layout()))
	} else {
		// layout must be a file
		ok, err := isDirectory(view.Layout())
		if err == nil && ok {
			return nil, fmt.Errorf("layout is a directory but it must be a file")
		}

		if err != nil {
			return nil, err
		}
		// compile layout
		commonFiles := []string{view.Layout()}
		// global partials
		for _, p := range view.Partials() {
			commonFiles = append(commonFiles, find(p, view.Extensions())...)
		}
		layoutTemplate = template.Must(template.New(filepath.Base(view.Layout())).
			Funcs(view.FuncMap()).
			ParseFiles(commonFiles...))

		//log.Println("compiled layoutTemplate...")
		//for _, v := range layoutTemplate.Templates() {
		//	fmt.Println("template => ", v.Name())
		//}
	}

	// 2. add content
	// check if content is a not a file or directory
	if _, err := os.Stat(view.Content()); errors.Is(err, os.ErrNotExist) {
		// content is not a file or directory but html content
		viewTemplate = template.Must(layoutTemplate.Parse(view.Content()))
	} else {
		// content is a file or directory
		var pageFiles []string
		// view and its partials
		pageFiles = append(pageFiles, find(view.Content(), view.Extensions())...)

		viewTemplate = template.Must(layoutTemplate.ParseFiles(pageFiles...))
	}

	// check if the final viewTemplate contains a content child template which is `content` by default.
	if ct := viewTemplate.Lookup(view.LayoutContentName()); ct == nil {
		return nil,
			fmt.Errorf("err looking up layoutContent: the layout %s expects a template named %s",
				view.Layout(), view.LayoutContentName())
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

func isDirectory(path string) (bool, error) {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return false, err
	}

	return fileInfo.IsDir(), err
}
