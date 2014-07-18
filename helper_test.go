package main

import (
	"fmt"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// Inspired by https://github.com/benbjohnson/testing

// equals fails the test if got is not equal to want.
func equals(t testing.TB, got, want interface{}) {
	if want == nil && (got == nil || reflect.ValueOf(got).IsNil()) {
		// Accept any nil interface value
		return
	}
	if !reflect.DeepEqual(got, want) {
		_, file, line, _ := runtime.Caller(1)
		fmt.Printf("\t%s:%d: got: %#v; want: %#v\n", filepath.Base(file), line, got, want)
		t.FailNow()
	}
}

// approx tests approximate equality of floats.
// Note that this is a fraught topic. This is a very naive comparison.
func approx(t testing.TB, got, want float64) {
	f1 := got
	f2 := want
	if f1 == f2 {
		return
	} else if f1 > f2 {
		f1, f2 = f2, f1
	}
	delta := (f2 - f1) / f1
	if delta > 0.0001 { // Accept want up to 0.01% greater than got
		_, file, line, _ := runtime.Caller(1)
		fmt.Printf("\t%s:%d: got %v; want %v\n", filepath.Base(file), line, got, want)
		t.FailNow()
	}
}
