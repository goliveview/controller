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
	"strings"

	"github.com/lithammer/shortuuid/v3"
)

func contains(arr []string, s string) bool {
	for _, a := range arr {
		if a == s {
			return true
		}
	}
	return false
}

type OnMount func(w http.ResponseWriter, r *http.Request) (int, M)
type ViewOption func(opt *viewOpt)
type ViewHandler interface {
	OnMount(w http.ResponseWriter, r *http.Request) (int, M)
	EventHandler(ctx Context) error
}

type viewOpt struct {
	errorPage         string
	layout            string
	layoutContentName string
	partials          []string
	extensions        []string
	funcMap           template.FuncMap
	onMountFunc       OnMount
	eventHandlers     map[string]EventHandler
	viewHandler       ViewHandler
}

func WithLayout(layout string) ViewOption {
	return func(o *viewOpt) {
		o.layout = layout
	}
}

func WithLayoutContentName(layoutContentName string) ViewOption {
	return func(o *viewOpt) {
		o.layoutContentName = layoutContentName
	}
}

func WithPartials(partials ...string) ViewOption {
	return func(o *viewOpt) {
		o.partials = partials
	}
}

func WithExtensions(extensions ...string) ViewOption {
	return func(o *viewOpt) {
		o.extensions = extensions
	}
}

func WithFuncMap(funcMap template.FuncMap) ViewOption {
	return func(o *viewOpt) {
		o.funcMap = funcMap
	}
}

func WithOnMount(onMountFunc OnMount) ViewOption {
	return func(o *viewOpt) {
		o.onMountFunc = onMountFunc
	}
}

func WithErrorPage(errorPage string) ViewOption {
	return func(o *viewOpt) {
		o.errorPage = errorPage
	}
}

func WithEventHandlers(eventHandlers map[string]EventHandler) ViewOption {
	return func(o *viewOpt) {
		o.eventHandlers = eventHandlers
	}
}

