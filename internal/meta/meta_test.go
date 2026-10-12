package meta

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Meta
	}{
		{
			name: "full file",
			in: "# YouTube upload: how-a-kettle-works\n\n" +
				"## Title\nHow a kettle knows when to stop #Shorts\n\n" +
				"## Description\nEver wondered why it clicks off?\n\n" +
				"🔥 Water boils\n💨 Steam hits a metal disc\n\n" +
				"#shorts #science\n\n" +
				"## Tags\nkettle, how it works, science for kids\n",
			want: Meta{
				Title:       "How a kettle knows when to stop #Shorts",
				Description: "Ever wondered why it clicks off?\n\n🔥 Water boils\n💨 Steam hits a metal disc\n\n#shorts #science",
				Tags:        []string{"kettle", "how it works", "science for kids"},
			},
		},
		{
			name: "no tags section",
			in:   "# x\n\n## Title\nA title\n\n## Description\nBody.\n",
			want: Meta{Title: "A title", Description: "Body."},
		},
		{
			name: "CRLF, heading case, unknown sections and stray commas",
			in:   "## TITLE\r\nA title\r\n\r\n## Notes\r\nupload as private\r\n\r\n## tags\r\na, ,b,\r\nc\r\n",
			want: Meta{Title: "A title", Tags: []string{"a", "b", "c"}},
		},
		{
			name: "text before the first section is ignored",
			in:   "loose text\n## Title\nT\n# New top-level heading\nnot the title\n",
			want: Meta{Title: "T"},
		},
		{name: "empty", in: "", want: Meta{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Parse(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Parse() =\n%#v\nwant\n%#v", got, tt.want)
			}
		})
	}
}
