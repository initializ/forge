package a2a

import "testing"

// TestMessageFileParts verifies FileParts surfaces exactly the file parts (with
// index + mime + name) and ignores text/data parts — the basis for the #255
// ingest gate that rejects media the runtime can't forward to the model.
func TestMessageFileParts(t *testing.T) {
	tests := []struct {
		name  string
		parts []Part
		want  []MediaPartInfo
	}{
		{
			name:  "text only",
			parts: []Part{NewTextPart("hi")},
			want:  nil,
		},
		{
			name:  "data only",
			parts: []Part{NewDataPart(map[string]any{"k": "v"})},
			want:  nil,
		},
		{
			name: "single file part",
			parts: []Part{
				NewTextPart("look at this"),
				NewFilePart(FileContent{Name: "photo.png", MimeType: "image/png", Bytes: []byte{1, 2, 3}}),
			},
			want: []MediaPartInfo{{Index: 1, MimeType: "image/png", Name: "photo.png"}},
		},
		{
			name: "multiple file parts keep their indices",
			parts: []Part{
				NewFilePart(FileContent{Name: "a.pdf", MimeType: "application/pdf"}),
				NewTextPart("between"),
				NewFilePart(FileContent{Name: "b.mp4", MimeType: "video/mp4"}),
			},
			want: []MediaPartInfo{
				{Index: 0, MimeType: "application/pdf", Name: "a.pdf"},
				{Index: 2, MimeType: "video/mp4", Name: "b.mp4"},
			},
		},
		{
			name:  "file part with nil FileContent still reported",
			parts: []Part{{Kind: PartKindFile}},
			want:  []MediaPartInfo{{Index: 0}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Message{Role: MessageRoleUser, Parts: tt.parts}.FileParts()
			if len(got) != len(tt.want) {
				t.Fatalf("FileParts() = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("FileParts()[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}