func WithViewHandler(viewHandler ViewHandler) ViewOption {
	return func(o *viewOpt) {
		o.viewHandler = viewHandler
	}
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

func (wc *websocketController) NewView(page string, options ...ViewOption) http.HandlerFunc {
	o := &viewOpt{
		layout:            "./templates/layouts/index.html",
		layoutContentName: "content",
		partials:          []string{"./templates/partials"},
		extensions:        []string{".html", ".tmpl"},
		funcMap:           DefaultFuncMap(),
	}
	for _, option := range options {
		option(o)
	}

	var pageTemplate *template.Template
	var errorTemplate *template.Template
	var err error

	parseTemplates := func() {
		// layout
		commonFiles := []string{o.layout}
		// global partials
		for _, p := range o.partials {
			commonFiles = append(commonFiles, find(p, o.extensions)...)
		}
		layoutTemplate := template.Must(template.New("").Funcs(o.funcMap).ParseFiles(commonFiles...))

		pageTemplateCone := template.Must(layoutTemplate.Clone())
		var pageFiles []string
		// page and its partials
		pageFiles = append(pageFiles, find(page, o.extensions)...)
		// contains: 1. layout 2. page  3. partials
		pageTemplate, err = pageTemplateCone.ParseFiles(pageFiles...)
		if err != nil {
			panic(fmt.Errorf("error parsing files err %v", err))
		}

		if ct := pageTemplate.Lookup(o.layoutContentName); ct == nil {
			panic(fmt.Errorf("err looking up layoutContent: the layout %s expects a template named %s",
				o.layout, o.layoutContentName))
		}

		if err != nil {
			panic(err)
		}

		if o.errorPage != "" {
			// layout
			var errorFiles []string
			errorTemplateCone := template.Must(layoutTemplate.Clone())
			// error page and its partials
			errorFiles = append(errorFiles, find(page, o.extensions)...)
			// contains: 1. layout 2. page  3. partials
			errorTemplate, err = errorTemplateCone.ParseFiles(errorFiles...)
			if err != nil {
				panic(fmt.Errorf("error parsing error page template err %v", err))
			}

			if ct := errorTemplate.Lookup(o.layoutContentName); ct == nil {
				panic(fmt.Errorf("err looking up layoutContent: the layout %s expects a template named %s",
					o.layout, o.layoutContentName))
			}
		}
	}

	parseTemplates()

	mountData := make(M)
	status := 200
	renderPage := func(w http.ResponseWriter, r *http.Request) {

		if o.viewHandler != nil {
			status, mountData = o.viewHandler.OnMount(w, r)
		} else if o.onMountFunc != nil {
			status, mountData = o.onMountFunc(w, r)
		}

		if mountData == nil {
			mountData = make(M)
		}

		mountData["app_name"] = wc.name

		w.WriteHeader(status)
		if status > 299 {
			// TODO: custom error page
			w.Write([]byte(fmt.Sprintf(
				`<div style="text-align:center"><h1>%d</h1></div>
<div style="text-align:center"><a href="javascript:history.back()">back</a></div>
<div style="text-align:center"><a href="/">home</a></div>`, status)))
			return
		}

		if wc.disableTemplateCache {
			parseTemplates()
		}

		pageTemplate.Option("missingkey=zero")
		err = pageTemplate.ExecuteTemplate(w, filepath.Base(o.layout), mountData)
		if err != nil {
			if errorTemplate != nil {
				err = errorTemplate.ExecuteTemplate(w, filepath.Base(o.layout), nil)
				if err != nil {
					log.Printf("err rendering error template: %v\n", err)
					w.Write([]byte("something went wrong"))
				}
			} else {
				if r.Method == "POST" {
					log.Printf("onPost err: %v\n, with data => \n %+v\n", err, getJSON(mountData))
				} else {
					log.Printf("onMount err: %v\n, with data => \n %+v\n", err, getJSON(mountData))
				}

				w.Write([]byte("something went wrong"))
			}
		}
		if wc.debugLog {
			log.Printf("onMount render page %+v, with data => \n %+v\n", page, getJSON(mountData))
		}
	}

	handleSocket := func(w http.ResponseWriter, r *http.Request, user int) {
		ctx := r.Context()
		if wc.requestContextFunc != nil {
			ctx = wc.requestContextFunc(r)
		}
		var topic *string
		if wc.subscribeTopicFunc != nil {
			topic = wc.subscribeTopicFunc(r)
		}

		c, err := wc.upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()

		connID := shortuuid.New()
		store := wc.userSessions.getOrCreate(user)
		store.Set(mountData)
		if topic != nil {
			wc.addConnection(*topic, connID, c)
		}
	loop:
		for {
			mt, message, err := c.ReadMessage()
			if err != nil {
				log.Println("readx:", err)
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

			sess := session{
				dom: &dom{
					messageType:          mt,
					conns:                wc.getTopicConnections(*topic),
					store:                store,
					rootTemplate:         pageTemplate,
					temporaryKeys:        []string{"selector", "template"},
					enableHTMLFormatting: wc.enableHTMLFormatting,
					debugLog:             wc.debugLog,
				},
				event:          *event,
				requestContext: ctx,
			}
			if wc.disableTemplateCache {
				parseTemplates()
			}
			sess.unsetError()

			var eventHandlerErr error
			if o.viewHandler != nil {
				if wc.debugLog {
					log.Printf("[controller] received event %+v \n", sess.event)
				}
				eventHandlerErr = o.viewHandler.EventHandler(sess)
			} else {
				eventHandler, ok := o.eventHandlers[event.ID]
				if !ok {
					log.Printf("err: no handler found for event %s\n", event.ID)
					continue
				}
				eventHandlerErr = eventHandler(sess)
			}

			if eventHandlerErr != nil {
				log.Printf("[error] \n event => %+v, \n err: %v\n", event, eventHandlerErr)
				sess.setError(UserError(eventHandlerErr), eventHandlerErr)
			}
		}

		if topic != nil {
			wc.removeConnection(*topic, connID)
		}
	}

	return func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimSpace(wc.name)
		wc.cookieStore.MaxAge(0)
		cookieSession, _ := wc.cookieStore.Get(r, fmt.Sprintf("_glv_key_%s", name))
		user := cookieSession.Values["user"]
		if user == nil {
			c := wc.userCount.incr()
			cookieSession.Values["user"] = c
			user = c
		}

		err := cookieSession.Save(r, w)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if r.Header.Get("Connection") == "Upgrade" && r.Header.Get("Upgrade") == "websocket" {
			handleSocket(w, r, user.(int))
		} else {
			renderPage(w, r)
		}
	}
}

var DefaultUserErrorMessage = "internal error"

func UserError(err error) string {
	userMessage := DefaultUserErrorMessage
	if userError := errors.Unwrap(err); userError != nil {
		userMessage = userError.Error()
	}
	return userMessage
}
