package controller

import (
	"encoding/json"
	"fmt"
	"html/template"

	"github.com/Masterminds/sprig"
)

func DefaultFuncMap() template.FuncMap {
	allFuncs := make(template.FuncMap)
	for k, v := range sprig.FuncMap() {
		allFuncs[k] = v
	}
	allFuncs["bytesToMap"] = bytesToMap
	allFuncs["bytesToString"] = bytesToString
	allFuncs["stdout"] = stdout
	return allFuncs
}

func bytesToMap(data []byte) map[string]interface{} {
	m := make(map[string]interface{})
	err := json.Unmarshal(data, &m)
	if err != nil {
		panic(err)
	}
	return m
}

func bytesToString(data []byte) string {
	return string(data)
}

func stdout(key string, val interface{}) interface{} {
	fmt.Printf("%v: %v", key, val)
	return val
}
