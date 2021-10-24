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
