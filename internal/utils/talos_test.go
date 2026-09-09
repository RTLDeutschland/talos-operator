package utils

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseOSRelease(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    map[string]string
		wantErr bool
	}{
		{
			name: "full os-release",
			input: `NAME="Talos Linux"
VERSION="1.12.6"
ID=talos
ID_LIKE="debian"
VERSION_ID="1.12.6"
PRETTY_NAME="Talos Linux v1.12.6"
HOME_URL="https://www.talos.dev/"
BUG_REPORT_URL="https://github.com/siderolabs/talos/issues"
`,
			want: map[string]string{
				"NAME":           "Talos Linux",
				"VERSION":        "1.12.6",
				"ID":             "talos",
				"ID_LIKE":        "debian",
				"VERSION_ID":     "1.12.6",
				"PRETTY_NAME":    "Talos Linux v1.12.6",
				"HOME_URL":       "https://www.talos.dev/",
				"BUG_REPORT_URL": "https://github.com/siderolabs/talos/issues",
			},
			wantErr: false,
		},
		{
			name:    "empty input",
			input:   "",
			want:    map[string]string{},
			wantErr: false,
		},
		{
			name: "ignores comments and blank lines",
			input: `# comment
NAME=Talos

# another comment
VERSION=1.12.6
`,
			want: map[string]string{
				"NAME":    "Talos",
				"VERSION": "1.12.6",
			},
			wantErr: false,
		},
		{
			name:  "handles unquoted values",
			input: "ID=talos\nVERSION=1.12.6\n",
			want: map[string]string{
				"ID":      "talos",
				"VERSION": "1.12.6",
			},
			wantErr: false,
		},
		{
			name: "handles double quoted values",
			input: `NAME="Talos Linux"
VERSION="1.12.6"
`,
			want: map[string]string{
				"NAME":    "Talos Linux",
				"VERSION": "1.12.6",
			},
			wantErr: false,
		},
		{
			name:  "single quotes are preserved",
			input: "NAME='Talos Linux'\nVERSION='1.12.6'\n",
			want: map[string]string{
				"NAME":    "'Talos Linux'",
				"VERSION": "'1.12.6'",
			},
			wantErr: false,
		},
		{
			name:  "key-value pairs with equals sign in value",
			input: "notvalid\nNAME=Talos\nalsonotvalid=\n",
			want: map[string]string{
				"NAME":         "Talos",
				"alsonotvalid": "",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseOSRelease(strings.NewReader(tt.input))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseExtensions(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *Extensions
		wantErr bool
	}{
		{
			name: "with schematic",
			input: `layers:
  - image: registry.example/image1:latest
    metadata:
      name: other
      version: v1.0.0
  - image: registry.example/schematic:latest
    metadata:
      name: schematic
      version: 376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba
`,
			want: &Extensions{
				Layers: []ExtensionLayer{
					{
						Image: "registry.example/image1:latest",
						Metadata: ExtensionMetadata{
							Name:    "other",
							Version: "v1.0.0",
						},
					},
					{
						Image: "registry.example/schematic:latest",
						Metadata: ExtensionMetadata{
							Name:    "schematic",
							Version: "376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name:    "empty yaml",
			input:   "",
			want:    &Extensions{},
			wantErr: false,
		},
		{
			name:    "empty object",
			input:   "{}",
			want:    &Extensions{},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseExtensions(strings.NewReader(tt.input))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestGetSchematicID(t *testing.T) {
	tests := []struct {
		name       string
		extensions *Extensions
		want       string
	}{
		{
			name: "with schematic as last layer",
			extensions: &Extensions{
				Layers: []ExtensionLayer{
					{
						Image:    "registry.example/other:v1.0",
						Metadata: ExtensionMetadata{Name: "other", Version: "v1.0"},
					},
					{
						Image:    "registry.example/schematic:v1.0",
						Metadata: ExtensionMetadata{Name: "schematic", Version: "abc123"},
					},
				},
			},
			want: "abc123",
		},
		{
			name: "schematic not last layer",
			extensions: &Extensions{
				Layers: []ExtensionLayer{
					{
						Image:    "registry.example/schematic:v1.0",
						Metadata: ExtensionMetadata{Name: "schematic", Version: "abc123"},
					},
					{
						Image:    "registry.example/other:v1.0",
						Metadata: ExtensionMetadata{Name: "other", Version: "v1.0"},
					},
				},
			},
			want: "",
		},
		{
			name: "no schematic layer",
			extensions: &Extensions{
				Layers: []ExtensionLayer{
					{
						Image:    "registry.example/other:v1.0",
						Metadata: ExtensionMetadata{Name: "other", Version: "v1.0"},
					},
				},
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.extensions.GetSchematicID()
			require.Equal(t, tt.want, got)
		})
	}
}
