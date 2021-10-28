package controller

import (
	"encoding/json"
	"fmt"
	"sync"
)

type store struct {
	data map[string][]byte
	sync.RWMutex
}

func (s store) Set(m M) error {
	s.Lock()
	defer s.Unlock()
	for k, v := range m {
		data, err := json.Marshal(&v)
		if err != nil {
			return err
		}
		s.data[k] = data
	}
	return nil
}

func (s store) Decode(key string, v interface{}) error {
	s.RLock()
	defer s.RUnlock()
	data, ok := s.data[key]
	if !ok {
		return fmt.Errorf("key not found")
	}
	err := json.Unmarshal(data, v)
	if err != nil {
		return err
	}
	return nil
}
