package main

import "testing"

func BenchmarkParseStatsdMessage(b *testing.B) {
	// Rotate through a few different messages
	messages := [][]byte{
		[]byte("foo:1|c"),
		[]byte("foo bar baz:1|c"),
		[]byte("asdf:3|c|@0.456"),
		[]byte("<asdf>:3|c"),
		[]byte("a/b/c:300|g"),
	}
	for i := 0; i < b.N; i++ {
		msg := messages[i%len(messages)]
		_, ok := parseStatsdMessage(msg)
		if !ok {
			b.Fatalf("Failed to parse message: %s", msg)
		}
	}
}
