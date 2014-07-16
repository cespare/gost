package main

import "testing"

func BenchmarkParseSimple(b *testing.B) {
	msg := []byte("foo:1|c")
	for i := 0; i < b.N; i++ {
		_, ok := parseStatsdMessage(msg, true)
		if !ok {
			b.Fatalf("Failed to parse message: %s", msg)
		}
	}
}

func BenchmarkParseComplex(b *testing.B) {
	msg := []byte("<[foo bar/baz.quux ]>:123.456|c|@0.5678")
	for i := 0; i < b.N; i++ {
		_, ok := parseStatsdMessage(msg, true)
		if !ok {
			b.Fatalf("Failed to parse message: %s", msg)
		}
	}
}
