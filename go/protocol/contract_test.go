package protocol

import (
	"encoding/json"
	"os"
	"testing"
)

func TestPublishedContract(t *testing.T) {
	data, err := os.ReadFile("../../protocol/event.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var event Event
	if err = json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	if err = event.Validate(); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile("../../protocol/read.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var read ReadRequest
	if err = json.Unmarshal(data, &read); err != nil {
		t.Fatal(err)
	}
	if len(read.EventIDs) != 1 || read.EventIDs[0] != event.EventID {
		t.Fatal("跨语言事件与已读示例不一致")
	}
}
