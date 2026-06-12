package controllers

import (
	"testing"
)

// TestSniffImageMime covers the PNG and JPEG magic-byte detection used by the
// intro-image upload handlers (UploadCategoryIntroImage, UploadExamIntroImage).
func TestSniffImageMime(t *testing.T) {
	pngMagic := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	jpegMagic := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46}

	tests := []struct {
		name      string
		buf       []byte
		wantMime  string
		wantError bool
	}{
		{
			name:      "valid PNG header",
			buf:       pngMagic,
			wantMime:  "image/png",
			wantError: false,
		},
		{
			name:      "valid JPEG header",
			buf:       jpegMagic,
			wantMime:  "image/jpeg",
			wantError: false,
		},
		{
			name:      "PNG header plus extra data",
			buf:       append(pngMagic, []byte("rest of file bytes...")...),
			wantMime:  "image/png",
			wantError: false,
		},
		{
			name:      "JPEG header plus extra data",
			buf:       append(jpegMagic, []byte("rest of file bytes...")...),
			wantMime:  "image/jpeg",
			wantError: false,
		},
		{
			name:      "GIF header → unsupported",
			buf:       []byte{0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00},
			wantMime:  "",
			wantError: true,
		},
		{
			name:      "bogus 8-byte buffer → unsupported",
			buf:       []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07},
			wantMime:  "",
			wantError: true,
		},
		{
			name:      "empty buffer → unsupported",
			buf:       []byte{},
			wantMime:  "",
			wantError: true,
		},
		{
			name:      "2-byte buffer (too short for any signature) → unsupported",
			buf:       []byte{0xFF, 0xD8},
			wantMime:  "",
			wantError: true,
		},
		{
			name:      "3-byte JPEG prefix exactly → accepted",
			buf:       []byte{0xFF, 0xD8, 0xFF},
			wantMime:  "image/jpeg",
			wantError: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			mime, err := sniffImageMime(tc.buf)
			if tc.wantError {
				if err == nil {
					t.Errorf("expected error, got mime=%q", mime)
				}
				if mime != "" {
					t.Errorf("expected empty mime on error, got %q", mime)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if mime != tc.wantMime {
					t.Errorf("mime = %q; want %q", mime, tc.wantMime)
				}
			}
		})
	}
}
