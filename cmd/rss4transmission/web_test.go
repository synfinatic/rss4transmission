package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseHistoryAddr(t *testing.T) {
	cases := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"8080", "127.0.0.1:8080", false},
		{"127.0.0.1:8080", "127.0.0.1:8080", false},
		{"0.0.0.0:9090", "0.0.0.0:9090", false},
		{"[::1]:8080", "[::1]:8080", false},
		{"notaport", "", true},
		{"999999", "", true},
		{"0", "", true},
		{"-1", "", true},
		{"host:notaport", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := parseHistoryAddr(tc.input)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
			}
		})
	}
}
