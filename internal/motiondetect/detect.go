package motiondetect

import "bytes"

const (
	xmpMotionMarker  = `GCamera:MotionPhoto="1"`
	mp4BoxTypeMarker = "ftyp"
	boxSizeFieldLen  = 4
)

// Result descrive l'esito del rilevamento su un file JPEG.
type Result struct {
	IsMotion   bool
	VideoBytes []byte
	Reason     string // "published" | "checked" | "lost"
}

func findEmbeddedVideo(jpegBytes []byte) []byte {
	idx := bytes.Index(jpegBytes, []byte(mp4BoxTypeMarker))
	if idx < boxSizeFieldLen {
		return nil
	}
	boxStart := idx - boxSizeFieldLen
	video := jpegBytes[boxStart:]
	if len(video) < 8 {
		return nil
	}
	return video
}

// DetectMotionPhoto cerca il marcatore XMP GCamera:MotionPhoto e un payload
// MP4 in coda al JPEG. Il match testuale di "ftyp" cade 4 byte dopo l'inizio
// reale del box (i 4 byte precedenti sono la box-size, non testuali).
func DetectMotionPhoto(jpegBytes []byte) Result {
	hasXMPFlag := bytes.Contains(jpegBytes, []byte(xmpMotionMarker))
	video := findEmbeddedVideo(jpegBytes)

	if video != nil {
		return Result{IsMotion: true, VideoBytes: video, Reason: "published"}
	}
	if hasXMPFlag {
		return Result{IsMotion: false, VideoBytes: nil, Reason: "lost"}
	}
	return Result{IsMotion: false, VideoBytes: nil, Reason: "checked"}
}
