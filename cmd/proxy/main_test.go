package main

import (
	"slices"
	"testing"
)

func TestParseBackends(t *testing.T) {
	tests := []struct {
		in      string
		want    []string
		wantErr bool
	}{
		{"http://localhost:9001", []string{"http://localhost:9001"}, false},
		{"http://a:1, http://b:2 ,http://c:3", []string{"http://a:1", "http://b:2", "http://c:3"}, false},
		{"http://a:1,,", []string{"http://a:1"}, false},
		{"https://api.example.com", []string{"https://api.example.com"}, false},
		{"localhost:9001", nil, true}, // no scheme: parses, but as scheme "localhost"
		{"ftp://a:1", nil, true},
		{"http://", nil, true},
		{"http://a:1,oops", nil, true}, // one bad entry rejects the whole list
		{"", nil, true},
		{" , ", nil, true},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseBackends(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			var gotStrs []string
			for _, u := range got {
				gotStrs = append(gotStrs, u.String())
			}
			if !slices.Equal(gotStrs, tc.want) {
				t.Errorf("got %v, want %v", gotStrs, tc.want)
			}
		})
	}
}
