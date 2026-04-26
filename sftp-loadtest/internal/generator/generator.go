package generator

import (
	"crypto/rand"
	"io"
	mathrand "math/rand"
	"path"
	"strings"
	"sync"
	"time"
)

// NameFromPattern turns "invoice*" or "invoice*.csv" into a unique filename.
// The trailing * is replaced with a unix-nanosecond timestamp plus a tiny random tail
// so concurrent uploads never collide.
func NameFromPattern(pattern string) string {
	ts := time.Now().UnixNano()
	ext := path.Ext(pattern)

	base := pattern
	if ext != "" {
		base = strings.TrimSuffix(pattern, ext)
	}
	base = strings.TrimSuffix(base, "*")

	if ext == "" {
		ext = ".txt"
	}
	return base + itoa(ts) + "_" + itoa(int64(mathrand.Intn(1<<24))) + ext
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [24]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Two 1 MiB seed buffers, filled once at first use: one with raw random bytes
// (binary payloads) and one with printable ASCII including newlines (text-like
// payloads). Readers copy from whichever buffer matches the requested content
// type, at memory-copy speed.
var (
	binarySeedOnce sync.Once
	binarySeed     []byte
	asciiSeedOnce  sync.Once
	asciiSeed      []byte
)

const seedBufSize = 1 << 20 // 1 MiB

func ensureBinarySeed() {
	binarySeedOnce.Do(func() {
		binarySeed = make([]byte, seedBufSize)
		_, _ = rand.Read(binarySeed)
	})
}

func ensureASCIISeed() {
	asciiSeedOnce.Do(func() {
		asciiSeed = make([]byte, seedBufSize)
		// Fill with printable ASCII (space through ~) peppered with line
		// breaks every ~80 chars so the bytes resemble real text files.
		r := make([]byte, seedBufSize)
		_, _ = rand.Read(r)
		col := 0
		for i := range asciiSeed {
			if col >= 79 && i+1 < len(asciiSeed) {
				asciiSeed[i] = '\n'
				col = 0
				continue
			}
			// Map random byte to printable ASCII range [32, 126] (95 values).
			asciiSeed[i] = 32 + (r[i] % 95)
			col++
		}
	})
}

// Content types accepted by FastReader.
const (
	ContentBinary = "binary"
	ContentASCII  = "ascii"
	ContentRandom = "random" // choose binary or ascii per call (50/50)
)

// FastReader returns an io.Reader that yields exactly `size` bytes by cycling
// over a pre-filled seed buffer matching the requested content type.
func FastReader(size int64, kind string) io.Reader {
	switch kind {
	case ContentASCII:
		ensureASCIISeed()
		return &fastReader{remaining: size, src: asciiSeed}
	case ContentRandom:
		// Flip a coin per file so a run produces a realistic mix.
		var coin [1]byte
		_, _ = rand.Read(coin[:])
		if coin[0]&1 == 0 {
			ensureASCIISeed()
			return &fastReader{remaining: size, src: asciiSeed}
		}
		fallthrough
	default: // ContentBinary and anything unknown
		ensureBinarySeed()
		return &fastReader{remaining: size, src: binarySeed}
	}
}

type fastReader struct {
	remaining int64
	pos       int
	src       []byte
}

func (f *fastReader) Read(p []byte) (int, error) {
	if f.remaining <= 0 {
		return 0, io.EOF
	}
	want := len(p)
	if int64(want) > f.remaining {
		want = int(f.remaining)
	}
	written := 0
	for written < want {
		n := copy(p[written:want], f.src[f.pos:])
		written += n
		f.pos += n
		if f.pos >= len(f.src) {
			f.pos = 0
		}
	}
	f.remaining -= int64(want)
	return want, nil
}

// RandomReader stays binary for backward-compat with existing callers.
func RandomReader(size int64) io.Reader { return FastReader(size, ContentBinary) }
