package runner

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// boundedBuffer keeps the first and last max/2 bytes of a stream. Failures are
// usually explained at the end of the output, so dropping the middle keeps
// both the context of how a job started and how it ended.
type boundedBuffer struct {
	head    []byte
	tail    []byte // ring buffer once full
	tailPos int    // next write position in tail
	headCap int
	tailCap int
	total   int64
}

func newBoundedBuffer(max int64) *boundedBuffer {
	return &boundedBuffer{headCap: int(max - max/2), tailCap: int(max / 2)}
}

func (b *boundedBuffer) Write(p []byte) {
	b.total += int64(len(p))
	if n := min(b.headCap-len(b.head), len(p)); n > 0 {
		b.head = append(b.head, p[:n]...)
		p = p[n:]
	}
	if b.tailCap == 0 || len(p) == 0 {
		return
	}
	if len(p) >= b.tailCap {
		b.tail = append(b.tail[:0], p[len(p)-b.tailCap:]...)
		b.tailPos = 0
		return
	}
	for len(p) > 0 {
		if len(b.tail) < b.tailCap {
			n := min(b.tailCap-len(b.tail), len(p))
			b.tail = append(b.tail, p[:n]...)
			p = p[n:]
			continue
		}
		n := copy(b.tail[b.tailPos:], p)
		b.tailPos = (b.tailPos + n) % b.tailCap
		p = p[n:]
	}
}

func (b *boundedBuffer) Truncated() bool {
	return b.total > int64(len(b.head)+len(b.tail))
}

func (b *boundedBuffer) String() string {
	tail := append(append([]byte{}, b.tail[b.tailPos:]...), b.tail[:b.tailPos]...)
	if !b.Truncated() {
		return string(b.head) + string(tail)
	}
	head := b.head
	// Do not split multi-byte characters at the cut points.
	for i := len(head) - 1; i >= 0 && i >= len(head)-utf8.UTFMax; i-- {
		if utf8.RuneStart(head[i]) {
			if !utf8.FullRune(head[i:]) {
				head = head[:i]
			}
			break
		}
	}
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	if len(head) == 0 && len(tail) == 0 {
		return ""
	}
	dropped := b.total - int64(len(head)+len(tail))
	var out strings.Builder
	out.Write(head)
	fmt.Fprintf(&out, "\n... [cronwatch: %d bytes truncated] ...\n", dropped)
	out.Write(tail)
	return out.String()
}
