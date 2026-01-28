package generator

import (
	"bytes"
	"io"
	"testing"

	"github.com/dustin/go-humanize"
)

func TestNewReaderDefaults(t *testing.T) {
	r := NewReader()
	buf := make([]byte, 100)
	n, err := r.Read(buf)
	// Default sizeRemaining is 0, so should return EOF immediately.
	if err != io.EOF {
		t.Errorf("expected io.EOF, got %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 bytes read, got %d", n)
	}
}

func TestNewReaderWithTotalSize(t *testing.T) {
	r := NewReader(WithTotalSize(100))
	buf := make([]byte, 1000)
	n, err := r.Read(buf)
	if err != nil {
		t.Errorf("unexpected error on first read: %v", err)
	}
	if n != 100 {
		t.Errorf("expected 100 bytes read, got %d", n)
	}

	// Second read should return EOF.
	n, err = r.Read(buf)
	if err != io.EOF {
		t.Errorf("expected io.EOF on second read, got %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 bytes on second read, got %d", n)
	}
}

func TestWithASCII(t *testing.T) {
	r := NewReader(WithTotalSize(95), WithASCII())
	buf := make([]byte, 200)
	n, err := r.Read(buf)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if n != 95 {
		t.Errorf("expected 95 bytes, got %d", n)
	}

	// Check that it contains printable ASCII (space to tilde).
	for i := 0; i < n; i++ {
		if buf[i] < ' ' || buf[i] > '~' {
			t.Errorf("byte %d is not printable ASCII: %d", i, buf[i])
		}
	}
}

func TestWithRandom(t *testing.T) {
	r := NewReader(WithTotalSize(100), WithRandom())
	buf := make([]byte, 100)
	n, err := r.Read(buf)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if n != 100 {
		t.Errorf("expected 100 bytes, got %d", n)
	}
	// Random should have entropy; a simple check that it's not all zeros.
	allZeros := true
	for i := 0; i < n; i++ {
		if buf[i] != 0 {
			allZeros = false
			break
		}
	}
	if allZeros {
		t.Error("random buffer is unexpectedly all zeros")
	}
}

func TestWithRandomSeed(t *testing.T) {
	// Two readers with the same seed should produce the same output.
	r1 := NewReader(WithTotalSize(100), WithRandomSeed(12345))
	r2 := NewReader(WithTotalSize(100), WithRandomSeed(12345))

	buf1 := make([]byte, 100)
	buf2 := make([]byte, 100)

	r1.Read(buf1)
	r2.Read(buf2)

	if !bytes.Equal(buf1, buf2) {
		t.Error("readers with same seed should produce identical output")
	}
}

func TestWithChunkSize(t *testing.T) {
	r := NewReader(WithTotalSize(1000), WithChunkSize(100))
	buf := make([]byte, 500)
	n, err := r.Read(buf)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	// Should read only chunkSize bytes even though buffer is larger.
	if n != 100 {
		t.Errorf("expected 100 bytes (chunk size), got %d", n)
	}
}

func TestWithChunkSizeZero(t *testing.T) {
	// ChunkSize of 0 should not change the default.
	r := NewReader(WithTotalSize(100), WithChunkSize(0)).(*Reader)
	if r.chunkSize != 64*humanize.KiByte {
		t.Errorf("expected default chunkSize %d, got %d", 64*humanize.KiByte, r.chunkSize)
	}
}

func TestReadMultipleChunks(t *testing.T) {
	r := NewReader(WithTotalSize(300), WithChunkSize(100))
	buf := make([]byte, 100)
	totalRead := 0

	for {
		n, err := r.Read(buf)
		totalRead += n
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if totalRead != 300 {
		t.Errorf("expected total 300 bytes, got %d", totalRead)
	}
}

func TestASCIIPatternCycles(t *testing.T) {
	// Test that ASCII pattern cycles correctly.
	printableRange := int('~' - ' ' + 1) // 95 characters
	r := NewReader(WithTotalSize(uint64(printableRange*2)), WithASCII())
	buf := make([]byte, printableRange*2)

	n, _ := r.Read(buf)
	if n != printableRange*2 {
		t.Fatalf("expected %d bytes, got %d", printableRange*2, n)
	}

	// First cycle should start with ' '.
	if buf[0] != ' ' {
		t.Errorf("expected first byte to be ' ', got %c", buf[0])
	}

	// End of first cycle should be '~'.
	if buf[printableRange-1] != '~' {
		t.Errorf("expected byte at %d to be '~', got %c", printableRange-1, buf[printableRange-1])
	}

	// Start of second cycle should be ' ' again.
	if buf[printableRange] != ' ' {
		t.Errorf("expected byte at %d to be ' ', got %c", printableRange, buf[printableRange])
	}
}

func TestSmallBufferReads(t *testing.T) {
	r := NewReader(WithTotalSize(1000), WithChunkSize(500))
	buf := make([]byte, 10) // Buffer smaller than chunk size.
	n, err := r.Read(buf)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	// Should read only buffer size since it's smaller than chunk size.
	if n != 10 {
		t.Errorf("expected 10 bytes (buffer size), got %d", n)
	}
}
