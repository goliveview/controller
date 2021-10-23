package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gorilla/securecookie"

	"github.com/gorilla/sessions"

	"github.com/lithammer/shortuuid/v3"

	"github.com/Masterminds/sprig"
	"github.com/gorilla/websocket"
)

type Controller interface {
	NewView(page string, options ...ViewOption) http.HandlerFunc
}

type controlOpt struct {
	requestContextFunc   func(r *http.Request) context.Context
	subscribeTopicFunc   func(r *http.Request) *string
	upgrader             websocket.Upgrader
	enableHTMLFormatting bool
	disableTemplateCache bool
}

type Option func(*controlOpt)

func WithRequestContext(f func(r *http.Request) context.Context) Option {
	return func(o *controlOpt) {
		o.requestContextFunc = f
	}
}

func WithSubscribeTopic(f func(r *http.Request) *string) Option {
	return func(o *controlOpt) {
		o.subscribeTopicFunc = f
	}
}

func WithUpgrader(upgrader websocket.Upgrader) Option {
	return func(o *controlOpt) {
		o.upgrader = upgrader
	}
}

func EnableHTMLFormatting() Option {
	return func(o *controlOpt) {
		o.enableHTMLFormatting = true
	}
}

func DisableTemplateCache() Option {
	return func(o *controlOpt) {
		o.disableTemplateCache = true
	}
}

func Websocket(name *string, options ...Option) Controller {
	if name == nil {
		panic("controller name is required")
	}

	o := &controlOpt{
		requestContextFunc: nil,
		subscribeTopicFunc: func(r *http.Request) *string {
			challengeKey := r.Header.Get("Sec-Websocket-Key")
			topic := fmt.Sprintf("root_%s", challengeKey)
			if r.URL.Path != "/" {
				topic = fmt.Sprintf("%s_%s",
					strings.Replace(r.URL.Path, "/", "_", -1), challengeKey)
			}

			log.Println("client subscribed to topic", topic)
			return &topic
		},
		upgrader: websocket.Upgrader{},
	}

	for _, option := range options {
		option(o)
	}
	return &websocketController{
		cookieStore:      sessions.NewCookieStore([]byte(securecookie.GenerateRandomKey(32))),
		topicConnections: make(map[string]map[string]*websocket.Conn),
		controlOpt:       *o,
		name:             *name,
		userSessions: userSessions{
			stores: make(map[int]SessionStore),
		},
	}
}

type userCount struct {
	n int
	sync.RWMutex
}

func (u *userCount) incr() int {
	u.Lock()
	defer u.Unlock()
	u.n = u.n + 1
	return u.n
}

type userSessions struct {
	stores map[int]SessionStore
	sync.RWMutex
}

func (u *userSessions) GetOrCreate(key int) SessionStore {
	u.Lock()
	defer u.Unlock()
	s, ok := u.stores[key]
	if ok {
		log.Println("existing user ", key)
		return s
	}
	s = &store{
		data: make(M),
	}
	u.stores[key] = s
	return s
}

type websocketController struct {
	name      string
	userCount userCount
	controlOpt
	cookieStore      *sessions.CookieStore
	topicConnections map[string]map[string]*websocket.Conn
	userSessions     userSessions
	sync.RWMutex
}

func (wc *websocketController) addConnection(topic, connID string, sess *websocket.Conn) {
	wc.Lock()
	defer wc.Unlock()
	_, ok := wc.topicConnections[topic]
	if !ok {
		// topic doesn't exit. create
		wc.topicConnections[topic] = make(map[string]*websocket.Conn)
	}
	wc.topicConnections[topic][connID] = sess
	log.Println("addConnection", topic, connID, len(wc.topicConnections[topic]))
}

