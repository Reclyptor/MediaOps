package frames

import (
	"bytes"
	"context"
	"crypto/md5"
	"strings"
	"testing"
)

// nal builds one length-prefixed NAL unit with a header byte and filler.
func nal(lenSize int, header byte, body ...byte) []byte {
	unit := append([]byte{header}, body...)
	out := make([]byte, lenSize)
	for i, n := 0, len(unit); i < lenSize; i++ {
		out[lenSize-1-i] = byte(n >> (8 * i))
	}
	return append(out, unit...)
}

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func TestHashPacketH264(t *testing.T) {
	aud := nal(4, 0x09, 0xf0)
	sei := nal(4, 0x06, 1, 2, 3)
	idr := nal(4, 0x65, 9, 9, 9, 9)
	slice := nal(4, 0x41, 7)

	tests := []struct {
		name        string
		pkt         []byte
		wantRemoved int
		wantKept    []byte
	}{
		{"AUD first", cat(aud, idr), 1, idr},
		{"SEI then AUD then IDR as x264 emits", cat(sei, aud, idr), 1, cat(sei, idr)},
		{"multiple AUDs", cat(aud, slice, aud), 2, slice},
		{"no AUD", cat(slice), 0, slice},
		{"only an AUD", cat(aud), 1, nil},
		{"nal_ref_idc bits do not matter", cat(nal(4, 0x69, 0xf0), slice), 1, slice},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := md5.New()
			removed, err := hashPacket(h, tc.pkt, DropH264AUD, 4)
			if err != nil {
				t.Fatal(err)
			}
			if removed != tc.wantRemoved {
				t.Errorf("removed = %d, want %d", removed, tc.wantRemoved)
			}
			if want := md5.Sum(tc.wantKept); !bytes.Equal(h.Sum(nil), want[:]) {
				t.Errorf("digest is not that of the kept NAL units")
			}
		})
	}
}

func TestHashPacketHEVCUsesBitsOneToSix(t *testing.T) {
	aud := nal(4, 35<<1, 0x01, 0x50)
	vps := nal(4, 32<<1, 0x01, 0x0c)
	// 0x23 is type 17 once shifted, but 0x23&0x1f is 3: the h264 rule would
	// read it as a slice.
	idr := nal(4, 0x23, 0x01, 0xaf)

	h := md5.New()
	removed, err := hashPacket(h, cat(aud, vps, idr), DropHEVCAUD, 4)
	if err != nil || removed != 1 {
		t.Fatalf("removed = %d, %v", removed, err)
	}
	if want := md5.Sum(cat(vps, idr)); !bytes.Equal(h.Sum(nil), want[:]) {
		t.Fatal("digest is not that of VPS and IDR")
	}
	// The h264 rule must not touch HEVC: 35<<1 = 0x46, and 0x46&0x1f = 6.
	if n, _ := hashPacket(md5.New(), aud, DropH264AUD, 4); n != 0 {
		t.Fatal("DropH264AUD removed an HEVC AUD")
	}
}

func TestHashPacketOtherLengthSizes(t *testing.T) {
	aud, slice := nal(2, 0x09, 0xf0), nal(2, 0x41, 7, 8)
	h := md5.New()
	removed, err := hashPacket(h, cat(aud, slice), DropH264AUD, 2)
	if err != nil || removed != 1 {
		t.Fatalf("removed = %d, %v", removed, err)
	}
	if want := md5.Sum(slice); !bytes.Equal(h.Sum(nil), want[:]) {
		t.Fatal("digest mismatch")
	}
}

func TestHashPacketKeepAllHashesTheRawPacket(t *testing.T) {
	pkt := []byte{1, 2, 3, 4, 5}
	h := md5.New()
	if n, err := hashPacket(h, pkt, KeepAll, 0); err != nil || n != 0 {
		t.Fatalf("= %d, %v", n, err)
	}
	if want := md5.Sum(pkt); !bytes.Equal(h.Sum(nil), want[:]) {
		t.Fatal("KeepAll changed the payload")
	}
}

func TestHashPacketRejectsLengthsThatDoNotTile(t *testing.T) {
	good := nal(4, 0x41, 7)
	for name, pkt := range map[string][]byte{
		"length past the end":   append(append([]byte{}, good...), 0, 0, 0, 9, 0x41),
		"truncated length":      append(append([]byte{}, good...), 0, 0),
		"zero length":           append(append([]byte{}, good...), 0, 0, 0, 0),
		"first length too long": {0, 0, 0, 200, 0x09},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := hashPacket(md5.New(), pkt, DropH264AUD, 4); err == nil {
				t.Fatal("accepted a packet whose NAL units do not tile it")
			}
		})
	}
}

func TestPayloadHashesCutsBySizes(t *testing.T) {
	aud, idr, slice := nal(4, 0x09, 0xf0), nal(4, 0x65, 1, 2), nal(4, 0x41, 3)
	p1, p2 := cat(aud, idr), cat(aud, slice)
	piper := &fakePiper{t: t, stdout: cat(p1, p2)}

	digests, removed, err := PayloadHashes(context.Background(), piper, "/x/a.mkv", 2, []int{len(p1), len(p2)}, DropH264AUD, 4)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 || len(digests) != 2 {
		t.Fatalf("removed = %d, digests = %d", removed, len(digests))
	}
	if digests[0] != md5.Sum(idr) || digests[1] != md5.Sum(slice) {
		t.Fatal("digests are not those of the packets without their AUD")
	}
	if want := "0:2"; piper.argv[7] != want {
		t.Fatalf("map = %q, want %q (argv %q)", piper.argv[7], want, piper.argv)
	}
}

func TestPayloadHashesFailsClosed(t *testing.T) {
	pkt := nal(4, 0x65, 1, 2)
	tests := []struct {
		name   string
		stdout []byte
		sizes  []int
		filter NALFilter
		lenSz  int
		want   string
	}{
		{"short read", pkt[:len(pkt)-1], []int{len(pkt)}, DropH264AUD, 4, "ended inside packet 0"},
		{"missing second packet", pkt, []int{len(pkt), len(pkt)}, DropH264AUD, 4, "ended inside packet 1"},
		{"trailing bytes", cat(pkt, []byte{0}), []int{len(pkt)}, DropH264AUD, 4, "after its last packet"},
		{"lengths do not tile", cat(pkt, []byte{0, 0}), []int{len(pkt) + 2}, DropH264AUD, 4, "past the packet"},
		{"trailing bytes with KeepAll", cat(pkt, []byte{0}), []int{len(pkt)}, KeepAll, 0, "after its last packet"},
		{"bad length size", pkt, []int{len(pkt)}, DropH264AUD, 0, "not 1 to 4"},
		{"negative size", pkt, []int{-1}, KeepAll, 0, "negative size"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			piper := &fakePiper{t: t, stdout: tc.stdout}
			d, n, err := PayloadHashes(context.Background(), piper, "/x/a.mkv", 0, tc.sizes, tc.filter, tc.lenSz)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			if d != nil || n != 0 {
				t.Fatalf("returned %d digests and %d removed alongside an error", len(d), n)
			}
		})
	}
}

func TestPayloadHashesNonZeroExit(t *testing.T) {
	piper := &fakePiper{t: t, exit: 1, stderr: []byte("Stream map '0:9' matches no streams\n")}
	_, _, err := PayloadHashes(context.Background(), piper, "/x/a.mkv", 9, nil, KeepAll, 0)
	if err == nil || !strings.Contains(err.Error(), "matches no streams") {
		t.Fatalf("err = %v", err)
	}
}
