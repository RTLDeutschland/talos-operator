package controller

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSha256(t *testing.T) {
	require.Equal(
		t,
		sha256Sum([]byte{}),
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	)
	require.Equal(
		t,
		sha256Sum([]byte{0x00}),
		"6e340b9cffb37a989ca544e6bb780a2c78901d3fb33738768511a30617afa01d",
	)
	require.Equal(
		t,
		sha256Sum([]byte("hello world")),
		"b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9",
	)
}
