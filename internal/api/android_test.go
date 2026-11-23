package api

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestAndroidBridgeAutoProvisionsAndExecutes exercises the real mobile flow:
// a client connects with X-GoPlay-Client: android, the instance is created,
// a GPF1 JPEG frame is accepted, an action is dispatched and its ack resolves
// the executor.
func TestAndroidBridgeAutoProvisionsAndExecutes(t *testing.T) {
	ts, _ := newTestServer(t)
	base := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/instance/android-1"
	hdr := http.Header{}
	hdr.Set("X-GoPlay-Client", "android")
	conn, _, err := websocket.DefaultDialer.Dial(base, hdr)
	if err != nil {
		t.Fatalf("dial android bridge: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	readType := func(want string) map[string]any {
		for i := 0; i < 80; i++ {
			var m map[string]any
			if err := conn.ReadJSON(&m); err != nil {
				t.Fatalf("read %s: %v", want, err)
			}
			if m["type"] == want {
				return m
			}
		}
		t.Fatalf("did not receive %q", want)
		return nil
	}
	readType("hello")
	if err := conn.WriteJSON(map[string]any{
		"type": "hello", "protocol": 1, "client": "android",
		"clientVersion": "test", "instanceId": "android-1", "device": "test device",
	}); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	readType("hello_ack")

	img := image.NewRGBA(image.Rect(0, 0, 64, 36))
	for y := 0; y < 36; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 3), uint8(y * 5), 90, 255})
		}
	}
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, img, &jpeg.Options{Quality: 70}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	header, _ := json.Marshal(map[string]any{
		"capturedAtMs": time.Now().UnixMilli(), "width": 64, "height": 36,
		"quality": 70, "sequence": 1, "source": "media_projection",
		"instanceId": "android-1", "bytes": jpg.Len(),
	})
	payload := make([]byte, 0, 8+len(header)+jpg.Len())
	payload = append(payload, []byte("GPF1")...)
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(header)))
	payload = append(payload, n[:]...)
	payload = append(payload, header...)
	payload = append(payload, jpg.Bytes()...)
	if err := conn.WriteMessage(websocket.BinaryMessage, payload); err != nil {
		t.Fatalf("send frame: %v", err)
	}

	act := readType("action")
	id, _ := act["id"].(string)
	if id == "" {
		t.Fatalf("action message has no id: %#v", act)
	}
	if err := conn.WriteJSON(map[string]any{
		"type": "ack", "id": id, "ok": true, "actionType": "wait", "executedMs": 1,
	}); err != nil {
		t.Fatalf("send ack: %v", err)
	}

	// The instance must now report bridge frames and at least one action.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var view snapView
		doJSON(t, http.MethodGet, ts.URL+"/api/instances/android-1", nil, &view)
		if view.Status == "running" && view.Stats.Actions > 0 {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("android instance did not reach running/decisions")
}
