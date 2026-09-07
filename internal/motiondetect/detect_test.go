package motiondetect

import (
	"bytes"
	"testing"
)

var xmpMarker = []byte(`xmlns:GCamera="http://ns.google.com/photos/1.0/camera/" GCamera:MotionPhoto="1"`)

func fakeMP4Payload() []byte {
	return []byte("\x00\x00\x00\x18ftypisom\x00\x02\x00\x00isomiso2mp41FAKEMDATBYTES")
}

func TestPublishedWhenXMPAndVideoPresent(t *testing.T) {
	jpeg := bytes.Join([][]byte{
		[]byte("JFIF-fake-header"),
		xmpMarker,
		[]byte("restofjpegdata\xff\xd9"),
		fakeMP4Payload(),
	}, nil)

	result := DetectMotionPhoto(jpeg)

	if !result.IsMotion {
		t.Fatal("expected IsMotion true")
	}
	if result.Reason != "published" {
		t.Fatalf("expected reason published, got %s", result.Reason)
	}
	if !bytes.HasPrefix(result.VideoBytes, []byte("\x00\x00\x00\x18ftypisom")) {
		t.Fatalf("expected video bytes to start with ftyp box, got %v", result.VideoBytes[:20])
	}
}

func TestCheckedWhenNoMotionMarkersAtAll(t *testing.T) {
	jpeg := []byte("JFIF-fake-header-plain-photo-no-markers\xff\xd9")

	result := DetectMotionPhoto(jpeg)

	if result.IsMotion {
		t.Fatal("expected IsMotion false")
	}
	if result.Reason != "checked" {
		t.Fatalf("expected reason checked, got %s", result.Reason)
	}
	if result.VideoBytes != nil {
		t.Fatal("expected nil video bytes")
	}
}

func TestLostWhenXMPPresentButVideoMissing(t *testing.T) {
	jpeg := bytes.Join([][]byte{
		[]byte("JFIF-fake-header"),
		xmpMarker,
		[]byte("restofjpegdata\xff\xd9"),
	}, nil)

	result := DetectMotionPhoto(jpeg)

	if result.IsMotion {
		t.Fatal("expected IsMotion false")
	}
	if result.Reason != "lost" {
		t.Fatalf("expected reason lost, got %s", result.Reason)
	}
	if result.VideoBytes != nil {
		t.Fatal("expected nil video bytes")
	}
}
