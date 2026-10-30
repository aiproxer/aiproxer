package aipapi

import "testing"

func TestJoinInstructionText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   []Message
		want string
	}{
		{
			name: "nil",
			in:   nil,
			want: "",
		},
		{
			name: "empty",
			in:   []Message{},
			want: "",
		},
		{
			name: "skips_non_text_and_blank",
			in: []Message{
				{Parts: []Part{
					{Kind: PartText, Text: "  "},
					{Kind: PartImageRef, ImageRef: "ref"},
				}},
			},
			want: "",
		},
		{
			name: "joins_multiple_text_parts_with_blank_line",
			in: []Message{
				{Parts: []Part{
					{Kind: PartText, Text: "first"},
				}},
				{Parts: []Part{
					{Kind: PartText, Text: "second"},
				}},
			},
			want: "first\n\nsecond",
		},
		{
			name: "trims_outer_whitespace",
			in: []Message{
				{Parts: []Part{
					{Kind: PartText, Text: "  hello  "},
				}},
			},
			want: "hello",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := JoinInstructionText(tt.in)
			if got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}
