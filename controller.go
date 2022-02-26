package controller

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/securecookie"

	"github.com/gorilla/sessions"

	"github.com/gorilla/websocket"
)

type Controller interface {
	Handler(view View) http.HandlerFunc
}

type controlOpt struct {
	subscribeTopicFunc func(r *http.Request) *string
	upgrader           websocket.Upgrader

	enableHTMLFormatting bool
	disableTemplateCache bool
	debugLog             bool
	enableWatch          bool
	watchPaths           []string
	developmentMode      bool
	errorView            View
}

type Option func(*controlOpt)

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

func WithErrorView(view View) Option {
	return func(o *controlOpt) {
		o.errorView = view
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

func EnableDebugLog() Option {
	return func(o *controlOpt) {
		o.debugLog = true
	}
}

func EnableWatch(paths ...string) Option {
	return func(o *controlOpt) {
		o.enableWatch = true
		if len(paths) > 0 {
			o.watchPaths = paths
		}
	}
}

func DevelopmentMode(enable bool) Option {
	return func(o *controlOpt) {
		o.developmentMode = enable
	}
}

func Websocket(name string, options ...Option) Controller {
	if name == "" {
		panic("controller name is required")
	}

	o := &controlOpt{
		subscribeTopicFunc: func(r *http.Request) *string {
			topic := "root"
			if r.URL.Path != "/" {
				topic = strings.Replace(r.URL.Path, "/", "_", -1)
			}

			log.Println("client subscribed to topic: ", topic)
			return &topic
		},
		upgrader:   websocket.Upgrader{EnableCompression: true},
		watchPaths: []string{"./templates"},
		errorView:  &DefaultErrorView{},
	}

	for _, option := range options {
		option(o)
	}

	wc := &websocketController{
		cookieStore:      sessions.NewCookieStore(securecookie.GenerateRandomKey(32)),
		topicConnections: make(map[string]map[string]*websocket.Conn),
		controlOpt:       *o,
		name:             name,
		userSessions: userSessions{
			stores: make(map[int]Store),
		},
	}
	log.Println("controller starting in developer mode ...", wc.developmentMode)
	if wc.developmentMode {
		wc.debugLog = true
		wc.enableWatch = true
		wc.enableHTMLFormatting = true
		wc.disableTemplateCache = true
	}

	if wc.enableWatch {
		go watchTemplates(wc)
	}
	return wc
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
	stores map[int]Store
	sync.RWMutex
}

func (u *userSessions) getOrCreate(key int) Store {
	u.Lock()
	defer u.Unlock()
	s, ok := u.stores[key]
	if ok {
		log.Println("existing user ", key)
		return s
	}
	s = &inmemStore{
		data: make(map[string][]byte),
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

func (wc *websocketController) getAllConnections() map[string]*websocket.Conn {
	wc.Lock()
	defer wc.Unlock()
	conns := make(map[string]*websocket.Conn)
	for _, cm := range wc.topicConnections {
		for k, m := range cm {
			conns[k] = m
		}
	}

	return conns
}

func (wc *websocketController) message(topic string, message []byte) {
	wc.Lock()
	defer wc.Unlock()
	preparedMessage, err := websocket.NewPreparedMessage(websocket.TextMessage, message)
	if err != nil {
		log.Printf("err preparing message %v\n", err)
		return
	}

	conns, ok := wc.topicConnections[topic]
	if !ok {
		log.Printf("warn: topic %v doesn't exist\n", topic)
		return
	}

	for connID, conn := range conns {
		err := conn.WritePreparedMessage(preparedMessage)
		if err != nil {
			log.Printf("error: writing message for topic:%v, closing conn %s with err %v", topic, connID, err)
			conn.Close()
			continue
		}
	}
}

func (wc *websocketController) messageAll(message []byte) {
	wc.Lock()
	defer wc.Unlock()
	preparedMessage, err := websocket.NewPreparedMessage(websocket.TextMessage, message)
	if err != nil {
		log.Printf("err preparing message %v\n", err)
		return
	}

	for _, cm := range wc.topicConnections {
		for connID, conn := range cm {
			err := conn.WritePreparedMessage(preparedMessage)
			if err != nil {
				log.Printf("error: writing message %v, closing conn %s with err %v", message, connID, err)
				conn.Close()
				continue
			}
		}
	}
}

func (wc *websocketController) getUser(w http.ResponseWriter, r *http.Request) (int, error) {
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
		log.Printf("getUser err %v\n", err)
		return -1, err
	}

	return user.(int), nil
}

func (wc *websocketController) Handler(view View) http.HandlerFunc {
	viewTemplate, err := parseTemplate(view)
	if err != nil {
		panic(err)
	}

	errorViewTemplate, err := parseTemplate(wc.errorView)
	if err != nil {
		panic(err)
	}

	mountData := make(M)
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := wc.getUser(w, r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		v := &viewHandler{
			view:              view,
			errorView:         wc.errorView,
			viewTemplate:      viewTemplate,
			errorViewTemplate: errorViewTemplate,
			mountData:         mountData,
			wc:                wc,
			user:              user,
		}
		if r.Header.Get("Connection") == "Upgrade" &&
			r.Header.Get("Upgrade") == "websocket" {
			onEvent(w, r, v)
		} else {
			onMount(w, r, v)
		}
	}
}