func (wc *websocketController) removeConnection(topic, connID string) {
	wc.Lock()
	defer wc.Unlock()
	connMap, ok := wc.topicConnections[topic]
	if !ok {
		return
	}
	// delete connection from topic
	conn, ok := connMap[connID]
	if ok {
		delete(connMap, connID)
		conn.Close()
	}
	// no connections for the topic, remove it
	if len(connMap) == 0 {
		delete(wc.topicConnections, topic)
	}

	log.Println("removeConnection", topic, connID, len(wc.topicConnections[topic]))
}

func (wc *websocketController) getTopicConnections(topic string) map[string]*websocket.Conn {
	wc.Lock()
	defer wc.Unlock()
	connMap, ok := wc.topicConnections[topic]
	if !ok {
		log.Printf("warn: topic %v doesn't exist\n", topic)
		return map[string]*websocket.Conn{}
	}
	return connMap
}

func (wc *websocketController) NewView(page string, options ...ViewOption) http.HandlerFunc {
	o := &viewOpt{
		layout:            "./templates/layouts/index.html",
		layoutContentName: "content",
		partials:          []string{"./templates/partials"},
		extensions:        []string{".html", ".tmpl"},
		funcMap:           sprig.FuncMap(),
	}
	for _, option := range options {
		option(o)
	}

	var pageTemplate *template.Template
	var errorTemplate *template.Template
	var err error

	parseTemplates := func() {
		// layout
		files := []string{o.layout}
		// global partials
		for _, p := range o.partials {
			files = append(files, find(p, o.extensions)...)
		}

		// page and its partials
		files = append(files, find(page, o.extensions)...)
		// contains: 1. layout 2. page  3. partials
		pageTemplate, err = template.New("").Funcs(o.funcMap).ParseFiles(files...)
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
			errorFiles := []string{o.layout}
			// global partials
			for _, p := range o.partials {
				errorFiles = append(errorFiles, find(p, o.extensions)...)
			}
			// error page and its partials
			errorFiles = append(errorFiles, find(page, o.extensions)...)
			// contains: 1. layout 2. page  3. partials
			errorTemplate, err = template.New("").Funcs(o.funcMap).ParseFiles(errorFiles...)
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

	mountData := make(map[string]interface{})
	status := 200
	renderPage := func(w http.ResponseWriter, r *http.Request) {

		if o.onMountFunc != nil {
			status, mountData = o.onMountFunc(r)
		}
		w.WriteHeader(status)
		if status > 299 {
			// TODO: custom error page
			w.Write([]byte(fmt.Sprintf(
				`<div style="text-align:center"><h1>%d</h1></div>
<div style="text-align:center"><a href="javascript:history.back()">back</a></div>`, status)))
			return
		}

		if wc.disableTemplateCache {
			parseTemplates()
		}

		err = pageTemplate.ExecuteTemplate(w, filepath.Base(o.layout), mountData)
		if err != nil {
			if errorTemplate != nil {
				err = errorTemplate.ExecuteTemplate(w, filepath.Base(o.layout), nil)
				if err != nil {
					w.Write([]byte("something went wrong"))
				}
			} else {
				w.Write([]byte("something went wrong"))
			}
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
		store := wc.userSessions.GetOrCreate(user)
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

			eventHandler, ok := o.eventHandlers[event.ID]
			if !ok {
				log.Printf("err: no handler found for event %s\n", event.ID)
				continue
			}

			if wc.disableTemplateCache {
				parseTemplates()
			}

			sess := session{
				messageType:          mt,
				conns:                wc.getTopicConnections(*topic),
				store:                store,
				rootTemplate:         pageTemplate,
				event:                *event,
				temporaryKeys:        []string{"action", "target", "targets", "template"},
				enableHTMLFormatting: wc.enableHTMLFormatting,
				requestContext:       ctx,
			}
			sess.unsetError()
			err = eventHandler(sess)
			if err != nil {
				log.Printf("%s: err: %v\n", event.ID, err)
				userMessage := "internal error"
				if userError := errors.Unwrap(err); userError != nil {
					userMessage = userError.Error()
				}
				sess.setError(userMessage, err)
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
