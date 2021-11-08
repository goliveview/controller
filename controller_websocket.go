package controller

import (
	"context"
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
	Handle(view View) http.HandlerFunc
}

type controlOpt struct {
	requestContextFunc func(r *http.Request) context.Context
	subscribeTopicFunc func(r *http.Request) *string
	upgrader           websocket.Upgrader

	enableHTMLFormatting bool
	disableTemplateCache bool
	debugLog             bool
	enableWatch          bool
	watchPaths           []string
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
		upgrader:   websocket.Upgrader{EnableCompression: true},
		watchPaths: []string{"./templates"},
	}

	for _, option := range options {
		option(o)
	}

	wc := &websocketController{
		cookieStore:      sessions.NewCookieStore([]byte(securecookie.GenerateRandomKey(32))),
		topicConnections: make(map[string]map[string]*websocket.Conn),
		controlOpt:       *o,
		name:             *name,
		userSessions: userSessions{
			stores: make(map[int]SessionStore),
		},
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
	stores map[int]SessionStore
	sync.RWMutex
}

func (u *userSessions) getOrCreate(key int) SessionStore {
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
