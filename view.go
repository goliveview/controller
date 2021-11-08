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

type View interface {
	Content() string
	OnMount(w http.ResponseWriter, r *http.Request) (int, M)
	OnEvent(ctx Context) error
	Layout() string
	LayoutContentName() string
	Partials() []string
	Extensions() []string
	FuncMap() template.FuncMap
	ErrorPage() string
}

type DefaultView struct{}

func (d DefaultView) OnMount(w http.ResponseWriter, r *http.Request) (int, M) {
	return 200, M{}
}

func (d DefaultView) OnEvent(ctx Context) error {
	switch ctx.Event().ID {
	default:
		log.Printf("[defaultView] warning:handler not found for event => \n %+v\n", ctx.Event())
	}
	return nil
}

func (d DefaultView) Content() string {
	return "./templates/index.html"
}

func (d DefaultView) Layout() string {
	return "./templates/layouts/index.html"
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

func (d DefaultView) ErrorPage() string {
	return "./templates/error.html"
}

func (wc *websocketController) Handle(view View) http.HandlerFunc {
	var pageTemplate *template.Template
	var errorTemplate *template.Template
	var err error

	parseTemplates := func() {
		// layout
		commonFiles := []string{view.Layout()}
		// global partials
		for _, p := range view.Partials() {
			commonFiles = append(commonFiles, find(p, view.Extensions())...)
		}
		layoutTemplate := template.Must(template.New("").Funcs(view.FuncMap()).ParseFiles(commonFiles...))

		pageTemplateCone := template.Must(layoutTemplate.Clone())
		var pageFiles []string
		// page and its partials
		pageFiles = append(pageFiles, find(view.Content(), view.Extensions())...)
		// contains: 1. layout 2. page  3. partials
		pageTemplate, err = pageTemplateCone.ParseFiles(pageFiles...)
		if err != nil {
			panic(fmt.Errorf("error parsing files err %v", err))
		}

		if ct := pageTemplate.Lookup(view.LayoutContentName()); ct == nil {
			panic(fmt.Errorf("err looking up layoutContent: the layout %s expects a template named %s",
				view.Layout(), view.LayoutContentName()))
		}

		if err != nil {
			panic(err)
		}

		if view.ErrorPage() != "" {
			// layout
			var errorFiles []string
			errorTemplateCone := template.Must(layoutTemplate.Clone())
			// error page and its partials
			errorFiles = append(errorFiles, find(view.Content(), view.Extensions())...)
			// contains: 1. layout 2. page  3. partials
			errorTemplate, err = errorTemplateCone.ParseFiles(errorFiles...)
			if err != nil {
				panic(fmt.Errorf("error parsing error view template err %v", err))
			}

			if ct := errorTemplate.Lookup(view.LayoutContentName()); ct == nil {
				panic(fmt.Errorf("err looking up layoutContent: the layout %s expects a template named %s",
					view.Layout(), view.LayoutContentName()))
			}
		}
	}

	parseTemplates()

	mountData := make(M)
	status := 200
	renderView := func(w http.ResponseWriter, r *http.Request) {
		status, mountData = view.OnMount(w, r)
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
		err = pageTemplate.ExecuteTemplate(w, filepath.Base(view.Layout()), mountData)
		if err != nil {
			if errorTemplate != nil {
				err = errorTemplate.ExecuteTemplate(w, filepath.Base(view.Layout()), nil)
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
			log.Printf("onMount render view %+v, with data => \n %+v\n", view.Content(), getJSON(mountData))
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

			if wc.debugLog {
				log.Printf("[controller] received event %+v \n", sess.event)
			}
			eventHandlerErr = view.OnEvent(sess)

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
			renderView(w, r)
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
