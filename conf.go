package main

import (
	"flag"
	"log"
	"os"
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"
)

// emptyFields takes a pointer to a struct type and returns a slice of json tags of its empty fields.
// NOTE: This function panics if s is not a pointer to a struct type.
func emptyFields(s interface{}) []string {
	empty := []string{}
	v := reflect.ValueOf(s).Elem()
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		name := v.Type().Field(i).Tag.Get("toml")
		zero := reflect.Zero(field.Type())
		if reflect.DeepEqual(field.Interface(), zero.Interface()) {
			empty = append(empty, name)
		}
	}
	return empty
}

// filterNamespace replaces templated fields in the user-provided namespace and sanitizes it.
func filterNamespace(ns string) string {
	hostname, err := os.Hostname()
	if err != nil {
		log.Fatal(err)
	}
	ns = strings.NewReplacer("%H", hostname).Replace(ns)
	return sanitizeKey(ns)
}

func parseConf() {
	flag.Parse()
	conf = &Conf{}
	f, err := os.Open(*configFile)
	if err != nil {
		log.Fatal(err)
	}
	if _, err = toml.DecodeReader(f, conf); err != nil {
		log.Fatalf("Error decoding %s: %s\n", *configFile, err)
	}
	empty := emptyFields(conf)
	if len(empty) > 0 {
		log.Fatalf("Missing fields in %s: %v\n", *configFile, empty)
	}

	parts := strings.Split(conf.Namespace, ".")
	namespace = make([]string, len(parts))
	for i, part := range parts {
		namespace[i] = filterNamespace(part)
	}
}
