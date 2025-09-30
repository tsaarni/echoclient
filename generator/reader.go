package generator

import (
	"io"
	"math/rand"
	"time"
)

type ContentMode int

const (
	modeASCII ContentMode = iota
	modeRandom
)

const (
	KB = 1024
	MB = 1024 * KB

	asciiPrintableStart = byte(' ')
	asciiPrintableEnd   = byte('~')
	asciiPrintableRange = int(asciiPrintableEnd - asciiPrintableStart + 1)
)

type ReaderOption func(*Reader)

type Reader struct {
	sizeRemaining     uint64
	chunkSize         uint64
	asciiPatternIndex int // index for cycling through printable ASCII
	mode              ContentMode
	randSrc           *rand.Rand
}

// NewReader creates an io.Reader that yields deterministic byte data according to options.
func NewReader(opts ...ReaderOption) io.Reader {
	r := &Reader{
		sizeRemaining:     0,
		chunkSize:         64 * KB,
		asciiPatternIndex: 0,
		mode:              modeASCII,
		randSrc:           nil,
	}
	for _, o := range opts {
		o(r)
	}
	if r.sizeRemaining < 0 {
		r.sizeRemaining = 0
	}
	return r
}

// WithASCII sets the reader to generate ASCII byte data.
func WithASCII(mode ContentMode) ReaderOption {
	return func(r *Reader) {
		r.mode = modeASCII
	}
}

// WithRandom sets the reader to generate random byte data.
func WithRandom() ReaderOption {
	return func(r *Reader) {
		r.mode = modeRandom
		r.randSrc = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
}

// WithRandomSeed sets the reader to generate random byte data using the provided seed.
func WithRandomSeed(seed int64) ReaderOption {
	return func(r *Reader) {
		r.mode = modeRandom
		r.randSrc = rand.New(rand.NewSource(seed))
	}
}

// WithTotalSize sets the total size of data to generate. Once this size is reached, Read returns io.EOF.
func WithTotalSize(n uint64) ReaderOption {
	return func(r *Reader) {
		r.sizeRemaining = n
	}
}

// WithChunkSize sets the maximum chunk size for each Read call.
func WithChunkSize(n uint64) ReaderOption {
	return func(r *Reader) {
		if n > 0 {
			r.chunkSize = n
		}
	}
}

// Read implements io.Reader, generating data according to the configured mode.
func (r *Reader) Read(p []byte) (int, error) {
	if r.sizeRemaining == 0 {
		return 0, io.EOF
	}

	// Determine how many bytes to write this call: min(len(p), chunkSize, sizeRemaining)
	toWrite := r.sizeRemaining
	if toWrite > r.chunkSize {
		toWrite = r.chunkSize
	}
	if toWrite > uint64(len(p)) {
		toWrite = uint64(len(p))
	}

	if toWrite == 0 {
		return 0, io.EOF
	}

	switch r.mode {
	case modeRandom:
		r.fillRandom(p, toWrite)
	case modeASCII:
		fallthrough
	default:
		r.fillASCII(p, toWrite)
	}

	r.sizeRemaining -= toWrite

	return int(toWrite), nil
}

// fillRandom fills buffer with random bytes up to toWrite bytes.
func (r *Reader) fillRandom(buffer []byte, toWrite uint64) {
	r.randSrc.Read(buffer[:toWrite])
}

// fillASCII fills buffer with an ASCII pattern up to toWrite bytes.
func (r *Reader) fillASCII(buffer []byte, toWrite uint64) {
	for i := uint64(0); i < toWrite; i++ {
		buffer[i] = asciiPrintableStart + byte(r.asciiPatternIndex)
		r.asciiPatternIndex++
		if r.asciiPatternIndex >= asciiPrintableRange {
			r.asciiPatternIndex = 0
		}
	}
}
